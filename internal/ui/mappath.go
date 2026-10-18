package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/mapview"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
)

// pathView is the map showing a single path, opened with M from wherever a route is mentioned: a packet,
// a message, a contact's route or a trace. It shows only that path's nodes, named, with each hop's signal.
type pathView struct {
	title  string
	routes []mapRoute // the alternatives [ ] steps through, such as each copy of a packet heard
	to     string     // the far end's name, shown when it isn't placed
	sel    int
	back   int // the screen esc goes back to
	saved  mapview.View
	auto   bool
	fit    bool
	play   bool // replay the route once the view has settled
	playAt time.Time
}

// showPath opens the map on one path, or several alternatives, remembering where to go back to.
func (m *Model) showPath(title, to string, routes []mapRoute) {
	if len(routes) == 0 {
		return
	}
	mv := &m.mapv
	if mv.path == nil {
		mv.path = &pathView{saved: mv.view, auto: mv.auto, back: m.tab}
	}
	if m.tab != 2 {
		mv.path.back = m.tab
	}
	mv.path.title, mv.path.to, mv.path.routes, mv.path.sel, mv.path.fit, mv.path.play = title, to, routes, 0, true, true
	mv.cacheKey = ""
	m.setTab(2)
}

// leavePath goes back to the screen the path was opened from.
func (m *Model) leavePath() {
	back := m.mapv.path.back
	m.closePath()
	m.setTab(back)
}

// closePath puts the full map back as it was before the path was shown.
func (m *Model) closePath() {
	mv := &m.mapv
	if p := mv.path; p != nil {
		mv.path, mv.cacheKey = nil, ""
		mv.view, mv.from, mv.auto, mv.movedAt = p.saved, p.saved, p.auto, time.Time{}
	}
}

