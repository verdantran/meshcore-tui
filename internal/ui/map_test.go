package ui

import (
	"crypto/sha256"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/mapview"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

func placedAdvert(t *testing.T, name string, lat, lon float64, path []byte) []byte {
	t.Helper()
	adv, err := meshcore.NewAdvert(meshcore.NewLocalIdentityFromSeed(sha256.Sum256([]byte(name))),
		meshcore.AdvertAppData{Type: "CHAT", Name: name, Lat: int32(lat * 1e6), Lon: int32(lon * 1e6), HasLocation: true}, 1760000000)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := adv.ToBytes()
	return rawPacket(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, path, payload)
}

// tick runs a frame and whatever it starts, such as loading the map.
func (h *harness) tick() {
	_, cmd := h.m.Update(frameMsg(h.clk.t))
	h.start(cmd)
	h.settle()
}

func (h *harness) openMap() {
	h.t.Helper()
	h.keys("3")
	h.until("the world map", func() bool { return h.m.mapv.world != nil })
}

func TestMapScreen(t *testing.T) {
	h := newHarness(t)
	h.openMap()
	scr := h.screen()
	for _, want := range []string{"MAP · 16 NODES PLACED · 0 ROUTES", "◉", "Dundry", "press d to download street detail from OpenFreeMap: 12 map tiles, up to about 2.0 MB", "Natural Earth",
		"Adverts and messages" /* in the help below */} {
		if want == "Adverts and messages" {
			continue
		}
		if !strings.Contains(scr, want) {
			t.Errorf("map lacks %q:\n%s", want, scr)
		}
	}
	h.advance(5 * time.Second)
	if !h.m.mapBeaconOn() || !h.m.animating() || h.m.moving() {
		t.Error("the beacon runs at full motion, on its own")
	}
	h.m.ticking = false
	if cmd := h.m.ensureTick(); cmd == nil {
		t.Error("the beacon keeps the clock going, at its slower rate")
	}
	h.keys(":beacon off")
	h.key(tea.KeyEnter)
	h.m.ticking = false
	if h.m.mapBeaconOn() || h.m.ensureTick() != nil {
		t.Error(":beacon off stops the beacon and its clock")
	}
	h.keys(":beacon on")
	h.key(tea.KeyEnter)

	// A message on Public comes in by Kelston or Lansdown, then Purdown; a stranger advertises from Bath.
	h.hear(channelPacket(t, h, "Public", "Bob", "hello all", []byte{0x5F, 0x77}), 6)
	h.hear(placedAdvert(t, "Visitor", 51.38, -2.36, []byte{0xA3}), -3)
	mv := &h.m.mapv
	if len(mv.routes) != 2 || len(mv.anims) < 3 {
		t.Fatalf("routes %d, anims %d", len(mv.routes), len(mv.anims))
	}
	if !strings.Contains(h.m.mapInfo(nil, nil), "ADVERT  Visitor via A3  SNR -3.0") && !strings.Contains(mv.event, "ADVERT  Visitor via A3") {
		t.Errorf("event = %q", mv.event)
	}
	h.advance(5 * time.Second)

	h.keys("]]")
	scr = h.screen()
	if mv.layers.Selected != 1 || !strings.Contains(scr, "ROUTE 2 OF 2") || !strings.Contains(scr, "GRP_TXT PUBLIC") ||
		!strings.Contains(scr, "5F (") || !strings.Contains(scr, "─+6.0→ me") {
		t.Errorf("the picked route names its hops:\n%s", scr)
	}
	h.keys("]")
	if mv.layers.Selected != 1 {
		t.Error("stays on the last route")
	}
	h.keys("[")
	if mv.layers.Selected != 0 || !strings.Contains(h.screen(), "Visitor") {
		t.Error("the newer route")
	}
	h.key(tea.KeyEscape)
	if mv.layers.Selected != -1 {
		t.Error("esc shows every route again")
	}

	start := mv.view
	h.keys("l")
	h.key(tea.KeyUp)
	if mv.view.Center.Lon <= start.Center.Lon || mv.view.Center.Lat <= start.Center.Lat {
		t.Error("moved east and north")
	}
	if !h.m.mapAnimating() || !motion.Running(mv.movedAt, mapGlide, h.m.now) {
		t.Error("moves glide")
	}
	cost := mv.drawCost
	mv.drawCost = time.Second
	h.keys("h")
	if h.m.mapGliding() || h.m.mapCurrent() != mv.view {
		t.Error("a map too slow to draw a frame in time jumps instead of gliding")
	}
	mv.drawCost = cost
	h.keys("l")
	h.keys("++")
	if mv.view.Zoom != start.Zoom+1 {
		t.Errorf("zoom = %v", mv.view.Zoom)
	}
	h.keys("-m")
	if mv.view.Center != start.Center || mv.view.Zoom != start.Zoom {
		t.Error("m goes back to the whole mesh")
	}
	h.keys("Lr")
	if mv.layers.Labels || mv.layers.Roads {
		t.Error("labels and roads off")
	}

	// A new pane size refits the whole mesh, until the map's been moved by hand.
	h.m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if h.screen(); mv.view.Zoom >= start.Zoom || !mv.auto {
		t.Errorf("refit when smaller: %v", mv.view.Zoom)
	}
	h.keys("+")
	small := mv.view
	h.m.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	if h.screen(); mv.view.Zoom != small.Zoom || mv.view.Center != small.Center {
		t.Error("kept where it was moved to")
	}
	h.keys("?")
	if !strings.Contains(h.screen(), "only adverts and messages on open channels travel the map") {
		t.Error("help")
	}
	h.keys("?")

	// Leaving and coming back keeps the view, and M opens the map from anywhere.
	h.keys("6M")
	if h.m.tab != 2 {
		t.Error("M opens the map")
	}
	h.keys("1:")
	h.typeLine("map")
	if h.m.tab != 2 {
		t.Error(":map opens the map")
	}
}

func TestMapIgnoresPrivateTraffic(t *testing.T) {
	h := newHarness(t)
	h.openMap()
	h.hear(rawPacket(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeTxtMsg, []byte{0xA3}, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}), 4)
	if len(h.m.mapv.routes) != 0 || len(h.m.mapv.anims) != 0 {
		t.Error("a direct message never reaches the map")
	}
}

