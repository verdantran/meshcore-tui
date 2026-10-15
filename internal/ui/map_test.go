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

	"meshcore-tui/internal/admin"
	"meshcore-tui/internal/mapview"
	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/theme"
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
	if !h.m.mapSweeping() || !h.m.animating() || h.m.moving() {
		t.Error("the radar sweep runs at full motion, on its own")
	}
	h.m.ticking = false
	if cmd := h.m.ensureTick(); cmd == nil {
		t.Error("the sweep keeps the clock going, at its slower rate")
	}

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
	h.keys("1M")
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

func TestMapFromPackets(t *testing.T) {
	h := newHarness(t)
	h.hear(channelPacket(t, h, "Public", "Bob", "hello all", []byte{0x5F, 0x77}), 6)
	h.keys("4M")
	if h.m.tab != 2 || h.m.mapv.layers.Selected != 0 {
		t.Errorf("M on a packet opens its route: tab %d, route %d", h.m.tab, h.m.mapv.layers.Selected)
	}
	h.m.mapv.routes = nil
	h.keys("4M")
	if !strings.Contains(h.lastLog(), "scrolled off") {
		t.Error("a route no longer kept")
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
	if len(h.m.mapv.anims) != 0 || h.m.mapAnimating() || h.m.mapSweeping() {
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

func TestMapTraffic(t *testing.T) {
	h := newHarness(t)
	h.openMap()
	for range 3 {
		h.hear(channelPacket(t, h, "Public", "Carol", "busy", []byte{0x4D, 0x1E}), 4) // Carol, Clevedon, Cabot Tower, me
	}
	h.hear(channelPacket(t, h, "Public", "Bob", "hi", []byte{0x5F, 0x77}), 6) // 5F is Kelston or Lansdown
	flows, busiest := h.m.mapFlows()
	if busiest == nil || busiest.count != 3 {
		t.Fatalf("busiest = %+v", busiest)
	}
	shared := 0.0
	for _, f := range h.m.mapv.flows {
		if f.aName == "Bob" || f.bName == "Bob" {
			shared += f.count
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

	// An hour later the counts have halved; a day later they've gone.
	h.clk.t = h.clk.t.Add(time.Hour)
	h.m.now = h.clk.t
	if _, b := h.m.mapFlows(); math.Abs(b.now(h.m.now)-1.5) > 1e-9 {
		t.Errorf("after an hour = %v", b.now(h.m.now))
	}
	before := h.screen()
	h.keys("t")
	if h.m.mapv.layers.Traffic || h.screen() == before || strings.Contains(h.m.mapTitle(), "BUSIEST") {
		t.Error("t hides the traffic layer")
	}
	h.keys("t")
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
