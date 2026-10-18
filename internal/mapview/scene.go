package mapview

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/verdantran/meshcore-tui/internal/theme"
)

type Kind int

const (
	KindSelf Kind = iota
	KindRepeater
	KindChat
	KindRoom
	KindOther
)

type Node struct {
	Name string
	Kind Kind
	Pos  LatLon
	Key  [32]byte
}

// Stop is a place a route passed through: one node, or several whose keys share the path's hash.
type Stop struct {
	Hash    string    // the hop as the path names it; empty for the sender and for us
	Nodes   []int     // the placed nodes it could be, likeliest first; none if no candidate has a location
	Weights []float64 // how likely each is, by how well it fits between its neighbours
}

// Link is the signal on one hop: measured by our radio for the last hop, or known from a trace or a
// repeater's neighbour list for the others.
type Link struct {
	SNR   float64
	Known bool
}

type Route struct {
	ID    string // the packet's hash and path, so a packet picked on another screen finds its route here
	What  string // ADVERT, or GRP_TXT and the channel
	From  string
	At    time.Time
	Flood bool
	Stops []Stop
	Links []Link // one per hop: Links[i] is the signal from Stops[i] to Stops[i+1]
}

// Placed lists the route's stops that have a position, with the links between them; a hop through an
// unplaced repeater, or an ambiguous one, comes back as uncertain.
func (r Route) Placed(nodes []Node) (pts []LatLon, links []Link, uncertain []bool) {
	gap := false
	for i, s := range r.Stops {
		if len(s.Nodes) == 0 {
			gap = true
			continue
		}
		if len(pts) > 0 {
			l := Link{}
			if !gap && i > 0 {
				l = r.Links[i-1]
			}
			links = append(links, l)
			uncertain = append(uncertain, gap || len(s.Nodes) > 1 || len(r.Stops[prevPlaced(r, i)].Nodes) > 1)
		}
		pts = append(pts, nodes[s.Nodes[0]].Pos)
		gap = false
	}
	return pts, links, uncertain
}

func prevPlaced(r Route, i int) int {
	for j := i - 1; j >= 0; j-- {
		if len(r.Stops[j].Nodes) > 0 {
			return j
		}
	}
	return i
}

// Km is the length of the placed part of the route.
func (r Route) Km(nodes []Node) float64 {
	pts, _, _ := r.Placed(nodes)
	total := 0.0
	for i := 1; i < len(pts); i++ {
		total += KmBetween(pts[i-1], pts[i])
	}
	return total
}

// Layers are what the map shows.
type Layers struct {
	Labels, Roads, Traffic bool
	Selected               int  // a route to pick out, or -1
	PathOnly               bool // show only the picked route: its nodes, named, and each hop's signal
}

// Flow is recent traffic over a link, for the traffic layer. Level runs from 0 to 1, the busiest link.
type Flow struct {
	A, B  LatLon
	Level float64
}

type Input struct {
	World  *World
	Detail *Detail
	Nodes  []Node
	Routes []Route // newest first
	Flows  []Flow
	Layers Layers
	Now    time.Time
}

// Only the newest routes are drawn, each at full strength for a couple of minutes and then fading out;
// the traffic layer keeps what they carried. A picked route is always drawn.
const (
	RoutesShown = 8
	routeFull   = 2 * time.Minute
	routeGone   = 10 * time.Minute
)

// RouteFade is how far a route heard at a time has faded: 0 is full strength, 1 is gone.
func RouteFade(at, now time.Time) float64 {
	return min(max(float64(now.Sub(at)-routeFull)/float64(routeGone-routeFull), 0), 1)
}

type sLine struct {
	pts  []LatLon
	col  theme.RGB
	prio int
	dash bool
	bold bool // drawn twice, offset, since braille can't vary width
}

type sMark struct {
	pos   LatLon
	glyph rune
	label string
	col   theme.RGB
	bold  bool
	force bool // label always drawn
}

type scene struct {
	land       [][]LatLon
	detailOn   bool
	sea, isles [][]LatLon
	lines      []sLine
	marks      []sMark
	towns      []sMark
	notes      []sMark // signal labels on a path's hops
}

const (
	prioBorder = 1 + iota
	prioCoast
	prioRiver
	prioPrimary
	prioTrunk
	prioMotorway
	prioTraffic
	prioBeacon
	prioRouteDim
	prioRouteAlt
	prioRoute
	prioRouteSel
	prioRipple
	prioAnim
)

// detailFrom is the zoom from which street detail replaces the base map where it's held.
const detailFrom = 7.0

func toLL(pts [][2]float64) []LatLon {
	out := make([]LatLon, len(pts))
	for i, p := range pts {
		out[i] = LatLon{p[1], p[0]}
	}
	return out
}

// maxRank is how many towns to label at a zoom: capitals at country scale, small towns close in.
func maxRank(zoom float64) int {
	switch {
	case zoom < 4.5:
		return 2
	case zoom < 5.5:
		return 4
	case zoom < 6.5:
		return 6
	case zoom < 7.5:
		return 7
	}
	return 10
}

