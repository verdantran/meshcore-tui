package ui

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/mapview"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/packets"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

const (
	mapRoutes  = 30                     // routes kept to pick through
	mapAnims   = 40                     // animations running at once, at most
	mapGlide   = 320 * time.Millisecond // pans and zooms ease over this
	mapAgent   = "meshcore-tui (+https://github.com/verdantran/meshcore-tui)"
	mapMargin  = 0.12
	linkMaxAge = 24 * time.Hour // older trace readings no longer colour a link

	trafficFloor = 0.1 // below this a link has gone quiet and isn't drawn
	trafficShare = 0.1 // nor is one with less than this share of the busiest link's traffic
	trafficLinks = 15  // the busiest links drawn, at most
	trafficSure  = 0.7 // a hop this likely to be one repeater is credited to it alone
	trafficHops  = 16  // the hops nearest us that are counted
	trafficKeep  = 256 // links remembered, at most
)

// trafficWindows are the half-lives t steps through. Every link keeps a count for each, so switching is instant.
var trafficWindows = []struct {
	halfLife time.Duration
	busiest  string // how the title names the busiest link
}{{15 * time.Minute, "BUSIEST LATELY"}, {time.Hour, "BUSIEST THIS HOUR"}}

// mapRoute is a packet's journey as heard. It's resolved against the nodes each time it's drawn, so a
// repeater added later, or a trace run later, still places and colours it.
type mapRoute struct {
	id       string
	what     string
	from, to mapEnd // a heard packet ends at us; a stored route or a trace starts from us
	hops     [][]byte
	hopSNR   []float64 // each hop's signal, when a trace measured them; NaN where it didn't
	lastSNR  float64
	measured bool // lastSNR is the last hop's signal at our radio
	at       time.Time
	flood    bool
}

// mapEnd is one end of a route: us, a node by key, or a sender known only by name and perhaps a location.
type mapEnd struct {
	me   bool
	name string
	key  [32]byte
	pos  mapview.LatLon
	kind mapview.Kind
}

func (e mapEnd) label() string {
	if e.me {
		return "me"
	}
	return e.name
}

// packetRoute is a heard packet's journey to us along one copy's path.
func (m *Model) packetRoute(dec packets.Decoded, path []byte, snr float32, at time.Time) mapRoute {
	r := mapRoute{id: fmt.Sprintf("%X/%X", dec.Hash, path), what: dec.Type, to: mapEnd{me: true}, lastSNR: float64(snr),
		measured: true, at: at, flood: dec.Flood}
	switch dec.Type {
	case "ADVERT":
		r.from = mapEnd{name: dec.Name, pos: mapview.LatLon{Lat: dec.Lat, Lon: dec.Lon}, kind: mapview.KindOther}
		for _, n := range m.mapNodes() {
			if n.Name == dec.Name {
				r.from.kind = n.Kind
				if _, manual := m.manualPlace(n.Key); manual || r.from.pos.Unplaced() {
					r.from.pos = n.Pos
				}
			}
		}
	default:
		r.what, r.from = strings.TrimSpace(dec.Type+" "+dec.Channel), mapEnd{name: dec.Sender}
	}
	r.hops = splitPath(path, dec.HashSize, dec.Flood)
	return r
}

// splitPath cuts a flood packet's path into hop hashes. A direct packet's path is the route ahead of it, not
// the one it came by, so it isn't used.
func splitPath(path []byte, size int, flood bool) [][]byte {
	var hops [][]byte
	if flood && size > 0 {
		for i := 0; i+size <= len(path); i += size {
			hops = append(hops, path[i:i+size])
		}
	}
	return hops
}

type linkReading struct {
	snr float64
	at  time.Time
}

// flow is the traffic seen crossing one link: a count that decays, as of when it was last added to.
type flow struct {
	a, b         mapview.LatLon
	aID, bID     string
	aName, bName string
	count        [2]float64 // one per traffic window
	unsure       [2]float64 // the part of count from hops that could be one of several repeaters
	at           time.Time
}

func decayed(v float64, d time.Duration, w int) float64 {
	return v * math.Exp2(-d.Hours()/trafficWindows[w].halfLife.Hours())
}

func (f *flow) now(t time.Time, w int) float64 { return decayed(f.count[w], t.Sub(f.at), w) }

func (f *flow) add(t time.Time, n float64, unsure bool) {
	for w := range trafficWindows {
		f.count[w] = f.now(t, w) + n
		f.unsure[w] = decayed(f.unsure[w], t.Sub(f.at), w)
		if unsure {
			f.unsure[w] += n
		}
	}
	f.at = t
}

type mapModel struct {
	world   *mapview.World
	loading bool
	detail  *mapview.Detail

	view    mapview.View // where the view is heading
	from    mapview.View // where it was when it set off
	movedAt time.Time
	fitted  bool
	auto    bool // showing the whole mesh, so a new pane size refits it; moving the map by hand stops that
	layers  mapview.Layers
	showing bool // the show menu, for switching kinds of node and layers off
	showSel int

	routes  []mapRoute // newest first
	anims   []mapview.Anim
	links   map[[2]byte]linkReading // signal between two nodes, by their first key bytes
	flows   map[string]*flow        // recent traffic over each link, by the two nodes
	path    *pathView               // one path on its own, opened with M from elsewhere
	event   string                  // the latest packet, typed out under the map
	eventAt time.Time

	fetching   bool
	fetchDone  *atomic.Int32
	fetchTotal int
	cancel     context.CancelFunc
	note       string
	noteBad    bool

	busiest  string        // the flow named in the title, kept until another is clearly busier
	window   int           // which of trafficWindows the traffic layer shows
	hub      string        // a node whose traffic is picked out with f, by nodeID
	drawCost time.Duration // a full redraw, averaged; too slow for a frame and moves jump instead of gliding
	cacheKey string
	cache    *mapview.Canvas
	rows     []string // the cached map as terminal rows; a frame re-renders only the rows animations touch

	stops map[string][]mapview.Stop // routes' stops as last ranked, by hops, ends and candidates
}

