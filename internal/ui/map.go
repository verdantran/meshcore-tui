package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"meshcore-tui/internal/config"
	"meshcore-tui/internal/mapview"
	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/packets"
	"meshcore-tui/internal/radio"
)

const (
	mapRoutes  = 30                     // routes kept to pick through
	mapAnims   = 40                     // animations running at once, at most
	mapGlide   = 320 * time.Millisecond // pans and zooms ease over this
	mapAgent   = "meshcore-tui (+https://github.com/verdantran/meshcore-tui)"
	mapMargin  = 0.12
	linkMaxAge = 24 * time.Hour // older trace readings no longer colour a link
)

// mapRoute is a packet's journey as heard. It's resolved against the nodes each time it's drawn, so a
// repeater added later, or a trace run later, still places and colours it.
type mapRoute struct {
	id       string
	what     string
	from     string
	fromPos  mapview.LatLon
	fromKind mapview.Kind
	hops     [][]byte
	lastSNR  float64
	at       time.Time
	flood    bool
}

type linkReading struct {
	snr float64
	at  time.Time
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

	routes  []mapRoute // newest first
	anims   []mapview.Anim
	links   map[[2]byte]linkReading // signal between two nodes, by their first key bytes
	event   string                  // the latest packet, typed out under the map
	eventAt time.Time

	fetching   bool
	fetchDone  *atomic.Int32
	fetchTotal int
	cancel     context.CancelFunc
	note       string
	noteBad    bool

	cacheKey string
	cache    *mapview.Canvas
}