func TestPathFromPackets(t *testing.T) {
	h := newHarness(t)
	h.hear(channelPacket(t, h, "Public", "Carol", "busy", []byte{0x4D, 0x1E}), 4)
	h.hear(channelPacket(t, h, "Public", "Bob", "hello all", []byte{0x5F, 0x77}), 6)
	h.hear(channelPacket(t, h, "Public", "Bob", "hello all", []byte{0xA3}), -2) // the same message again, another way
	h.keys("4M")
	h.until("the map", func() bool { return h.m.mapv.world != nil })
	h.m.View() // the very first frame, before any glide
	if h.m.mapv.view.Zoom < 9 || h.m.mapCurrent() != h.m.mapv.view {
		t.Errorf("opens on the path rather than gliding in from the world: zoom %v", h.m.mapv.view.Zoom)
	}
	scr := h.screen()
	p := h.m.mapv.path
	if h.m.tab != 2 || p == nil || len(p.routes) != 2 || p.back != 3 {
		t.Fatalf("M on a packet opens its path: tab %d, %+v", h.m.tab, p)
	}
	for _, want := range []string{"PATH · GRP_TXT PUBLIC FROM BOB · 1 OF 2", "Bob", "Dundry", "-2.0", "─-2.0→ me", "esc back to packets"} {
		if !strings.Contains(scr, want) {
			t.Errorf("path lacks %q:\n%s", want, scr)
		}
	}
	for _, other := range []string{"Carol", "Twmbarlwm", "Purdown"} {
		if strings.Contains(scr, other) {
			t.Errorf("only the path's nodes are shown, not %s:\n%s", other, scr)
		}
	}
	h.keys("]")
	if scr := h.screen(); !strings.Contains(scr, "2 OF 2") || !strings.Contains(scr, "Purdown") || !strings.Contains(scr, "5F (") {
		t.Errorf("the other copy:\n%s", scr)
	}
	h.keys("]")
	if p.sel != 1 {
		t.Error("stays on the last copy")
	}
	h.keys("[")
	if p.sel != 0 {
		t.Error("back to the first")
	}
	h.keys("+td") // move it, and the full map's keys don't apply
	if h.m.mapv.fetching || !h.m.mapv.layers.Traffic {
		t.Error("t and d belong to the full map")
	}
	h.keys("m")
	if !p.fit {
		t.Error("m fits the path again")
	}
	saved := p.saved
	h.key(tea.KeyEscape)
	if h.m.tab != 3 || h.m.mapv.path != nil || h.m.mapv.view != saved {
		t.Errorf("esc goes back to Packets and leaves the full map as it was: tab %d", h.m.tab)
	}

	// M from another screen while a path is showing opens the full map instead.
	h.keys("M")
	h.keys("6M")
	if h.m.mapv.path != nil || h.m.tab != 2 {
		t.Error("M from elsewhere is the full map")
	}
}

func (h *harness) openConvo(title string) {
	h.t.Helper()
	h.keys("1")
	h.m.chats.reading, h.m.chats.writing = false, false
	for h.m.chats.sel > 0 {
		h.key(tea.KeyUp)
	}
	for cur, _ := h.m.selectedConvo(); cur.Title != title; cur, _ = h.m.selectedConvo() {
		if h.m.chats.sel >= len(h.m.chats.infos)-1 {
			h.t.Fatalf("no conversation %s", title)
		}
		h.key(tea.KeyDown)
	}
}

func (h *harness) incoming(in radio.Incoming) {
	in.At, in.SentAt = h.clk.t, h.clk.t
	h.m.Update(eventMsg{in})
	h.settle()
}