func newMapModel() mapModel {
	return mapModel{layers: mapview.Layers{Labels: true, Roads: true, Routes: true, Traffic: true, Selected: -1}, links: map[[2]byte]linkReading{},
		flows: map[string]*flow{}, stops: map[string][]mapview.Stop{}, fetchDone: &atomic.Int32{}}
}

type mapLoadedMsg struct {
	world  *mapview.World
	detail *mapview.Detail
	err    error
}

type detailFetchedMsg struct {
	detail *mapview.Detail
	tiles  int
	bytes  int
	err    error
}

func (m *Model) mapDir() string {
	if m.opt.MapDir != "" {
		return m.opt.MapDir
	}
	return filepath.Join(config.DataDir(), "maps")
}

func (m *Model) detailPath() string { return filepath.Join(m.mapDir(), "detail.json.gz") }

// loadMap decodes the built-in world map and any street detail in the background, the first time the map shows.
func (m *Model) loadMap() tea.Cmd {
	m.mapv.loading = true
	path := m.detailPath()
	return func() tea.Msg {
		w := mapview.LoadWorld()
		d, err := mapview.LoadDetail(path)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		return mapLoadedMsg{w, d, err}
	}
}

func (m *Model) mapMsg(msg tea.Msg) tea.Cmd {
	mv := &m.mapv
	switch msg := msg.(type) {
	case mapLoadedMsg:
		mv.world, mv.detail, mv.loading = msg.world, msg.detail, false
		if msg.err != nil {
			mv.note, mv.noteBad = "street detail couldn't be read ("+msg.err.Error()+"); d downloads it again", true
		}
	case detailFetchedMsg:
		mv.fetching, mv.cancel = false, nil
		switch {
		case errors.Is(msg.err, context.Canceled):
			mv.note, mv.noteBad = "download stopped; d carries on from where it got to", false
		case msg.err != nil:
			mv.note, mv.noteBad = "street detail: "+msg.err.Error()+"; d tries again", true
		default:
			if err := msg.detail.Save(m.detailPath()); err != nil {
				mv.note, mv.noteBad = "street detail couldn't be saved: "+err.Error(), true
				return nil
			}
			m.fetcher().ClearCache()
			mv.detail, mv.cacheKey = msg.detail, ""
			mv.note, mv.noteBad = fmt.Sprintf("street detail added: %d tiles, %.1f MB downloaded", msg.tiles, float64(msg.bytes)/1e6), false
			m.addLog(m.now, "MAP  "+mv.note, radio.ToneOK)
		}
	}
	return nil
}

func (m *Model) fetcher() mapview.Fetcher {
	idx := m.opt.MapIndex
	if idx == "" {
		idx = mapview.OpenFreeMap
	}
	return mapview.Fetcher{Index: idx, Agent: mapAgent, Dir: filepath.Join(m.mapDir(), "tiles"), Client: m.opt.MapClient}
}

