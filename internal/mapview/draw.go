package mapview

import (
	"math"
	"sort"
)

// Draw renders the static map, everything but the animations, onto a canvas w×h cells.
func Draw(in Input, v View, w, h int, pal Palette) *Canvas {
	v.W, v.H = w*2, h*4
	s := buildScene(in, v, pal)
	c := NewCanvas(w, h, pal.Land)
	proj := v.projector()
	project := func(rings [][]LatLon) [][][2]float64 {
		out := make([][][2]float64, 0, len(rings))
		for _, r := range rings {
			ring := make([][2]float64, len(r))
			for j, p := range r {
				ring[j][0], ring[j][1] = proj(p)
			}
			out = append(out, ring)
		}
		return out
	}
	land := c.evenOddMask(project(s.land))
	water := make([]bool, len(land))
	for i, l := range land {
		water[i] = !l
	}
	if s.detailOn {
		// Inside the tiles held, the street detail's water replaces the base map's.
		sea := c.unionMask(project(s.sea), project(s.isles))
		dw, dh := c.W*2, c.H*4
		for _, b := range in.Detail.boxes {
			x0, y0 := v.Project(LatLon{b[3], b[0]})
			x1, y1 := v.Project(LatLon{b[1], b[2]})
			for y := max(0, int(math.Round(y0))); y < min(dh, int(math.Round(y1))); y++ {
				for x := max(0, int(math.Round(x0))); x < min(dw, int(math.Round(x1))); x++ {
					water[y*dw+x] = sea[y*dw+x]
				}
			}
		}
	}
	c.setWater(water, pal.Land, pal.Sea)

	lines := s.lines
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].prio < lines[j].prio })
	for _, l := range lines {
		dash := 0
		if l.dash {
			dash = 4
		}
		for i := 0; i+1 < len(l.pts); i++ {
			x0, y0 := proj(l.pts[i])
			x1, y1 := proj(l.pts[i+1])
			c.Line(x0, y0, x1, y1, l.col, l.prio, dash)
			if l.bold {
				// Braille can't vary stroke width, so routes get a second parallel line to outweigh roads.
				d := math.Hypot(x1-x0, y1-y0)
				if d > 0 {
					nx, ny := math.Round(-(y1-y0)/d), math.Round((x1-x0)/d)
					c.Line(x0+nx, y0+ny, x1+nx, y1+ny, l.col, l.prio, dash)
				}
			}
		}
	}

	type placed struct {
		m      sMark
		cx, cy int
	}
	var ps []placed
	for _, m := range s.marks {
		x, y := v.Project(m.pos)
		cx, cy := int(math.Floor(x/2)), int(math.Floor(y/4))
		if cx < 0 || cy < 0 || cx >= w || cy >= h {
			continue
		}
		c.Text(cx, cy, string(m.glyph), m.col, m.bold)
		ps = append(ps, placed{m, cx, cy})
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].m.force && !ps[j].m.force })
	// The first pass keeps labels off routes; the second lets the rest cover unpicked routes rather than vanish.
	done := make([]bool, len(ps))
	for _, limit := range []int{prioRoute, prioRouteSel} {
		for i, p := range ps {
			if p.m.label == "" || done[i] {
				continue
			}
			n := textWidth(p.m.label)
			for _, pos := range [][2]int{{p.cx + 2, p.cy}, {p.cx - n - 1, p.cy}, {p.cx - n/2, p.cy - 1}, {p.cx - n/2, p.cy + 1}} {
				if c.Free(pos[0], pos[1], n, limit) {
					c.Text(pos[0], pos[1], p.m.label, p.m.col, p.m.bold)
					done[i] = true
					break
				}
			}
		}
	}
	for _, n := range s.notes {
		x, y := v.Project(n.pos)
		w := textWidth(n.label)
		for _, dy := range []int{0, -1, 1} {
			cx, cy := int(x)/2-w/2, int(y)/4+dy
			if c.Free(cx, cy, w, prioRouteSel+1) {
				c.Text(cx, cy, n.label, n.col, n.bold)
				break
			}
		}
	}
	for _, t := range s.towns {
		x, y := v.Project(t.pos)
		n := textWidth(t.label)
		cx, cy := int(x)/2-n/2, int(y)/4
		if c.Free(cx-1, cy, n+2, prioRiver+1) {
			c.Text(cx, cy, t.label, t.col, false)
		}
	}
	return c
}

// Credit writes the map data's attribution in the bottom right, on its own background so it reads over anything.
func Credit(c *Canvas, text string, pal Palette) {
	text = " " + text + " "
	n := textWidth(text)
	c.Label(max(c.W-n, 0), c.H-1, text, pal.Dim.Lerp(pal.Text, 0.35), pal.Bg)
}