func TestMessagePaths(t *testing.T) {
	h := newHarness(t)
	// The packet first, then the message: the message is given the packet's path.
	h.hear(channelPacket(t, h, "Public", "Bob", "where are you", []byte{0x5F, 0x77}), 6)
	h.hear(channelPacket(t, h, "Public", "Bob", "where are you", []byte{0xA3}), 2) // a repeat by another way
	h.incoming(radio.Incoming{Channel: true, ChannelIdx: 0, Hops: 2, Text: "Bob: where are you", SNR: 6, HasSNR: true})
	// The message first, then its packet.
	h.incoming(radio.Incoming{Channel: true, ChannelIdx: 0, Hops: 1, Text: "Carol: hello"})
	h.hear(channelPacket(t, h, "Public", "Carol", "hello", []byte{0x1E}), 4)
	// One whose packet was never heard.
	h.incoming(radio.Incoming{Channel: true, ChannelIdx: 0, Hops: 3, Text: "Dan: unheard"})

	h.openConvo("Public")
	scr := h.screen()
	for _, want := range []string{"2 hops via 5F·77", "1 hop via 1E", "3 hops", "→ pick a message to reply to or see its route"} {
		if !strings.Contains(scr, want) {
			t.Errorf("chats lack %q:\n%s", want, scr)
		}
	}

	// → picks the newest message; ↑↓ move; the keys for it are on screen.
	h.key(tea.KeyRight)
	c := &h.m.chats
	if !c.reading || c.msgs[h.m.picked()].Text != "unheard" {
		t.Fatal("reading from the newest")
	}
	h.keys("M")
	if h.m.mapv.path != nil || !strings.Contains(h.lastLog(), "only that it took 3 hops") {
		t.Errorf("no path known: %s", h.lastLog())
	}
	h.key(tea.KeyUp)
	h.key(tea.KeyUp)
	scr = h.screen()
	if c.msgs[h.m.picked()].Text != "where are you" || !strings.Contains(scr, "↑↓ messages · r reply · M route on the map") ||
		!strings.Contains(scr, "r reply · M route") {
		t.Fatalf("picked Bob's message:\n%s", scr)
	}
	h.keys("M")
	p := h.m.mapv.path
	if p == nil || p.title != "message from Bob" || len(p.routes) != 2 || p.back != 0 || len(p.routes[0].hops) != 2 {
		t.Fatalf("Bob's path, then the repeat heard another way: %+v", p)
	}
	if r, nodes := h.m.resolve(p.routes[0], h.m.mapNodes()); len(r.Stops) != 4 || nodes[r.Stops[2].Nodes[0]].Name != "Purdown" || !r.Links[2].Known {
		t.Errorf("Bob, 5F, Purdown, me: %+v", r.Stops)
	}
	h.key(tea.KeyEscape)
	if h.m.tab != 0 || !c.reading {
		t.Error("esc comes back to the picked message")
	}

	// r replies, mentioning them.
	h.keys("r")
	if !c.writing || c.input.text != "@[Bob] " || c.reading {
		t.Errorf("reply: %q", c.input.text)
	}
	h.key(tea.KeyEscape)
	h.key(tea.KeyRight)
	h.key(tea.KeyLeft)
	if c.reading {
		t.Error("← goes back to the conversations")
	}
	h.key(tea.KeyRight)
	h.key(tea.KeyDown) // can't go past the newest
	h.keys("g")
	h.key(tea.KeyDown)
	if c.msgs[h.m.picked()].Text != "unheard" {
		t.Error("stays on the newest")
	}

	// A direct message to us from Alice: its packet names her key byte and us, and gives its path.
	alice := h.contact("Alice")
	dm := rawPacket(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeTxtMsg, []byte{0xA3}, append([]byte{h.m.ready.Self.PubKey[0], alice.Key[0]}, make([]byte, 20)...))
	h.hear(dm, 5)
	h.incoming(radio.Incoming{From: alice.Prefix(), Hops: 1, Text: "psst"})
	h.openConvo("Alice")
	if !strings.Contains(h.screen(), "1 hop via A3") {
		t.Errorf("the direct message's path:\n%s", h.screen())
	}
	h.keys("M")
	if p := h.m.mapv.path; p == nil || p.title != "message from Alice" || len(p.routes[0].hops) != 1 {
		t.Fatalf("direct message path: %+v", p)
	}
	h.key(tea.KeyEscape)
	h.key(tea.KeyRight)
	h.keys("r")
	if c.input.text != "" || !c.writing {
		t.Error("a direct reply needs no mention")
	}
	h.key(tea.KeyEscape)
	for _, row := range h.m.inspector.rows {
		if row.dec.Type == "TXT_MSG" {
			t.Error("direct messages never reach the Packets screen")
		}
	}

	// A message we sent shows the route to the contact.
	h.m.chats.msgs = append(h.m.chats.msgs, store.Message{ID: 9999, Out: true, Convo: store.ContactConvo(alice.Key), Sender: "me", Text: "hi"})
	h.m.showMessagePath(h.m.chats.msgs[len(h.m.chats.msgs)-1])
	if p := h.m.mapv.path; p == nil || p.title != "your route to Alice" {
		t.Errorf("own message: %+v", p)
	}
	h.m.closePath()
	h.m.showMessagePath(store.Message{Out: true, Convo: store.ChannelConvo(0)})
	if !strings.Contains(h.lastLog(), "flood out from you") {
		t.Error("own channel message")
	}
}

