package mapview

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"meshcore-tui/internal/theme"
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
	Labels, Roads bool
	Selected      int // a route to pick out, or -1
}

type Input struct {
	World  *World
	Detail *Detail
	Nodes  []Node
	Routes []Route
	Layers Layers
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
}

const (
	prioBorder = 1 + iota
	prioCoast
	prioRiver
	prioPrimary
	prioTrunk
	prioMotorway
	prioSweep
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
	if s.detailOn {
		for _, r := range in.Detail.Sea {
			s.sea = append(s.sea, toLL(r))
		}
		for _, r := range in.Detail.Islands {
			s.isles = append(s.isles, toLL(r))
		}
	}

	// The base map's coastline, minus the cuts Natural Earth makes along 180° and anything detail replaces.
	w := in.World
	for i, r := range w.Land {
		if !w.landBox[i].Overlaps(view) {
			continue
		}
		s.land = append(s.land, toLL(r))
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
	for i, b := range w.Borders {
		if w.borderBox[i].Overlaps(view) {
			s.lines = append(s.lines, sLine{pts: toLL(b), col: fade(pal.Border), prio: prioBorder, dash: true})
		}
	}
	if s.detailOn {
		add := func(kind string, col theme.RGB, prio int) {
			for _, pts := range in.Detail.Lines[kind] {
				s.lines = append(s.lines, sLine{pts: toLL(pts), col: fade(col), prio: prio})
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

	onRoute := map[int]bool{}
	for i, r := range in.Routes {
		sel := i == l.Selected
		if focus && !sel {
			pts, _, _ := r.Placed(in.Nodes)
			if len(pts) > 1 {
				s.lines = append(s.lines, sLine{pts: pts, col: pal.Unknown.Lerp(pal.Land, 0.4), prio: prioRouteDim})
			}
			continue
		}
		pts, links, uncertain := r.Placed(in.Nodes)
		for j := 0; j+1 < len(pts); j++ {
			col := pal.Unknown
			if links[j].Known {
				col = pal.SNR(links[j].SNR)
			}
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

	for i, n := range in.Nodes {
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