func buildScene(in Input, v View, pal Palette) scene {
	var s scene
	l := in.Layers
	focus := l.Selected >= 0 && l.Selected < len(in.Routes)
	fade := func(c theme.RGB) theme.RGB {
		if focus {
			return c.Lerp(pal.Land, 0.55)
		}
		return c
	}
	view := v.Bounds().Grow(0.1)
	s.detailOn = in.Detail != nil && v.Zoom >= detailFrom
	inView := func(shapes []shape) [][]LatLon {
		var out [][]LatLon
		for _, sh := range shapes {
			if sh.box.Overlaps(view) {
				out = append(out, sh.pts)
			}
		}
		return out
	}
	if s.detailOn {
		s.sea, s.isles = inView(in.Detail.sea), inView(in.Detail.islands)
	}

	// The base map's coastline, minus the cuts Natural Earth makes along 180° and anything detail replaces.
	w := in.World
	for i, r := range w.Land {
		if !w.landBox[i].Overlaps(view) {
			continue
		}
		s.land = append(s.land, w.landLL[i])
		var run []LatLon
		flush := func() {
			if len(run) > 1 {
				s.lines = append(s.lines, sLine{pts: run, col: fade(pal.Coast), prio: prioCoast})
			}
			run = nil
		}
		for k := range r {
			a, b := r[k], r[(k+1)%len(r)]
			mid := LatLon{(a[1] + b[1]) / 2, (a[0] + b[0]) / 2}
			if math.Abs(a[0]) == 180 && a[0] == b[0] || s.detailOn && in.Detail.Covers(mid) {
				flush()
				continue
			}
			if len(run) == 0 {
				run = append(run, LatLon{a[1], a[0]})
			}
			run = append(run, LatLon{b[1], b[0]})
		}
		flush()
	}
	for i := range w.Borders {
		if w.borderBox[i].Overlaps(view) {
			s.lines = append(s.lines, sLine{pts: w.borderLL[i], col: fade(pal.Border), prio: prioBorder, dash: true})
		}
	}
	if s.detailOn {
		add := func(kind string, col theme.RGB, prio int) {
			for _, pts := range inView(in.Detail.lines[kind]) {
				s.lines = append(s.lines, sLine{pts: pts, col: fade(col), prio: prio})
			}
		}
		add("coast", pal.Coast, prioCoast)
		add("river", pal.River, prioRiver)
		if l.Roads {
			add("primary", pal.Primary, prioPrimary)
			add("trunk", pal.Trunk, prioTrunk)
			add("motorway", pal.Motorway, prioMotorway)
		}
	}

	only := l.PathOnly && focus
	if l.Traffic && !only {
		// Saturation is the whole story here, in one hue, so it never reads as the signal colours routes use.
		for _, f := range in.Flows {
			col := pal.Land.Lerp(pal.Violet, 0.2+0.8*f.Level)
			s.lines = append(s.lines, sLine{pts: []LatLon{f.A, f.B}, col: fade(col), prio: prioTraffic, bold: f.Level >= 0.6})
		}
	}

	onRoute := map[int]bool{}
	for i, r := range in.Routes {
		sel := i == l.Selected
		if only && !sel {
			continue
		}
		age := RouteFade(r.At, in.Now)
		if !sel && (i >= RoutesShown || age >= 1) {
			continue
		}
		if focus && !sel {
			pts, _, _ := r.Placed(in.Nodes)
			if len(pts) > 1 {
				s.lines = append(s.lines, sLine{pts: pts, col: pal.Unknown.Lerp(pal.Land, 0.4+0.5*age), prio: prioRouteDim})
			}
			continue
		}
		if sel {
			age = 0
		}
		pts, links, uncertain := r.Placed(in.Nodes)
		for j := 0; j+1 < len(pts); j++ {
			col := pal.Unknown
			if links[j].Known {
				col = pal.SNR(links[j].SNR)
			}
			col = col.Lerp(pal.Land, 0.85*age)
			prio := prioRoute
			if sel {
				prio = prioRouteSel
			}
			s.lines = append(s.lines, sLine{pts: []LatLon{pts[j], pts[j+1]}, col: col, prio: prio, dash: uncertain[j], bold: !uncertain[j]})
		}
		for k, st := range r.Stops {
			for _, n := range st.Nodes {
				onRoute[n] = onRoute[n] || sel
			}
			if !sel || len(st.Nodes) < 2 {
				continue
			}
			// Each other candidate for an ambiguous hop gets a dashed line from the stops either side.
			for _, alt := range st.Nodes[1:] {
				for _, end := range []int{k - 1, k + 1} {
					if end >= 0 && end < len(r.Stops) && len(r.Stops[end].Nodes) > 0 {
						s.lines = append(s.lines, sLine{pts: []LatLon{in.Nodes[r.Stops[end].Nodes[0]].Pos, in.Nodes[alt].Pos},
							col: pal.AltRoute, prio: prioRouteAlt, dash: true})
					}
				}
			}
		}
	}

	if only {
		// Each hop's signal, where known, labelled half way along it.
		pts, links, _ := in.Routes[l.Selected].Placed(in.Nodes)
		for j := 0; j+1 < len(pts); j++ {
			if links[j].Known {
				mid := LatLon{(pts[j].Lat + pts[j+1].Lat) / 2, (pts[j].Lon + near(pts[j+1].Lon, pts[j].Lon)) / 2}
				s.notes = append(s.notes, sMark{pos: mid, label: fmt.Sprintf("%+.1f", links[j].SNR), col: pal.SNR(links[j].SNR), bold: true})
			}
		}
	}
	for i, n := range in.Nodes {
		if only && !onRoute[i] {
			continue
		}
		if n.Pos.Unplaced() {
			continue
		}
		m := sMark{pos: n.Pos, label: n.Name}
		switch n.Kind {
		case KindSelf:
			m.glyph, m.col, m.bold, m.force = '◉', pal.Self, true, true
		case KindRepeater:
			m.glyph, m.col = '▲', pal.Repeater
		case KindRoom:
			m.glyph, m.col = '■', pal.Room
		default:
			m.glyph, m.col = '●', pal.Chat
		}
		if focus {
			if onRoute[i] {
				m.force, m.bold = true, true
				if p := ambiguity(in.Routes[l.Selected], i, in.Nodes); p != "" {
					m.label = p
				}
			} else {
				m.col = pal.Faded
			}
		}
		if !l.Labels && !m.force {
			m.label = ""
		}
		s.marks = append(s.marks, m)
	}
	if v.Zoom < detailFrom {
		s.marks = cluster(s.marks, v.Zoom, pal)
	}
	if l.Labels {
		limit := maxRank(v.Zoom)
		places := w.Places
		if s.detailOn {
			// Inside the detail its own towns are used: at this scale they're more complete than Natural Earth's.
			places = nil
			for _, p := range w.Places {
				if !in.Detail.Covers(LatLon{p.Lat, p.Lon}) {
					places = append(places, p)
				}
			}
			places = append(places, in.Detail.Places...)
		}
		var shown []Place
		for _, p := range places {
			if p.Rank <= limit && view.Contains(LatLon{p.Lat, p.Lon}) {
				shown = append(shown, p)
			}
		}
		sort.SliceStable(shown, func(i, j int) bool { return shown[i].Rank < shown[j].Rank })
		for _, p := range shown {
			s.towns = append(s.towns, sMark{pos: LatLon{p.Lat, p.Lon}, label: strings.ToUpper(p.Name), col: fade(pal.Town)})
		}
	}
	return s
}