func TestPathFromContacts(t *testing.T) {
	h := newHarness(t)
	h.openMap()
	dundry := h.contact("Dundry")
	h.m.showContactPath(dundry)
	if h.m.mapv.path != nil || !strings.Contains(h.lastLog(), "no route to Dundry is known") {
		t.Errorf("a repeater with no route: %s", h.lastLog())
	}
	carol := h.contact("Carol") // stored route Dundry, Cabot Tower
	h.m.showContactPath(carol)
	p := h.m.mapv.path
	if p == nil || p.title != "your route to Carol" {
		t.Fatalf("stored route: %+v", p)
	}
	if r, nodes := h.m.resolve(p.routes[0], h.m.mapNodes()); len(r.Stops) != 4 || nodes[r.Stops[0].Nodes[0]].Name != "me" || nodes[r.Stops[3].Nodes[0]].Name != "Carol" {
		t.Errorf("me, Dundry, Cabot Tower, Carol: %+v", r.Stops)
	}
	h.m.closePath()

	// A trace out through Cabot Tower to Dundry and back: the map shows the way out, every hop measured.
	tag := uint32(77)
	h.m.contacts.traces[tag] = &traceRun{key: dundry.Key, result: &radio.TraceResult{Hashes: []byte{0x1E, 0xA3, 0x1E}, SNRs: []float32{8, -3, 5}, LastSNR: 6}, at: h.m.now}
	h.m.contacts.lastTrace[dundry.Key] = tag
	h.keys("2")
	h.m.showContactPath(dundry)
	p = h.m.mapv.path
	if p.back != 1 {
		t.Errorf("esc goes back to Contacts: %d", p.back)
	}
	if p == nil || !strings.HasPrefix(p.title, "trace to Dundry") {
		t.Fatalf("trace: %+v", p)
	}
	r, nodes := h.m.resolve(p.routes[0], h.m.mapNodes())
	if len(r.Stops) != 3 || nodes[r.Stops[1].Nodes[0]].Name != "Cabot Tower" || !r.Links[0].Known || r.Links[1].SNR != -3 {
		t.Errorf("trace route %+v", r)
	}
	if scr := h.screen(); !strings.Contains(scr, "+8.0") || !strings.Contains(scr, "-3.0") {
		t.Errorf("hop signal labels:\n%s", scr)
	}
	h.m.closePath()

	// A companion with no route, trace or advert path floods.
	bob := h.contact("Bob")
	bob.PathLen = -1
	delete(h.m.contacts.advert, bob.Key)
	h.m.showContactPath(bob)
	if h.m.mapv.path != nil || !strings.Contains(h.lastLog(), "no route to Bob is known") {
		t.Errorf("no route: %s", h.lastLog())
	}
	h.m.contacts.advert[bob.Key] = radio.AdvertPath{At: h.m.now, Path: []byte{0x5F}}
	h.m.showContactPath(bob)
	if p := h.m.mapv.path; p == nil || p.title != "Bob's advert" {
		t.Errorf("advert path: %+v", p)
	}
	h.m.closePath()

	// A path with no placed nodes says so rather than drawing nothing.
	h.m.showPath("nowhere", "far", []mapRoute{{what: "test", from: mapEnd{name: "ghost"}, to: mapEnd{name: "far"}, hops: [][]byte{{0x99}}}})
	if scr := h.screen(); !strings.Contains(scr, "None of this path's nodes has a location") || !strings.Contains(scr, "99 (no location)") {
		t.Errorf("unplaced:\n%s", scr)
	}
	h.m.showPath("empty", "", nil)
	if h.m.mapv.path.title != "nowhere" {
		t.Error("nothing to show")
	}
}