// fetchDetail downloads street detail for the tiles the mesh's area is missing; with all set, it fetches
// every tile again, for :map update.
func (m *Model) fetchDetail(all bool) tea.Cmd {
	mv := &m.mapv
	if mv.fetching {
		return nil
	}
	_, box, ok := mapview.MeshArea(m.placedPoints())
	if !ok {
		mv.note, mv.noteBad = "no node has a location yet, so there's no area to download", true
		return nil
	}
	tiles := mv.detail.Missing(box)
	base := mv.detail
	if all && mv.detail != nil {
		tiles, base = append(mv.detail.Tiles, tiles...), nil
	}
	if len(tiles) == 0 {
		mv.note, mv.noteBad = "street detail already covers your mesh", false
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	mv.fetching, mv.cancel, mv.fetchTotal, mv.note = true, cancel, len(tiles), ""
	mv.fetchDone.Store(0)
	f, done := m.fetcher(), mv.fetchDone
	return func() tea.Msg {
		d, n, err := f.Fetch(ctx, base, tiles, func(k int) { done.Store(int32(k)) })
		return detailFetchedMsg{d, len(tiles), n, err}
	}
}

func (m *Model) removeDetail() error {
	if err := os.Remove(m.detailPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	m.mapv.detail, m.mapv.cacheKey = nil, ""
	return nil
}

func kindOf(t radio.ContactType) mapview.Kind {
	switch t {
	case radio.TypeRepeater:
		return mapview.KindRepeater
	case radio.TypeRoom:
		return mapview.KindRoom
	case radio.TypeChat:
		return mapview.KindChat
	}
	return mapview.KindOther
}

// mapNodes are us and every contact, then nodes heard but not added; the map draws those with a location.
// Each is where you put it, if you did, or else where it says it is.
func (m *Model) mapNodes() []mapview.Node {
	self := m.ready.Self
	nodes := []mapview.Node{{Name: "me", Kind: mapview.KindSelf, Pos: mapview.LatLon{Lat: self.Lat, Lon: self.Lon}, Key: self.PubKey}}
	seen := map[[32]byte]bool{self.PubKey: true}
	manual := map[[32]byte]mapview.LatLon{}
	for k, p := range m.adminConfig().Locations {
		if b, err := hex.DecodeString(k); err == nil && len(b) == 32 {
			manual[[32]byte(b)] = mapview.LatLon{Lat: p[0], Lon: p[1]}
		}
	}
	placeOf := func(c radio.Contact) mapview.LatLon {
		if p, ok := manual[c.Key]; ok {
			return p
		}
		return mapview.LatLon{Lat: c.Lat, Lon: c.Lon}
	}
	if m.chat != nil {
		for _, c := range m.chat.Contacts() {
			if !seen[c.Key] {
				seen[c.Key] = true
				nodes = append(nodes, mapview.Node{Name: c.Name, Kind: kindOf(c.Type), Pos: placeOf(c), Key: c.Key})
			}
		}
	}
	for _, h := range m.contacts.heard {
		if !seen[h.c.Key] {
			seen[h.c.Key] = true
			nodes = append(nodes, mapview.Node{Name: h.c.Name, Kind: kindOf(h.c.Type), Pos: placeOf(h.c), Key: h.c.Key})
		}
	}
	return nodes
}

func (m *Model) manualPlace(key [32]byte) (mapview.LatLon, bool) {
	p, ok := m.adminConfig().Locations[radio.KeyHex(key[:])]
	return mapview.LatLon{Lat: p[0], Lon: p[1]}, ok
}

func (m *Model) placedPoints() []mapview.LatLon {
	var pts []mapview.LatLon
	for _, n := range m.mapNodes() {
		if !n.Pos.Unplaced() {
			pts = append(pts, n.Pos)
		}
	}
	return pts
}

// linkSNR is what's known of the signal between two nodes, from a trace or a repeater's neighbour list.
func (m *Model) linkSNR(a, b byte) (float64, bool) {
	r, ok := m.linkTable()[[2]byte{min(a, b), max(a, b)}]
	return r.snr, ok
}

// linkTable is every link's latest reading, by the first key bytes at either end, lowest first.
func (m *Model) linkTable() map[[2]byte]linkReading {
	t := map[[2]byte]linkReading{}
	for pair, r := range m.mapv.links {
		if m.now.Sub(r.at) < linkMaxAge {
			t[pair] = r
		}
	}
	for _, c := range m.allNodes() {
		sess, has := m.admin.Find(c.Key)
		if !has || sess.NeighbourAt.IsZero() {
			continue
		}
		for _, n := range sess.Neighbours {
			if len(n.Prefix) == 0 {
				continue
			}
			pair := [2]byte{min(c.Key[0], n.Prefix[0]), max(c.Key[0], n.Prefix[0])}
			if best, ok := t[pair]; !ok || sess.NeighbourAt.After(best.at) {
				t[pair] = linkReading{float64(n.SNR), sess.NeighbourAt}
			}
		}
	}
	return t
}

func (m *Model) noteLink(a, b byte, snr float64) {
	m.mapv.links[[2]byte{min(a, b), max(a, b)}] = linkReading{snr, m.now}
}

// resolve places a route against the nodes as they are now, adding an end that isn't a contact but has a location.
func (m *Model) resolve(r mapRoute, nodes []mapview.Node) (mapview.Route, []mapview.Node) {
	rs := m.resolver(nodes)
	return rs.resolve(r), rs.nodes
}

// resolver places many routes against the same nodes, working out what they share once.
type resolver struct {
	m     *Model
	nodes []mapview.Node
	index mapview.Index
	links map[[2]byte]linkReading
}

func (m *Model) resolver(nodes []mapview.Node) *resolver {
	return &resolver{m: m, nodes: nodes}
}

func (rs *resolver) resolve(r mapRoute) mapview.Route {
	m, nodes := rs.m, rs.nodes
	defer func() { rs.nodes = nodes }()
	place := func(e mapEnd) int {
		switch {
		case e.me:
			if nodes[0].Pos.Unplaced() {
				return -1
			}
			return 0
		case e.key != [32]byte{}:
			for i, n := range nodes {
				if n.Key == e.key && !n.Pos.Unplaced() {
					return i
				}
			}
		}
		for i, n := range nodes {
			if e.name != "" && n.Name == e.name && !n.Pos.Unplaced() && (e.pos.Unplaced() || n.Pos == e.pos) {
				return i
			}
		}
		if !e.pos.Unplaced() {
			nodes = append(nodes, mapview.Node{Name: e.name, Kind: e.kind, Pos: e.pos, Key: e.key})
			return len(nodes) - 1
		}
		return -1
	}
	from, to := place(r.from), place(r.to)
	stops := rs.stops(r, from, to, nodes)
	// Each hop's link, by the first key byte at either end.
	firsts := make([]byte, len(stops))
	ok := make([]bool, len(stops))
	endKey := func(e mapEnd, idx int) ([32]byte, bool) {
		switch {
		case e.me:
			return m.ready.Self.PubKey, true
		case e.key != [32]byte{}:
			return e.key, true
		case idx >= 0 && nodes[idx].Key != [32]byte{}:
			return nodes[idx].Key, true
		}
		return [32]byte{}, false
	}
	if k, has := endKey(r.from, from); has {
		firsts[0], ok[0] = k[0], true
	}
	for i, h := range r.hops {
		firsts[i+1], ok[i+1] = h[0], true
	}
	if k, has := endKey(r.to, to); has {
		firsts[len(stops)-1], ok[len(stops)-1] = k[0], true
	}
	links := make([]mapview.Link, len(stops)-1)
	for i := range links {
		switch {
		case r.hopSNR != nil:
			if i < len(r.hopSNR) && !math.IsNaN(r.hopSNR[i]) {
				links[i] = mapview.Link{SNR: r.hopSNR[i], Known: true}
			}
		case i == len(links)-1 && r.measured:
			links[i] = mapview.Link{SNR: r.lastSNR, Known: true} // measured by our radio
		case ok[i] && ok[i+1]:
			if rs.links == nil {
				rs.links = m.linkTable()
			}
			if l, known := rs.links[[2]byte{min(firsts[i], firsts[i+1]), max(firsts[i], firsts[i+1])}]; known {
				links[i] = mapview.Link{SNR: l.snr, Known: true}
			}
		}
	}
	return mapview.Route{ID: r.id, What: r.what, From: r.from.label(), At: r.at, Flood: r.flood, Stops: stops, Links: links}
}

// stops are kept from frame to frame while the candidates and the route's ends stay where they were, since
// ranking a hop with many candidates is the costly part.
func (rs *resolver) stops(r mapRoute, from, to int, nodes []mapview.Node) []mapview.Stop {
	pos := func(i int) mapview.LatLon {
		if i < 0 {
			return mapview.LatLon{}
		}
		return nodes[i].Pos
	}
	key := fmt.Sprintf("%x|%d%v|%d%v|%x", r.hops, from, pos(from), to, pos(to), rs.index.Sum(nodes))
	mv := &rs.m.mapv
	if st, ok := mv.stops[key]; ok {
		return st
	}
	st := rs.index.Stops(from, r.hops, nodes, to)
	if len(mv.stops) >= 4*mapRoutes {
		clear(mv.stops)
	}
	mv.stops[key] = st
	return st
}

// mapHeard adds a packet the Packets screen shows to the map: its route, a comet along it, a ripple where a
// flood started, and a pulse at a node that advertised. Nothing else that's heard reaches the map.
func (m *Model) mapHeard(dec packets.Decoded, snr float32) {
	mv := &m.mapv
	r := m.packetRoute(dec, dec.Path, snr, m.now)
	if len(r.hops) > 0 {
		m.noteLink(r.hops[len(r.hops)-1][0], m.ready.Self.PubKey[0], float64(snr))
	}
	mv.routes = append([]mapRoute{r}, mv.routes...)
	if len(mv.routes) > mapRoutes {
		mv.routes = mv.routes[:mapRoutes]
	}
	if mv.layers.Selected >= 0 {
		mv.layers.Selected = min(mv.layers.Selected+1, len(mv.routes)-1) // keep the same route picked
	}
	hops := make([]string, len(r.hops))
	for i, h := range r.hops {
		hops[i] = fmt.Sprintf("%X", h)
	}
	via := "direct"
	if len(hops) > 0 {
		via = "via " + strings.Join(hops, "·")
	}
	from := r.from.label()
	if from == "" {
		from = "someone"
	}
	mv.event, mv.eventAt = fmt.Sprintf("[%s] ▸ %s  %s %s  SNR %+.1f", m.now.Format("15:04:05"), r.what, from, via, snr), m.now

	resolved, nodes := m.resolve(r, m.mapNodes())
	m.countTraffic(resolved, nodes)
	pts, links, _ := resolved.Placed(nodes)
	m.addAnim(mapview.Anim{Kind: mapview.Comet, Start: m.now, Path: pts, Links: links})
	if len(resolved.Stops) > 0 && len(resolved.Stops[0].Nodes) > 0 {
		at := nodes[resolved.Stops[0].Nodes[0]].Pos
		if dec.Type == "ADVERT" {
			m.addAnim(mapview.Anim{Kind: mapview.Pulse, Start: m.now, At: at})
		} else if dec.Flood {
			m.addAnim(mapview.Anim{Kind: mapview.Ripple, Start: m.now, At: at})
		}
	}
}

func nodeID(n mapview.Node) string {
	if n.Key != [32]byte{} {
		return radio.KeyHex(n.Key[:])
	}
	return fmt.Sprintf("%s@%.5f,%.5f", n.Name, n.Pos.Lat, n.Pos.Lon)
}

// countTraffic adds a heard route's hops to the traffic layer. A hop that could be one of several repeaters
// is shared between them by how likely each is, rather than all credited to a guess, unless one is clearly
// the likeliest. Only the hops nearest us are counted, and the quietest links are forgotten past trafficKeep.
func (m *Model) countTraffic(r mapview.Route, nodes []mapview.Node) {
	mv := &m.mapv
	m.pruneFlows(0)
	for i := max(len(r.Stops)-1-trafficHops, 0); i+1 < len(r.Stops); i++ {
		from, to := sureStop(r.Stops[i]), sureStop(r.Stops[i+1])
		unsure := len(from.Nodes) > 1 || len(to.Nodes) > 1
		for ka, a := range from.Nodes[:min(len(from.Nodes), mapview.MaxCandidates)] {
			for kb, b := range to.Nodes[:min(len(to.Nodes), mapview.MaxCandidates)] {
				na, nb := nodes[a], nodes[b]
				ia, ib := nodeID(na), nodeID(nb)
				if ib < ia {
					na, nb, ia, ib = nb, na, ib, ia
				}
				f, ok := mv.flows[ia+"|"+ib]
				if !ok {
					f = &flow{a: na.Pos, b: nb.Pos, aID: ia, bID: ib, aName: na.Name, bName: nb.Name, at: m.now}
					mv.flows[ia+"|"+ib] = f
				}
				f.add(m.now, from.Weights[ka]*to.Weights[kb], unsure)
			}
		}
	}
	m.pruneFlows(trafficKeep)
}

// pruneFlows forgets links long quiet, then the quietest until no more than keep are left; 0 keeps any number.
func (m *Model) pruneFlows(keep int) {
	mv, slowest := &m.mapv, len(trafficWindows)-1
	for k, f := range mv.flows {
		if v := f.now(m.now, slowest); !(v >= trafficFloor/10) {
			delete(mv.flows, k)
		}
	}
	if keep == 0 || len(mv.flows) <= keep {
		return
	}
	keys := slices.Collect(maps.Keys(mv.flows))
	sort.Slice(keys, func(i, j int) bool {
		a, b := mv.flows[keys[i]].now(m.now, slowest), mv.flows[keys[j]].now(m.now, slowest)
		if a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys[:len(keys)-keep] {
		delete(mv.flows, k)
	}
}

// sureStop narrows an ambiguous stop to its likeliest candidate when that one is likely enough.
func sureStop(st mapview.Stop) mapview.Stop {
	if len(st.Nodes) > 1 && st.Weights[0] >= trafficSure {
		return mapview.Stop{Hash: st.Hash, Nodes: st.Nodes[:1], Weights: []float64{1}}
	}
	return st
}

// drawnFlows are the links busy enough to draw, busiest first: no more than trafficLinks, each with at least
// trafficShare of the busiest one's traffic. It also names the busiest, and forgets links long quiet.
func (m *Model) drawnFlows() (drawn []*flow, busiest *flow) {
	mv := &m.mapv
	w := mv.window
	peak, top := 0.0, ""
	m.pruneFlows(0)
	for k, f := range mv.flows {
		if v := f.now(m.now, w); v > peak || v == peak && k < top {
			peak, top = v, k
		}
	}
	// Links that carried the same packets tie exactly, so without some stickiness the title flickers between them.
	if cur, ok := mv.flows[mv.busiest]; !ok || cur.now(m.now, w) < 0.9*peak {
		mv.busiest = top
	}
	if peak < trafficFloor {
		return nil, nil
	}
	for _, f := range mv.flows {
		if v := f.now(m.now, w); v >= trafficFloor && v >= trafficShare*peak {
			drawn = append(drawn, f)
		}
	}
	sort.Slice(drawn, func(i, j int) bool {
		a, b := drawn[i].now(m.now, w), drawn[j].now(m.now, w)
		if a != b {
			return a > b
		}
		return drawn[i].aID+drawn[i].bID < drawn[j].aID+drawn[j].bID
	})
	return drawn[:min(len(drawn), trafficLinks)], mv.flows[mv.busiest]
}

// mapFlows are the drawn links scaled so the busiest is 1. A square root lifts quiet links a little without
// making them look busy.
func (m *Model) mapFlows() ([]mapview.Flow, *flow) {
	w := m.mapv.window
	drawn, busiest := m.drawnFlows()
	if len(drawn) == 0 {
		return nil, busiest
	}
	peak := drawn[0].now(m.now, w)
	var out []mapview.Flow
	for _, f := range drawn {
		v := f.now(m.now, w)
		level := math.Sqrt(v / peak)
		out = append(out, mapview.Flow{A: f.a, B: f.b, Level: math.Round(level*20) / 20, // steps, so the map isn't redrawn for every tiny fade
			Uncertain: f.unsure[w] > f.count[w]/2})
	}
	// Busiest drawn last, on top. Ties are broken by position so the order, and the map's cache key with it,
	// doesn't shuffle from frame to frame as Go's map order does.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Level != b.Level {
			return a.Level < b.Level
		}
		return fmt.Sprint(a.A, a.B) < fmt.Sprint(b.A, b.B)
	})
	return out, busiest
}

func (m *Model) addAnim(a mapview.Anim) {
	if !a.Allowed(m.level) || a.Kind == mapview.Comet && len(a.Path) < 2 {
		return
	}
	mv := &m.mapv
	live := mv.anims[:0]
	for _, x := range mv.anims {
		if x.Running(m.now) {
			live = append(live, x)
		}
	}
	mv.anims = append(live, a)
	if len(mv.anims) > mapAnims {
		mv.anims = mv.anims[len(mv.anims)-mapAnims:]
	}
}

// mapEvent learns link signal from traces, and marks our own transmissions with a ring.
func (m *Model) mapEvent(e radio.Event) {
	switch e := e.(type) {
	case radio.TraceResult:
		prev := m.ready.Self.PubKey[0]
		for i, h := range e.Hashes {
			if i < len(e.SNRs) {
				m.noteLink(prev, h, float64(e.SNRs[i]))
			}
			prev = h
		}
	case radio.Transmit:
		self := mapview.LatLon{Lat: m.ready.Self.Lat, Lon: m.ready.Self.Lon}
		if !self.Unplaced() {
			m.addAnim(mapview.Anim{Kind: mapview.Ripple, Start: m.now, At: self, Outward: true})
		}
	}
}

func (m *Model) mapGliding() bool {
	mv := &m.mapv
	return m.level.Allows(motion.Wipe) && mv.drawCost <= motion.FrameDur && motion.Running(mv.movedAt, mapGlide, m.now)
}

// mapDraw draws the static map, keeping track of how long that takes on this machine.
func (m *Model) mapDraw(in mapview.Input, v mapview.View, w, h int, pal mapview.Palette) *mapview.Canvas {
	start := time.Now()
	c := mapview.Draw(in, v, w, h, pal)
	took, mv := time.Since(start), &m.mapv
	if mv.drawCost == 0 {
		mv.drawCost = took
	}
	mv.drawCost = (3*mv.drawCost + took) / 4
	return c
}

// mapCurrent is the view part way through a glide.
func (m *Model) mapCurrent() mapview.View {
	mv := &m.mapv
	if !m.mapGliding() {
		return mv.view
	}
	return mapview.Between(mv.from, mv.view, motion.EaseOut(motion.Progress(mv.movedAt, mapGlide, m.now)))
}

func (m *Model) mapGo(v mapview.View) {
	mv := &m.mapv
	mv.from, mv.view, mv.movedAt = m.mapCurrent(), v, m.now
}

func (m *Model) fitMesh() {
	mv := &m.mapv
	mv.auto = true
	if pts, _, ok := mapview.MeshArea(m.placedPoints()); ok {
		m.mapGo(mv.view.Fit(pts, mapMargin))
		return
	}
	v := mv.view
	v.Center, v.Zoom = mapview.LatLon{Lat: 30, Lon: 0}, mapview.MinZoom
	m.mapGo(v)
}

func (m *Model) mapKey(s string) (tea.Cmd, bool) {
	mv := &m.mapv
	if mv.path != nil {
		if cmd, handled := m.pathKey(s); handled {
			return cmd, true
		}
	}
	if mv.world == nil {
		return nil, false
	}
	if mv.showing && m.showKey(s) {
		return nil, true
	}
	v := mv.view
	if s != "m" && s != "]" && s != "[" && s != "n" && s != "r" && s != "t" && s != "f" && s != "F" && s != "d" && s != "esc" && s != "space" {
		mv.auto = false
	}
	switch s {
	case "left", "h":
		m.mapGo(v.Pan(-1.0/6, 0))
	case "right", "l":
		m.mapGo(v.Pan(1.0/6, 0))
	case "up", "k":
		m.mapGo(v.Pan(0, -1.0/6))
	case "down", "j":
		m.mapGo(v.Pan(0, 1.0/6))
	case "+", "=":
		m.mapGo(v.Zoomed(0.5))
	case "-", "_":
		m.mapGo(v.Zoomed(-0.5))
	case "m":
		m.fitMesh()
	case "]":
		if len(mv.routes) > 0 {
			mv.layers.Selected = min(mv.layers.Selected+1, len(mv.routes)-1)
			mv.cacheKey = ""
		}
	case "[":
		mv.layers.Selected = max(mv.layers.Selected-1, -1)
	case "n":
		mv.layers.Labels = !mv.layers.Labels
	case "r":
		mv.layers.Roads = !mv.layers.Roads
	case "t":
		switch {
		case !mv.layers.Traffic:
			mv.layers.Traffic, mv.window = true, 0
		case mv.window < len(trafficWindows)-1:
			mv.window++
		default:
			mv.layers.Traffic, mv.hub = false, ""
		}
	case "f", "F":
		m.stepHub(s == "f")
	case "space":
		mv.showing, mv.showSel = true, 0
	case "d":
		return m.fetchDetail(false), true
	case "esc":
		switch {
		case mv.fetching:
			mv.cancel()
		case mv.layers.Selected >= 0:
			mv.layers.Selected = -1
		case mv.hub != "":
			mv.hub = ""
		default:
			return nil, false
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) mapAnimating() bool {
	mv := &m.mapv
	if !m.visible(2) || mv.world == nil {
		return mv.loading && m.visible(2)
	}
	if mv.fetching || m.mapGliding() || m.pathPlaying() {
		return true
	}
	if motion.Running(mv.eventAt, motion.TypeDur(mv.event, logCPS), m.now) && m.level.Allows(motion.Typing) {
		return true
	}
	for _, a := range mv.anims {
		if a.Allowed(m.level) && a.Running(m.now) {
			return true
		}
	}
	return false
}

// mapBeaconOn reports the beacon pulsing around us, which runs whenever the map shows at full motion.
func (m *Model) mapBeaconOn() bool {
	return m.beaconOn && m.visible(2) && m.mapv.world != nil && m.mapv.path == nil && m.level == motion.Full && m.phase == phaseMain &&
		!(mapview.LatLon{Lat: m.ready.Self.Lat, Lon: m.ready.Self.Lon}).Unplaced()
}

// hubNode is a node with traffic drawn, for picking out with f.
type hubNode struct {
	id, name string
	pos      mapview.LatLon
	links    int
	total    float64
}

// hubs are the nodes at either end of the drawn links, busiest first, and us last: every route ends with
// us, so we'd always lead.
func (m *Model) hubs() []hubNode {
	me := nodeID(m.mapNodes()[0])
	drawn, _ := m.drawnFlows()
	by := map[string]*hubNode{}
	var out []*hubNode
	for _, f := range drawn {
		v := f.now(m.now, m.mapv.window)
		for _, end := range []hubNode{{id: f.aID, name: f.aName, pos: f.a}, {id: f.bID, name: f.bName, pos: f.b}} {
			h, ok := by[end.id]
			if !ok {
				h = &end
				by[end.id] = h
				out = append(out, h)
			}
			h.links++
			h.total += v
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].id == me) != (out[j].id == me) {
			return out[j].id == me
		}
		if out[i].total != out[j].total {
			return out[i].total > out[j].total
		}
		return out[i].name < out[j].name
	})
	hubs := make([]hubNode, len(out))
	for i, h := range out {
		hubs[i] = *h
	}
	return hubs
}

// stepHub picks out the next node's traffic, busiest first, or the previous; past either end it shows all again.
func (m *Model) stepHub(next bool) {
	mv := &m.mapv
	mv.layers.Traffic = true
	hubs := m.hubs()
	i := slices.IndexFunc(hubs, func(h hubNode) bool { return h.id == mv.hub })
	switch {
	case next:
		i++
	case i < 0:
		i = len(hubs) - 1
	default:
		i--
	}
	mv.hub = ""
	if i >= 0 && i < len(hubs) {
		mv.hub = hubs[i].id
	}
}

// hub is the node picked out with f, while it still has traffic drawn.
func (m *Model) hub() (hubNode, bool) {
	if !m.mapv.layers.Traffic || m.mapv.hub == "" {
		return hubNode{}, false
	}
	for _, h := range m.hubs() {
		if h.id == m.mapv.hub {
			return h, true
		}
	}
	return hubNode{}, false
}

func (m *Model) mapTitle() string {
	mv := &m.mapv
	if mv.path != nil {
		return m.pathTitle()
	}
	placed := len(m.placedPoints())
	t := fmt.Sprintf("MAP · %s PLACED · %s", strings.ToUpper(plural(placed, "node")), strings.ToUpper(plural(len(mv.routes), "route")))
	if mv.layers.Selected >= 0 && mv.layers.Selected < len(mv.routes) {
		t += fmt.Sprintf(" · ROUTE %d OF %d", mv.layers.Selected+1, len(mv.routes))
	}
	if h, ok := m.hub(); ok {
		t += fmt.Sprintf(" · %s · %s", strings.ToUpper(h.name), strings.ToUpper(plural(h.links, "link")))
	} else if _, busiest := m.drawnFlows(); busiest != nil && mv.layers.Traffic {
		t += " · " + trafficWindows[mv.window].busiest + " " + strings.ToUpper(busiest.aName+"–"+busiest.bName)
	}
	if m.mapFiltered() {
		t += " · SOME HIDDEN"
	}
	if mv.fetching {
		t += " · DOWNLOADING"
	}
	return t
}

func (m *Model) renderMap(w, h int) []string {
	th, mv := m.th, &m.mapv
	if mv.world == nil {
		return []string{m.waiting("loading the world map…")}
	}
	mapH := max(h-2, 3)
	if mv.path != nil {
		return m.renderPath(w, mapH)
	}
	if !mv.fitted {
		mv.view.W, mv.view.H = w*2, mapH*4
		mv.fitted = true
		m.fitMesh()
		mv.from, mv.movedAt = mv.view, time.Time{}
	}
	if resized := mv.view.W != w*2 || mv.view.H != mapH*4; resized {
		mv.view.W, mv.view.H = w*2, mapH*4
		if mv.auto {
			m.fitMesh()
			mv.from, mv.movedAt = mv.view, time.Time{}
		}
	}
	v := m.mapCurrent()
	v.W, v.H = w*2, mapH*4

	rs := m.resolver(m.mapNodes())
	routes := make([]mapview.Route, 0, len(mv.routes))
	for _, r := range mv.routes {
		routes = append(routes, rs.resolve(r))
	}
	nodes := rs.nodes
	flows, _ := m.mapFlows()
	layers := mv.layers
	if h, ok := m.hub(); ok {
		layers.Hub = h.pos
	}
	pal := mapview.PaletteFor(th)
	// The theme's colours, not its name: mid-crossfade the blend already carries the new theme's name.
	key := fmt.Sprintf("%.6f|%.6f|%.4f|%d|%d|%v|%v|%p|%d|%s", v.Center.Lat, v.Center.Lon, v.Zoom, w, mapH, th, layers,
		mv.detail, len(nodes), routeKey(routes, m.now)) + fmt.Sprint(flows)
	if key != mv.cacheKey || mv.cache == nil {
		mv.cache = m.mapDraw(mapview.Input{World: mv.world, Detail: mv.detail, Nodes: nodes, Routes: routes, Flows: flows,
			Layers: layers, Now: m.now}, v, w, mapH, pal)
		credit := "Natural Earth"
		if mv.detail != nil && v.Zoom >= 7 && mv.detail.Covers(v.Center) {
			credit = mapview.DetailCredit
		}
		mapview.Credit(mv.cache, credit, pal)
		mv.rows, mv.cacheKey = mv.cache.Render(!th.Mono), key
	}
	c := mv.cache.Clone()
	var self mapview.LatLon // unplaced draws no beacon
	if m.beaconOn {
		self = mapview.LatLon{Lat: m.ready.Self.Lat, Lon: m.ready.Self.Lon}
	}
	mapview.DrawAnims(c, v, mv.anims, self, m.level, m.now, pal)
	out := append([]string(nil), mv.rows...)
	for y := range out {
		if c.Dirty(y) {
			out[y] = c.RenderRow(y, !th.Mono)
		}
	}

	if mv.showing {
		m.overlayShow(out, w)
	}
	hint := "←↑↓→ hjkl move · + - zoom · m whole mesh · space show · [ ] routes · t traffic · f F focus · d download streets"
	if mv.hub != "" || mv.layers.Selected >= 0 {
		hint = "esc show all · " + hint
	}
	out = append(out, clip(m.mapInfo(routes, nodes), w), clip(th.Fg(th.Dim, false).Render(hint), w))
	return out
}

// downloadSize says what a download will cost before it starts. Tiles vary a lot with how built up and watery
// an area is (the test areas ran from 20 to 170 KB each), so it gives an upper estimate.
func downloadSize(tiles int) string {
	return fmt.Sprintf("%s, up to about %.1f MB", plural(tiles, "map tile"), max(float64(tiles)*0.17, 0.1))
}

// routeKey changes when the routes drawn do, including each step of their fading.
func routeKey(routes []mapview.Route, now time.Time) string {
	var b strings.Builder
	for _, r := range routes {
		fmt.Fprintf(&b, "%s@%.1f", r.ID, mapview.RouteFade(r.At, now))
		for _, l := range r.Links {
			fmt.Fprintf(&b, ",%v%.0f", l.Known, l.SNR)
		}
		b.WriteByte(';')
	}
	return b.String()
}

// routeLine names each stop of a route and the signal on each hop, where it's known. to names the last stop
// when it isn't placed.
func (m *Model) routeLine(r mapview.Route, nodes []mapview.Node, to string) string {
	th := m.th
	var parts []string
	for i, st := range r.Stops {
		name := "?"
		switch {
		case len(st.Nodes) == 1:
			name = nodes[st.Nodes[0]].Name
		case len(st.Nodes) > 1:
			var alts []string
			for k, n := range st.Nodes {
				alts = append(alts, fmt.Sprintf("%s %.0f%%", nodes[n].Name, st.Weights[k]*100))
			}
			name = st.Hash + " (" + strings.Join(alts, " or ") + ")"
		case st.Hash != "":
			name = st.Hash + " (no location)"
		case i == 0 && r.From != "":
			name = r.From
		case i == len(r.Stops)-1:
			name = to
		}
		parts = append(parts, name)
		if i < len(r.Links) {
			if l := r.Links[i]; l.Known {
				parts = append(parts, th.Fg(th.SNR(l.SNR), true).Render(fmt.Sprintf("─%+.1f→", l.SNR)))
			} else {
				parts = append(parts, th.Fg(th.Dim, false).Render("──→"))
			}
		}
	}
	head := th.Fg(th.Magenta, true).Render("▌") + th.Fg(th.Text, true).Render(fmt.Sprintf("%s ", strings.ToUpper(strings.TrimSpace(r.What)))) +
		th.Fg(th.Dim, false).Render(fmt.Sprintf("%s · %.1f km · %s  ", plural(len(r.Stops)-2, "hop"), r.Km(nodes), ago(m.now.Sub(r.At))))
	return head + th.Fg(th.Text, false).Render(strings.Join(parts, " "))
}

// mapInfo is the line under the map: the picked route, the download, an offer of street detail, or the
// latest packet.
func (m *Model) mapInfo(routes []mapview.Route, nodes []mapview.Node) string {
	th, mv := m.th, &m.mapv
	if sel := mv.layers.Selected; sel >= 0 && sel < len(routes) {
		return m.routeLine(routes[sel], nodes, "me")
	}
	switch {
	case mv.fetching:
		return th.Fg(th.Yellow, false).Render(fmt.Sprintf("%s downloading street detail from OpenFreeMap: %d of %d tiles · esc stops",
			string(motion.RampRune(m.now)), mv.fetchDone.Load(), mv.fetchTotal))
	case mv.note != "":
		c := th.Green
		if mv.noteBad {
			c = th.Red
		}
		return th.Fg(c, false).Render("▸ " + mv.note)
	}
	if pts, box, ok := mapview.MeshArea(m.placedPoints()); ok {
		missing := mv.detail.Missing(box)
		switch {
		case mv.detail == nil:
			return th.Fg(th.Cyan, false).Render(fmt.Sprintf("▸ press d to download street detail from OpenFreeMap: %s",
				downloadSize(len(missing))))
		case len(missing) > 0:
			out := 0
			for _, p := range pts {
				if !mv.detail.Covers(p) {
					out++
				}
			}
			return th.Fg(th.Cyan, false).Render(fmt.Sprintf("▸ %s outside your street detail · press d to download %d more from OpenFreeMap: %s",
				plural(out, "node")+map[bool]string{true: " is", false: " are"}[out == 1], len(missing), downloadSize(len(missing))))
		}
	} else {
		return th.Fg(th.Dim, false).Render("No node has a location yet. Nodes appear as their adverts arrive with one.")
	}
	if mv.event == "" {
		return th.Fg(th.Dim, false).Render("Adverts and messages on open channels travel the map as they're heard.")
	}
	text := mv.event
	cursor := ""
	if m.level.Allows(motion.Typing) {
		var done bool
		if text, done = motion.Typewriter(mv.event, mv.eventAt, logCPS, m.now); !done {
			cursor = th.Fg(th.Text, true).Render("█")
		}
	}
	return th.Fg(th.Text, false).Render(text) + cursor
}

func mapCommand() command {
	return command{name: "map", args: "[update|remove]", help: "go to the map; update downloads its street detail again, remove deletes it",
		complete: func(*Model) []string { return []string{"update", "remove"} },
		run: func(m *Model, a []string) error {
			if len(a) == 0 {
				m.setTab(2)
				return nil
			}
			switch a[0] {
			case "update":
				if m.mapv.detail == nil {
					return fmt.Errorf("there's no street detail yet; d on the map downloads it")
				}
				m.setTab(2)
				m.queued = m.fetchDetail(true)
			case "remove":
				if err := m.removeDetail(); err != nil {
					return err
				}
				m.addLog(m.now, "MAP  street detail deleted", radio.ToneInfo)
			default:
				return fmt.Errorf("map takes update or remove")
			}
			return nil
		}}
}