func (m *Model) pathKey(s string) (tea.Cmd, bool) {
	p := m.mapv.path
	switch s {
	case "esc":
		m.leavePath()
	case "]":
		p.sel, p.play = min(p.sel+1, len(p.routes)-1), true
	case "[":
		p.sel, p.play = max(p.sel-1, 0), true
	case "space":
		p.play = true
	case "m":
		p.fit = true
	case "t", "f", "F", "d":
		return nil, true // the traffic layer and the download belong to the full map
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) pathTitle() string {
	p := m.mapv.path
	t := "PATH · " + strings.ToUpper(p.title)
	if len(p.routes) > 1 {
		t += fmt.Sprintf(" · %d OF %d", p.sel+1, len(p.routes))
	}
	return t
}

func (m *Model) renderPath(w, mapH int) []string {
	th, mv, p := m.th, &m.mapv, m.mapv.path
	nodes := m.mapNodes()
	r, nodes := m.resolve(p.routes[p.sel], nodes)
	mv.view.W, mv.view.H = w*2, mapH*4
	if p.fit {
		p.fit = false
		pts, _, _ := r.Placed(nodes)
		if len(pts) > 0 {
			v := mv.view.Fit(pts, 0.25)
			v.Zoom = min(v.Zoom, 11) // a one-hop path shouldn't zoom in to street level
			if mv.fitted {
				m.mapGo(v)
			} else {
				mv.view, mv.from, mv.movedAt = v, v, time.Time{} // no view yet to glide from
			}
		}
		mv.auto = false
	}
	if p.play {
		p.play, p.playAt = false, m.now
		if m.mapGliding() {
			p.playAt = mv.movedAt.Add(mapGlide)
		}
	}
	v := m.mapCurrent()
	v.W, v.H = w*2, mapH*4
	drawn := m.traceSoFar(r)
	layers := mapview.Layers{Labels: true, Roads: mv.layers.Roads, Routes: true, Selected: 0, PathOnly: true}
	pal := mapview.PaletteFor(th)
	key := fmt.Sprintf("path|%.6f|%.6f|%.4f|%d|%d|%v|%v|%p|%d|%s", v.Center.Lat, v.Center.Lon, v.Zoom, w, mapH, th, layers,
		mv.detail, len(nodes), routeKey([]mapview.Route{drawn}, m.now)) + fmt.Sprint(drawn.Links)
	if key != mv.cacheKey || mv.cache == nil {
		mv.cache = m.mapDraw(mapview.Input{World: mv.world, Detail: mv.detail, Nodes: nodes, Routes: []mapview.Route{drawn},
			Layers: layers, Now: m.now}, v, w, mapH, pal)
		credit := "Natural Earth"
		if mv.detail != nil && v.Zoom >= 7 && mv.detail.Covers(v.Center) {
			credit = mapview.DetailCredit
		}
		mapview.Credit(mv.cache, credit, pal)
		mv.rows, mv.cacheKey = mv.cache.Render(!th.Mono), key
	}
	out := append([]string(nil), mv.rows...)
	if anims := m.pathAnims(r, nodes); len(anims) > 0 {
		c := mv.cache.Clone()
		mapview.DrawAnims(c, v, anims, mapview.LatLon{}, m.level, m.now, pal) // no beacon on a path
		for y := range out {
			if c.Dirty(y) {
				out[y] = c.RenderRow(y, !th.Mono)
			}
		}
	}
	if pts, _, _ := r.Placed(nodes); len(pts) < 2 {
		note := " None of this path's nodes has a location, so it can't be drawn; its hops are listed below. "
		if len(pts) == 1 {
			note = " Only one of this path's nodes has a location; the rest are listed below. "
		}
		out[0] = th.Block(th.Bg, th.Yellow).Render(clip(note, w))
	}
	back := strings.ToLower(tabNames[p.back])
	return append(out, clip(m.routeLine(r, nodes, p.to), w), clip(th.Fg(th.Dim, false).Render(
		"[ ] other routes · space replay · ←↑↓→ hjkl move · + - zoom · m fit the path · n names · r roads · esc back to "+back), w))
}

// pathAnims replays the path as the full map shows a packet arriving: a comet along it, and a ripple where a
// flood started.
func (m *Model) pathAnims(r mapview.Route, nodes []mapview.Node) []mapview.Anim {
	p := m.mapv.path
	pts, links, _ := r.Placed(nodes)
	if p.playAt.IsZero() || len(pts) < 2 {
		return nil
	}
	if m.tracePlays() {
		// A trace draws hop by hop instead: a comet flies each hop in turn, and the hop is drawn once it lands.
		var anims []mapview.Anim
		for i := 0; i+1 < len(pts); i++ {
			anims = append(anims, mapview.Anim{Kind: mapview.Comet, Start: p.playAt.Add(time.Duration(i) * mapview.HopDur),
				Path: pts[i : i+2], Links: links[i : i+1]})
		}
		return anims
	}
	anims := []mapview.Anim{{Kind: mapview.Comet, Start: p.playAt, Path: pts, Links: links}}
	if p.routes[p.sel].flood {
		anims = append(anims, mapview.Anim{Kind: mapview.Ripple, Start: p.playAt, At: pts[0]})
	}
	return anims
}

// pathPlaying reports the replay still to run or running, so frames keep coming through it.
func (m *Model) pathPlaying() bool {
	p := m.mapv.path
	if p == nil || p.playAt.IsZero() {
		return false
	}
	nodes := m.mapNodes()
	r, nodes := m.resolve(p.routes[p.sel], nodes)
	for _, a := range m.pathAnims(r, nodes) {
		if m.now.Before(a.Start.Add(a.Dur())) && a.Allowed(m.level) {
			return true
		}
	}
	return m.tracePlays() && m.now.Before(p.playAt.Add(time.Duration(len(r.Stops))*mapview.HopDur+countDur))
}

// tracePlays reports a trace on the path view, which draws itself in rather than replaying with a comet.
func (m *Model) tracePlays() bool {
	p := m.mapv.path
	return p != nil && !p.playAt.IsZero() && p.routes[p.sel].what == "trace" && m.level.Allows(motion.Typing)
}

// traceSoFar is as much of a trace as has landed: the hops a comet has finished flying, each SNR counting
// up from when its hop landed.
func (m *Model) traceSoFar(r mapview.Route) mapview.Route {
	p := m.mapv.path
	if !m.tracePlays() || len(r.Stops) < 2 {
		return r
	}
	landed := min(int(m.now.Sub(p.playAt)/mapview.HopDur), len(r.Stops)-1)
	if landed < 0 {
		landed = 0
	}
	out := r
	out.Stops = r.Stops[:landed+1]
	out.Links = append([]mapview.Link(nil), r.Links[:landed]...)
	for i := range out.Links {
		at := p.playAt.Add(time.Duration(i+1) * mapview.HopDur)
		out.Links[i].SNR = motion.Count(0, r.Links[i].SNR, at, countDur, m.now)
	}
	return out
}

// showPacketPath opens the map on a packet's path, with each copy heard as an alternative.
func (m *Model) showPacketPath(row *packetRow) {
	var routes []mapRoute
	for i := len(row.copies) - 1; i >= 0; i-- {
		c := row.copies[i]
		routes = append(routes, m.packetRoute(row.dec, c.path, c.snr, c.at))
	}
	title := routes[0].what
	if from := routes[0].from.label(); from != "" {
		title += " from " + from
	}
	m.showPath(title, "me", routes)
}

// showMessagePath opens the map on the way a message came: the path saved with it from its packet, with any
// other copies of a channel message heard as alternatives. Without one, a direct conversation shows the
// contact's stored route, and otherwise it says what the radio reported.
func (m *Model) showMessagePath(msg store.Message) {
	if m.chat == nil {
		return
	}
	ct, isContact := m.chat.Contact(msg.Convo)
	if msg.Out {
		if isContact {
			m.showContactPath(ct)
			return
		}
		m.addLog(m.now, "MAP  your own channel messages flood out from you; their way back shows as repeats", radio.ToneInfo)
		return
	}
	from := mapEnd{name: msg.Sender}
	if isContact {
		from = mapEnd{key: ct.Key, name: ct.Name}
	}
	var routes []mapRoute
	if len(msg.Path) > 0 {
		routes = append(routes, mapRoute{id: fmt.Sprint(msg.ID), what: "message", from: from, to: mapEnd{me: true},
			hops: splitPath(msg.Path, msg.PathHash, true), lastSNR: float64(msg.SNR), measured: msg.HasSNR, at: msg.RecvAt, flood: true})
	}
	if cur, ok := m.selectedConvo(); ok && cur.Kind == chat.KindChannel {
		for _, row := range m.inspector.rows {
			d := row.dec
			if !d.Decrypted || d.Type != "GRP_TXT" || d.Channel != cur.Title || d.Sender != msg.Sender || d.Text != msg.Text {
				continue
			}
			for i := len(row.copies) - 1; i >= 0; i-- {
				cp := row.copies[i]
				if string(cp.path) != string(msg.Path) {
					routes = append(routes, m.packetRoute(d, cp.path, cp.snr, cp.at))
				}
			}
		}
	}
	if len(routes) > 0 {
		m.showPath("message from "+msg.Sender, "me", routes)
		return
	}
	if isContact {
		m.showContactPath(ct)
		return
	}
	how := fmt.Sprintf("the radio reported only that it took %s, and its packet wasn't heard", plural(msg.Hops, "hop"))
	if msg.Hops < 0 {
		how = "it came by a direct route, which a packet doesn't record"
	}
	m.addLog(m.now, "MAP  that message's path isn't known: "+how, radio.ToneWarn)
}

// showContactPath opens the map on what's known of the way to a contact: the newest trace, which has every
// hop's signal, then the route the radio stores for it, then the path its advert took to reach us.
func (m *Model) showContactPath(ct radio.Contact) {
	cm := &m.contacts
	if tag, ok := cm.lastTrace[ct.Key]; ok {
		if run := cm.traces[tag]; run != nil && run.result != nil {
			res := run.result
			// A trace goes out along the route and back the same way; the map shows the way out.
			out := res.Hashes[:(len(res.Hashes)+1)/2]
			var hops [][]byte
			for _, h := range out {
				hops = append(hops, []byte{h})
			}
			if ct.Type == radio.TypeRepeater && len(hops) > 0 && hops[len(hops)-1][0] == ct.Key[0] {
				hops = hops[:len(hops)-1] // the last hop out is the repeater itself
			}
			snrs := make([]float64, len(hops)+1)
			for i := range snrs {
				snrs[i] = math.NaN() // a trace to a companion stops at its last repeater, so the hop to it isn't measured
				if i < len(out) && i < len(res.SNRs) {
					snrs[i] = float64(res.SNRs[i])
				}
			}
			r := mapRoute{what: "trace", from: mapEnd{me: true}, to: mapEnd{key: ct.Key, name: ct.Name}, hops: hops, hopSNR: snrs, at: run.at}
			m.showPath("trace to "+ct.Name+", "+ago(m.now.Sub(run.at)), ct.Name, []mapRoute{r})
			return
		}
	}
	if ct.PathLen >= 0 {
		m.showPath("your route to "+ct.Name, ct.Name, []mapRoute{{what: "stored route", from: mapEnd{me: true},
			to: mapEnd{key: ct.Key, name: ct.Name}, hops: splitPath(ct.Path, 1, true), at: m.now}})
		return
	}
	if ap, ok := cm.advert[ct.Key]; ok {
		m.showPath(ct.Name+"'s advert", "me", []mapRoute{{what: "advert path", from: mapEnd{key: ct.Key, name: ct.Name},
			to: mapEnd{me: true}, hops: splitPath(ap.Path, 1, true), at: ap.At}})
		return
	}
	m.addLog(m.now, "MAP  no route to "+ct.Name+" is known: messages to it flood. t on Contacts traces one", radio.ToneWarn)
}