func TestMapLinkSignal(t *testing.T) {
	h := newHarness(t)
	h.openMap()
	h.hear(channelPacket(t, h, "Public", "Bob", "hello all", []byte{0xA3, 0x77}), 6)
	r, nodes := h.m.resolve(h.m.mapv.routes[0], h.m.mapNodes())
	if r.Links[1].Known || !r.Links[2].Known {
		t.Fatalf("only the last hop is measured: %+v", r.Links)
	}
	// A trace from us through Dundry and Purdown teaches their link.
	h.m.Update(eventMsg{radio.TraceResult{Hashes: []byte{0xA3, 0x77}, SNRs: []float32{7, -1.5}}})
	r, _ = h.m.resolve(h.m.mapv.routes[0], nodes)
	if !r.Links[1].Known || r.Links[1].SNR != -1.5 {
		t.Errorf("link from a trace: %+v", r.Links)
	}
	// So does a repeater's neighbour list, and the newer reading wins.
	h.clk.t = h.clk.t.Add(time.Minute)
	h.m.now = h.clk.t
	purdown := h.contact("Purdown")
	sess := h.m.admin.Session(purdown)
	sess.Neighbours, sess.NeighbourAt = []admin.NeighbourRow{{Prefix: []byte{0xA3, 1, 2, 3}, SNR: 9.5}}, h.m.now
	if snr, ok := h.m.linkSNR(0xA3, 0x77); !ok || snr != 9.5 {
		t.Errorf("neighbour reading = %v %v", snr, ok)
	}
	h.m.mapv.links[[2]byte{0x01, 0x02}] = linkReading{3, h.m.now.Add(-2 * linkMaxAge)}
	if _, ok := h.m.linkSNR(0x01, 0x02); ok {
		t.Error("old readings expire")
	}
	// Our own transmissions ring out from us.
	h.m.Update(eventMsg{radio.Transmit{}})
	if last := h.m.mapv.anims[len(h.m.mapv.anims)-1]; !last.Outward || last.Kind != mapview.Ripple {
		t.Error("transmit")
	}
}