func newMapModel() mapModel {
	return mapModel{layers: mapview.Layers{Labels: true, Roads: true, Selected: -1}, links: map[[2]byte]linkReading{},
		fetchDone: &atomic.Int32{}}
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
	return mapview.Fetcher{Index: idx, Agent: mapAgent, Dir: filepath.Join(m.mapDir(), "tiles")}
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
func (m *Model) mapNodes() []mapview.Node {
	self := m.ready.Self
	nodes := []mapview.Node{{Name: "me", Kind: mapview.KindSelf, Pos: mapview.LatLon{Lat: self.Lat, Lon: self.Lon}, Key: self.PubKey}}
	seen := map[[32]byte]bool{self.PubKey: true}
	if m.chat != nil {
		for _, c := range m.chat.Contacts() {
			if !seen[c.Key] {
				seen[c.Key] = true
				nodes = append(nodes, mapview.Node{Name: c.Name, Kind: kindOf(c.Type), Pos: mapview.LatLon{Lat: c.Lat, Lon: c.Lon}, Key: c.Key})
			}
		}
	}
	for _, h := range m.contacts.heard {
		if !seen[h.c.Key] {
			seen[h.c.Key] = true
			nodes = append(nodes, mapview.Node{Name: h.c.Name, Kind: kindOf(h.c.Type), Pos: mapview.LatLon{Lat: h.c.Lat, Lon: h.c.Lon}, Key: h.c.Key})
		}
	}
	return nodes
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
	pair := [2]byte{min(a, b), max(a, b)}
	best, ok := linkReading{}, false
	if r, has := m.mapv.links[pair]; has && m.now.Sub(r.at) < linkMaxAge {
		best, ok = r, true
	}
	for _, c := range m.allNodes() {
		sess, has := m.admin.Find(c.Key)
		if !has || sess.NeighbourAt.IsZero() {
			continue
		}
		for _, n := range sess.Neighbours {
			if len(n.Prefix) > 0 && [2]byte{min(c.Key[0], n.Prefix[0]), max(c.Key[0], n.Prefix[0])} == pair && sess.NeighbourAt.After(best.at) {
				best, ok = linkReading{float64(n.SNR), sess.NeighbourAt}, true
			}
		}
	}
	return best.snr, ok
}

func (m *Model) noteLink(a, b byte, snr float64) {
	m.mapv.links[[2]byte{min(a, b), max(a, b)}] = linkReading{snr, m.now}
}

// resolve places a route against the nodes as they are now, adding its sender if that isn't a contact.
func (m *Model) resolve(r mapRoute, nodes []mapview.Node) (mapview.Route, []mapview.Node) {
	from := -1
	for i, n := range nodes {
		if n.Name == r.from && !n.Pos.Unplaced() && (r.fromPos.Unplaced() || n.Pos == r.fromPos) {
			from = i
			break
		}
	}
	if from < 0 && !r.fromPos.Unplaced() {
		nodes = append(nodes, mapview.Node{Name: r.from, Kind: r.fromKind, Pos: r.fromPos})
		from = len(nodes) - 1
	}
	self := 0
	if nodes[0].Pos.Unplaced() {
		self = -1
	}
	stops := mapview.Stops(from, r.hops, nodes, self)
	// Each hop's link, by the first key byte at either end: the sender's key, each hash, then ours.
	firsts := make([]byte, len(stops))
	ok := make([]bool, len(stops))
	if from >= 0 && nodes[from].Key != [32]byte{} {
		firsts[0], ok[0] = nodes[from].Key[0], true
	}
	for i, h := range r.hops {
		firsts[i+1], ok[i+1] = h[0], true
	}
	firsts[len(stops)-1], ok[len(stops)-1] = m.ready.Self.PubKey[0], true
	links := make([]mapview.Link, len(stops)-1)
	for i := range links {
		if i == len(links)-1 {
			links[i] = mapview.Link{SNR: r.lastSNR, Known: true} // measured by our radio
			continue
		}
		if ok[i] && ok[i+1] {
			if snr, known := m.linkSNR(firsts[i], firsts[i+1]); known {
				links[i] = mapview.Link{SNR: snr, Known: true}
			}
		}
	}
	return mapview.Route{ID: r.id, What: r.what, From: r.from, At: r.at, Flood: r.flood, Stops: stops, Links: links}, nodes
}

// mapHeard adds a packet the Packets screen shows to the map: its route, a comet along it, a ripple where a
// flood started, and a pulse at a node that advertised. Nothing else that's heard reaches the map.
func (m *Model) mapHeard(dec packets.Decoded, snr float32) {
	mv := &m.mapv
	r := mapRoute{id: fmt.Sprintf("%X/%X", dec.Hash, dec.Path), what: dec.Type, lastSNR: float64(snr), at: m.now, flood: dec.Flood}
	switch dec.Type {
	case "ADVERT":
		r.from, r.fromPos, r.fromKind = dec.Name, mapview.LatLon{Lat: dec.Lat, Lon: dec.Lon}, mapview.KindOther
		for _, n := range m.mapNodes() {
			if n.Name == dec.Name {
				r.fromKind = n.Kind
			}
		}
	default:
		r.what, r.from = dec.Type+" "+dec.Channel, dec.Sender
	}
	if dec.Flood && dec.HashSize > 0 {
		for i := 0; i+dec.HashSize <= len(dec.Path); i += dec.HashSize {
			r.hops = append(r.hops, dec.Path[i:i+dec.HashSize])
		}
	}
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
	from := r.from
	if from == "" {
		from = "someone"
	}
	mv.event, mv.eventAt = fmt.Sprintf("[%s] ▸ %s  %s %s  SNR %+.1f", m.now.Format("15:04:05"), r.what, from, via, snr), m.now

	resolved, nodes := m.resolve(r, m.mapNodes())
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

// mapCurrent is the view part way through a glide.
func (m *Model) mapCurrent() mapview.View {
	mv := &m.mapv
	if !m.level.Allows(motion.Wipe) || !motion.Running(mv.movedAt, mapGlide, m.now) {
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
	if mv.world == nil {
		return nil, false
	}
	v := mv.view
	if s != "m" && s != "]" && s != "[" && s != "L" && s != "r" && s != "d" && s != "esc" {
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
	case "L":
		mv.layers.Labels = !mv.layers.Labels
	case "r":
		mv.layers.Roads = !mv.layers.Roads
	case "d":
		return m.fetchDetail(false), true
	case "esc":
		switch {
		case mv.fetching:
			mv.cancel()
		case mv.layers.Selected >= 0:
			mv.layers.Selected = -1
		default:
			return nil, false
		}
	default:
		return nil, false
	}
	return nil, true
}

// showRoute picks the route a packet took and opens the map on it, from the Packets screen.
func (m *Model) showRoute(dec packets.Decoded) bool {
	prefix := fmt.Sprintf("%X/", dec.Hash)
	for i, r := range m.mapv.routes {
		if strings.HasPrefix(r.id, prefix) {
			m.mapv.layers.Selected = i
			m.setTab(2)
			return true
		}
	}
	return false
}

func (m *Model) mapAnimating() bool {
	mv := &m.mapv
	if !m.visible(2) || mv.world == nil {
		return mv.loading && m.visible(2)
	}
	if mv.fetching || motion.Running(mv.movedAt, mapGlide, m.now) && m.level.Allows(motion.Wipe) {
		return true
	}
	if m.level == motion.Full && !(mapview.LatLon{Lat: m.ready.Self.Lat, Lon: m.ready.Self.Lon}).Unplaced() {
		return true // the radar sweep
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

func (m *Model) mapTitle() string {
	mv := &m.mapv
	placed := len(m.placedPoints())
	t := fmt.Sprintf("MAP · %s PLACED · %s", strings.ToUpper(plural(placed, "node")), strings.ToUpper(plural(len(mv.routes), "route")))
	if mv.layers.Selected >= 0 && mv.layers.Selected < len(mv.routes) {
		t += fmt.Sprintf(" · ROUTE %d OF %d", mv.layers.Selected+1, len(mv.routes))
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

	nodes := m.mapNodes()
	routes := make([]mapview.Route, 0, len(mv.routes))
	for _, r := range mv.routes {
		var rr mapview.Route
		rr, nodes = m.resolve(r, nodes)
		routes = append(routes, rr)
	}
	pal := mapview.PaletteFor(th)
	// The theme's colours, not its name: mid-crossfade the blend already carries the new theme's name.
	key := fmt.Sprintf("%.6f|%.6f|%.4f|%d|%d|%v|%v|%p|%d|%s", v.Center.Lat, v.Center.Lon, v.Zoom, w, mapH, th, mv.layers,
		mv.detail, len(nodes), routeKey(routes))
	if key != mv.cacheKey || mv.cache == nil {
		mv.cache = mapview.Draw(mapview.Input{World: mv.world, Detail: mv.detail, Nodes: nodes, Routes: routes, Layers: mv.layers}, v, w, mapH, pal)
		mv.cacheKey = key
	}
	c := mv.cache.Clone()
	self := mapview.LatLon{Lat: m.ready.Self.Lat, Lon: m.ready.Self.Lon}
	mapview.DrawAnims(c, v, mv.anims, self, m.level, m.now, pal)
	credit := "Natural Earth"
	if mv.detail != nil && v.Zoom >= 7 && mv.detail.Covers(v.Center) {
		credit = mapview.DetailCredit
	}
	mapview.Credit(c, credit, pal)
	out := c.Render(!th.Mono)

	out = append(out, clip(m.mapInfo(routes, nodes), w), clip(th.Fg(th.Dim, false).Render(
		"←↑↓→ hjkl move · + - zoom · m whole mesh · [ ] routes · d download street detail · L labels · r roads"), w))
	return out
}

// downloadSize says what a download will cost before it starts. Tiles vary a lot with how built up and watery
// an area is (the test areas ran from 20 to 170 KB each), so it gives an upper estimate.
func downloadSize(tiles int) string {
	return fmt.Sprintf("%s, up to about %.1f MB", plural(tiles, "map tile"), max(float64(tiles)*0.17, 0.1))
}

func routeKey(routes []mapview.Route) string {
	var b strings.Builder
	for _, r := range routes {
		b.WriteString(r.ID)
		for _, l := range r.Links {
			fmt.Fprintf(&b, ",%v%.0f", l.Known, l.SNR)
		}
		b.WriteByte(';')
	}
	return b.String()
}

// mapInfo is the line under the map: the picked route, the download, an offer of street detail, or the
// latest packet.
func (m *Model) mapInfo(routes []mapview.Route, nodes []mapview.Node) string {
	th, mv := m.th, &m.mapv
	if sel := mv.layers.Selected; sel >= 0 && sel < len(routes) {
		r := routes[sel]
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
				name = st.Hash
			case i == 0 && r.From != "":
				name = r.From
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
			th.Fg(th.Dim, false).Render(fmt.Sprintf("%d hops · %.1f km · %s  ", len(r.Stops)-2, r.Km(nodes), ago(m.now.Sub(r.At))))
		return head + th.Fg(th.Text, false).Render(strings.Join(parts, " "))
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