// ambiguity labels a node that's one of several a hop could be, with how likely it is.
func ambiguity(r Route, node int, nodes []Node) string {
	for _, st := range r.Stops {
		if len(st.Nodes) < 2 {
			continue
		}
		for i, n := range st.Nodes {
			if n == node {
				return fmt.Sprintf("%s? %.0f%%", nodes[n].Name, st.Weights[i]*100)
			}
		}
	}
	return ""
}

// cluster merges nodes that would overlap when zoomed out into one counted marker.
func cluster(marks []sMark, zoom float64, pal Palette) []sMark {
	const radius = 24.0 // dots
	type group struct {
		x, y  float64
		marks []sMark
		self  bool
	}
	sort.SliceStable(marks, func(i, j int) bool { return marks[i].glyph == '◉' && marks[j].glyph != '◉' })
	var groups []*group
	for _, m := range marks {
		x, y := worldXY(m.pos, zoom)
		var g *group
		for _, c := range groups {
			if math.Hypot(c.x-x, c.y-y) <= radius {
				g = c
				break
			}
		}
		if g == nil {
			g = &group{x: x, y: y}
			groups = append(groups, g)
		}
		g.marks = append(g.marks, m)
		g.self = g.self || m.glyph == '◉'
	}
	var out []sMark
	for _, g := range groups {
		if len(g.marks) == 1 {
			out = append(out, g.marks[0])
			continue
		}
		first := g.marks[0].pos
		var lat, lon float64
		for _, m := range g.marks {
			lat += m.pos.Lat
			lon += near(m.pos.Lon, first.Lon)
		}
		n := float64(len(g.marks))
		c := sMark{pos: LatLon{lat / n, near(lon/n, 0)}, glyph: '▲', col: pal.Repeater, bold: true, force: true,
			label: fmt.Sprintf("%d nodes", len(g.marks))}
		if g.self {
			c.glyph, c.col = '◉', pal.Self
			c.label = fmt.Sprintf("me + %d nodes", len(g.marks)-1)
		}
		out = append(out, c)
	}
	return out
}