func emptyTileServer(t *testing.T, status int, hold chan struct{}) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/planet" {
			w.Write([]byte(`{"tiles":["` + srv.URL + `/t/{z}/{x}/{y}.pbf"]}`))
			return
		}
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		if status != http.StatusOK {
			http.Error(w, "busy", status)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMapDownload(t *testing.T) {
	h := newHarness(t)
	hold := make(chan struct{})
	srv := emptyTileServer(t, http.StatusOK, hold)
	h.m.opt.MapDir, h.m.opt.MapIndex = t.TempDir(), srv.URL+"/planet"
	h.openMap()
	h.keys("d")
	if scr := h.screen(); !h.m.mapv.fetching || !strings.Contains(scr, "DOWNLOADING") || !strings.Contains(scr, "of 12 tiles") {
		t.Fatalf("downloading:\n%s", scr)
	}
	h.keys("d") // already going
	close(hold)
	h.until("street detail", func() bool { return h.m.mapv.detail != nil })
	if _, err := os.Stat(h.m.detailPath()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.screen(), "street detail added: 12 tiles") {
		t.Errorf("done:\n%s", h.screen())
	}
	h.m.mapv.note = ""
	h.keys("d")
	if !strings.Contains(h.m.mapv.note, "already covers your mesh") {
		t.Error(h.m.mapv.note)
	}
	h.m.mapv.note = ""
	h.keys("+++")
	h.advance(time.Second)
	if !strings.Contains(h.screen(), mapview.DetailCredit) {
		t.Errorf("credit once zoomed in over the detail:\n%s", h.screen())
	}

	// A new node outside the area: the map offers to add just its tiles.
	h.m.ready.Self.Lat, h.m.ready.Self.Lon = 51.3, -1.9
	if !strings.Contains(h.screen(), "1 node is outside your street detail · press d to download") {
		t.Errorf("extend:\n%s", h.screen())
	}
	h.m.ready.Self.Lat, h.m.ready.Self.Lon = 51.44, -2.598

	// Restarting reads it back; :map update fetches it again and :map remove deletes it.
	h.m.mapv.world, h.m.mapv.detail = nil, nil
	h.m.mapv.loading = false
	h.tick()
	h.until("reloaded", func() bool { return h.m.mapv.detail != nil })
	h.keys(":")
	h.typeLine("map update")
	h.until("updated", func() bool { return !h.m.mapv.fetching && strings.Contains(h.m.mapv.note, "12 tiles") })
	h.keys(":")
	h.typeLine("map remove")
	if h.m.mapv.detail != nil || !strings.Contains(h.lastLog(), "street detail deleted") {
		t.Error("removed")
	}
	if _, err := os.Stat(h.m.detailPath()); !os.IsNotExist(err) {
		t.Error("file gone")
	}
	for _, bad := range []string{"map nope", "map update"} {
		h.keys(":")
		h.typeLine(bad)
		if h.m.log[len(h.m.log)-1].tone != radio.ToneFail {
			t.Errorf(":%s should fail", bad)
		}
	}
	h.keys(":")
	h.typeLine("map remove")
	if h.m.log[len(h.m.log)-1].tone == radio.ToneFail {
		t.Error("removing nothing is fine")
	}

	// A broken file says so.
	os.WriteFile(h.m.detailPath(), []byte("junk"), 0o644)
	h.m.mapv.world = nil
	h.tick()
	h.until("reloaded", func() bool { return h.m.mapv.world != nil })
	if !strings.Contains(h.screen(), "couldn't be read") {
		t.Errorf("broken file:\n%s", h.screen())
	}
}

func TestMapDownloadStopsAndFails(t *testing.T) {
	h := newHarness(t)
	hold := make(chan struct{})
	defer close(hold)
	h.m.opt.MapDir, h.m.opt.MapIndex = t.TempDir(), emptyTileServer(t, http.StatusOK, hold).URL+"/planet"
	h.openMap()
	h.keys("d")
	h.key(tea.KeyEscape)
	h.until("stopped", func() bool { return !h.m.mapv.fetching })
	if !strings.Contains(h.m.mapv.note, "download stopped") || h.m.mapv.noteBad {
		t.Errorf("stopped: %q", h.m.mapv.note)
	}
	h.key(tea.KeyEscape) // nothing to stop or unpick: the key falls through

	h.m.opt.MapIndex = emptyTileServer(t, http.StatusServiceUnavailable, nil).URL + "/planet"
	h.keys("d")
	h.until("failed", func() bool { return !h.m.mapv.fetching })
	if !strings.Contains(h.screen(), "OpenFreeMap answered 503") || !h.m.mapv.noteBad {
		t.Errorf("failure:\n%s", h.screen())
	}

	// Nowhere to save it.
	h.m.opt.MapIndex = emptyTileServer(t, http.StatusOK, nil).URL + "/planet"
	file := h.m.opt.MapDir + "/blocker"
	os.WriteFile(file, nil, 0o644)
	h.m.opt.MapDir = file
	h.keys("d")
	h.until("unsaved", func() bool { return !h.m.mapv.fetching })
	if !strings.Contains(h.m.mapv.note, "couldn't be saved") {
		t.Errorf("unsaved: %q", h.m.mapv.note)
	}
}

func TestMapWithNoLocations(t *testing.T) {
	h := newHarness(t)
	h.m.ready.Self.Lat, h.m.ready.Self.Lon = 0, 0
	chat := h.m.chat
	h.m.chat = nil
	h.openMap()
	if !strings.Contains(h.screen(), "No node has a location yet") || h.m.mapv.view.Zoom != mapview.MinZoom {
		t.Errorf("nothing placed:\n%s", h.screen())
	}
	h.keys("d")
	if !strings.Contains(h.m.mapv.note, "no area to download") {
		t.Error(h.m.mapv.note)
	}
	h.m.chat = chat
}

func TestMapLoadingAndMotionOff(t *testing.T) {
	h := newHarness(t)
	h.m.mapv.loading = true // as if the world map were still being read
	h.keys("3")
	if !strings.Contains(h.screen(), "loading the world map") || !h.m.mapAnimating() {
		t.Error("loading")
	}
	if _, handled := h.m.mapKey("m"); handled {
		t.Error("keys wait for the map")
	}
	h.m.mapv.loading = false
	h.tick()
	h.until("loaded", func() bool { return h.m.mapv.world != nil })
	h.m.setMotion(motion.Off)
	h.hear(channelPacket(t, h, "Public", "Bob", "hi", []byte{0x77}), 2)
	if len(h.m.mapv.anims) != 0 || h.m.mapAnimating() || h.m.mapBeaconOn() {
		t.Error("no animation with motion off")
	}
	h.keys("l")
	if h.m.mapCurrent() != h.m.mapv.view {
		t.Error("moves are instant with motion off")
	}
}

func TestMapFollowsThemeFade(t *testing.T) {
	h := newHarness(t)
	h.openMap()
	h.screen()
	h.m.setTheme(theme.Paper)
	h.advance(100 * time.Millisecond) // part way through the crossfade
	h.m.View()
	h.advance(time.Second) // and done, with nothing else changing
	land := mapview.PaletteFor(theme.Paper).Land
	want := fmt.Sprintf("48;2;%d;%d;%d", land.R, land.G, land.B)
	if !strings.Contains(h.m.View().Content, want) {
		t.Error("the map ends the fade in the new theme's colours")
	}
}

func TestMapBusiestStaysPut(t *testing.T) {
	m, _ := newTestModel(t, motion.Full, 100, 30)
	mv := &m.mapv
	for _, name := range []string{"Hilltop", "Dundry", "Shed", "Mast"} {
		mv.flows[name] = &flow{aID: name, bID: "Base", aName: name, bName: "Base", count: [2]float64{5, 5}, at: m.now}
	}
	first := ""
	for range 50 {
		_, b := m.mapFlows()
		if first == "" {
			first = b.aName
		}
		if b.aName != first {
			t.Fatalf("busiest moved from %s to %s with nothing changing", first, b.aName)
		}
	}
	mv.flows["Shed"].count[0] = 5.2
	if _, b := m.mapFlows(); b.aName != first && first != "Shed" {
		t.Error("a link a little busier doesn't take over")
	}
	mv.flows["Mast"].count[0] = 9
	if _, b := m.mapFlows(); b.aName != "Mast" {
		t.Errorf("busiest = %s, want Mast, now clearly busiest", b.aName)
	}
}

func TestMapTraffic(t *testing.T) {
	h := newHarness(t)
	h.openMap()
	for range 3 {
		h.hear(channelPacket(t, h, "Public", "Carol", "busy", []byte{0x4D, 0x1E}), 4) // Carol, Clevedon, Cabot Tower, me
	}
	h.hear(channelPacket(t, h, "Public", "Bob", "hi", []byte{0x5F, 0x77}), 6) // 5F is Kelston or Lansdown
	flows, busiest := h.m.mapFlows()
	if busiest == nil || busiest.count[0] != 3 {
		t.Fatalf("busiest = %+v", busiest)
	}
	shared := 0.0
	for _, f := range h.m.mapv.flows {
		if f.aName == "Bob" || f.bName == "Bob" {
			shared += f.count[0]
		}
	}
	if math.Abs(shared-1) > 1e-9 {
		t.Errorf("Bob's hop to 5F is shared between its candidates, totalling one: %v", shared)
	}
	top := 0.0
	for _, f := range flows {
		top = max(top, f.Level)
	}
	if top != 1 || len(flows) < 5 {
		t.Errorf("levels top out at 1: %v, %d flows", top, len(flows))
	}
	if !strings.Contains(h.m.mapTitle(), "BUSIEST ") {
		t.Errorf("title = %s", h.m.mapTitle())
	}

	if !strings.Contains(h.m.mapTitle(), "BUSIEST LATELY") {
		t.Errorf("title = %s", h.m.mapTitle())
	}

	// An hour later the hour's counts have halved, and the recent ones have halved four times; a day later
	// they've gone.
	h.clk.t = h.clk.t.Add(time.Hour)
	h.m.now = h.clk.t
	if _, b := h.m.mapFlows(); math.Abs(b.now(h.m.now, 1)-1.5) > 1e-9 || math.Abs(b.now(h.m.now, 0)-3.0/16) > 1e-9 {
		t.Errorf("after an hour = %v, %v", b.now(h.m.now, 0), b.now(h.m.now, 1))
	}
	h.keys("t")
	if h.m.mapv.window != 1 || !strings.Contains(h.m.mapTitle(), "BUSIEST THIS HOUR") {
		t.Errorf("t counts the last hour: %s", h.m.mapTitle())
	}
	before := h.screen()
	h.keys("t")
	if h.m.mapv.layers.Traffic || h.screen() == before || strings.Contains(h.m.mapTitle(), "BUSIEST") {
		t.Error("a third t hides the traffic layer")
	}
	h.keys("t")
	if !h.m.mapv.layers.Traffic || h.m.mapv.window != 0 {
		t.Error("and t shows it again, counting recent traffic")
	}
	h.clk.t = h.clk.t.Add(24 * time.Hour)
	h.m.now = h.clk.t
	if flows, b := h.m.mapFlows(); len(flows) != 0 || b != nil || len(h.m.mapv.flows) != 0 {
		t.Error("quiet links are forgotten")
	}
}

// The static map is only redrawn when something on it changes; animation frames reuse it. A cache key that
// shuffled from frame to frame once made every frame redraw the whole map.
func TestMapRedrawsOnlyOnChange(t *testing.T) {
	h := newHarness(t)
	h.openMap()
	for i, p := range [][]byte{{0x4D, 0x1E}, {0x5F, 0x77}, {0xB2, 0xA3}, {0xE1, 0xB2, 0xA3}, {0x90, 0x77}, {0x08, 0x90, 0x77}} {
		h.hear(channelPacket(t, h, "Public", []string{"Carol", "Bob", "Alice", "Dan"}[i%4], "x", p), float32(6-i))
	}
	h.screen()
	for range 60 {
		key := h.m.mapv.cacheKey
		h.clk.t = h.clk.t.Add(motion.FrameDur)
		h.m.Update(frameMsg(h.clk.t))
		h.m.View()
		if h.m.mapv.cacheKey != key {
			t.Fatalf("redrawn with nothing changed:\n%s\n%s", key, h.m.mapv.cacheKey)
		}
	}
}

func TestReadingScrollsOnlyAsNeeded(t *testing.T) {
	h := newHarness(t)
	for i := range 30 {
		h.incoming(radio.Incoming{Channel: true, ChannelIdx: 0, Hops: 1, Text: fmt.Sprintf("Bob: message %d", i)})
	}
	h.openConvo("Public")
	h.key(tea.KeyRight)
	h.key(tea.KeyUp)
	if scr := h.screen(); h.m.chats.scroll != 0 || !strings.Contains(scr, "message 29") {
		t.Errorf("one up keeps the newest in view:\n%s", scr)
	}
	for range 25 {
		h.key(tea.KeyUp)
	}
	scr := h.screen()
	picked := h.m.chats.msgs[h.m.picked()].Text
	if h.m.chats.scroll == 0 || !strings.Contains(scr, picked) {
		t.Errorf("scrolled to keep %q in view (scroll %d):\n%s", picked, h.m.chats.scroll, scr)
	}
	for range 30 {
		h.key(tea.KeyDown)
	}
	if h.m.chats.scroll != 0 {
		t.Errorf("back down at the newest: scroll %d", h.m.chats.scroll)
	}
}

func TestPathReplays(t *testing.T) {
	h := newHarness(t)
	h.hear(channelPacket(t, h, "Public", "Bob", "hello all", []byte{0x5F, 0x77}), 6)
	h.hear(channelPacket(t, h, "Public", "Bob", "hello all", []byte{0xA3}), -2)
	h.keys("4M")
	h.until("the map", func() bool { return h.m.mapv.world != nil })
	still := h.screen()
	p := h.m.mapv.path
	if p.playAt.IsZero() || !h.m.mapAnimating() {
		t.Fatal("the route plays on opening")
	}
	h.clk.t = p.playAt.Add(mapview.HopDur)
	if moving := h.screen(); moving == still {
		t.Error("a comet runs along it")
	}
	h.clk.t = h.clk.t.Add(10 * time.Second)
	if h.m.mapAnimating() {
		t.Error("and finishes")
	}
	h.keys("]")
	h.screen()
	if p.playAt != h.clk.t || !h.m.mapAnimating() {
		t.Error("another route plays when picked")
	}
	h.clk.t = h.clk.t.Add(10 * time.Second)
	h.key(tea.KeySpace)
	h.screen()
	if p.playAt != h.clk.t {
		t.Error("space replays it")
	}
	h.m.level = motion.Off
	if h.m.mapAnimating() {
		t.Error("no replay with motion off")
	}
}

func TestMapTrafficKeepsToTheBusiest(t *testing.T) {
	m, _ := newTestModel(t, motion.Full, 100, 30)
	mv := &m.mapv
	add := func(name string, count float64, unsure bool) {
		f := &flow{aID: name, bID: "Base", aName: name, bName: "Base", a: mapview.LatLon{Lat: 51, Lon: count}, b: mapview.LatLon{Lat: 52, Lon: 0}, at: m.now}
		f.add(m.now, count, unsure)
		mv.flows[name] = f
	}
	add("Busiest", 100, false)
	for i := range 20 {
		add(fmt.Sprint("Link ", i), float64(20+i), false)
	}
	add("Quiet", 5, false)
	add("Guess", 50, true)
	flows, _ := m.mapFlows()
	if len(flows) != trafficLinks {
		t.Fatalf("%d links drawn, want the busiest %d", len(flows), trafficLinks)
	}
	for _, f := range flows {
		if f.A.Lon == 5 {
			t.Error("a link with under a tenth of the busiest's traffic isn't drawn")
		}
		if f.A.Lon == 50 && !f.Uncertain || f.A.Lon != 50 && f.Uncertain {
			t.Errorf("only the guessed link is uncertain: %+v", f)
		}
		if f.A.Lon == 100 && f.Level != 1 || f.A.Lon == 25 && f.Level != 0.5 {
			t.Errorf("levels follow the square root of the share: %+v", f)
		}
	}

	if st := sureStop(mapview.Stop{Nodes: []int{3, 4}, Weights: []float64{0.8, 0.2}}); len(st.Nodes) != 1 || st.Weights[0] != 1 {
		t.Errorf("a clear favourite takes the whole hop: %+v", st)
	}
	if st := sureStop(mapview.Stop{Nodes: []int{3, 4}, Weights: []float64{0.6, 0.4}}); len(st.Nodes) != 2 {
		t.Errorf("a close call stays shared: %+v", st)
	}
}

func TestMapPicksOutABusyNode(t *testing.T) {
	h := newHarness(t)
	h.openMap()
	for range 3 {
		h.hear(channelPacket(t, h, "Public", "Carol", "busy", []byte{0x4D, 0x1E}), 4) // Carol, Clevedon, Cabot Tower, me
	}
	h.hear(channelPacket(t, h, "Public", "Dan", "hi", []byte{0x08}), 6) // Dan, Twmbarlwm, me
	hubs := h.m.hubs()
	if len(hubs) < 3 || hubs[0].total < hubs[len(hubs)-2].total || hubs[len(hubs)-1].name != "me" {
		t.Fatalf("busiest first, and us last though every route ends with us: %+v", hubs)
	}
	h.keys("f")
	if h.m.mapv.hub != hubs[0].id || !strings.Contains(h.m.mapTitle(), strings.ToUpper(hubs[0].name)+" · ") {
		t.Errorf("f picks out the busiest node first: %s", h.m.mapTitle())
	}
	h.keys("f")
	if h.m.mapv.hub != hubs[1].id {
		t.Error("f again, the next")
	}
	h.keys("F")
	if h.m.mapv.hub != hubs[0].id {
		t.Error("F goes back")
	}
	h.keys("F")
	if h.m.mapv.hub != "" || !strings.Contains(h.m.mapTitle(), "BUSIEST") {
		t.Error("past the first, everything shows again")
	}
	h.keys("f")
	h.key(tea.KeyEscape)
	if h.m.mapv.hub != "" {
		t.Error("esc shows everything again")
	}
}
