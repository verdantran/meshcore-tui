package mapview

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"meshcore-tui/internal/theme"
)

type cell struct {
	mask  uint8 // braille dots set
	prio  int
	fg    theme.RGB
	bg    theme.RGB
	text  rune
	bold  bool
	solid bool // text with its own background, kept legible over anything
	cont  bool // covered by the wide character in the cell before
}

// Canvas is a grid of cells, each a 2×4 braille dot block with one foreground and one background colour.
type Canvas struct {
	W, H  int
	cells []cell
}

func NewCanvas(w, h int, bg theme.RGB) *Canvas {
	c := &Canvas{W: w, H: h, cells: make([]cell, w*h)}
	for i := range c.cells {
		c.cells[i].bg = bg
	}
	return c
}

func (c *Canvas) Clone() *Canvas {
	d := *c
	d.cells = append([]cell(nil), c.cells...)
	return &d
}

func (c *Canvas) at(cx, cy int) *cell {
	if cx < 0 || cy < 0 || cx >= c.W || cy >= c.H {
		return nil
	}
	return &c.cells[cy*c.W+cx]
}

var dotBits = [4][2]uint8{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

// Dot sets one braille dot. A higher-priority layer takes over the cell and clears what was there,
// because a cell can only carry one colour.
func (c *Canvas) Dot(x, y int, col theme.RGB, prio int) {
	if x < 0 || y < 0 {
		return
	}
	ce := c.at(x/2, y/4)
	if ce == nil || ce.text != 0 || ce.cont || prio < ce.prio {
		return
	}
	if prio > ce.prio {
		ce.mask, ce.prio = 0, prio
	}
	ce.fg = col
	ce.mask |= dotBits[y%4][x%2]
}

// Line draws in dot space; dash > 0 leaves gaps of that many dots.
func (c *Canvas) Line(x0, y0, x1, y1 float64, col theme.RGB, prio, dash int) {
	if math.Max(x0, x1) < -50 || math.Min(x0, x1) > float64(c.W*2+50) ||
		math.Max(y0, y1) < -50 || math.Min(y0, y1) > float64(c.H*4+50) {
		return
	}
	steps := int(math.Max(math.Abs(x1-x0), math.Abs(y1-y0))) + 1
	if steps > 4000 {
		return
	}
	for i := 0; i <= steps; i++ {
		if dash > 0 && (i/dash)%2 == 1 {
			continue
		}
		t := float64(i) / float64(steps)
		c.Dot(int(math.Round(x0+(x1-x0)*t)), int(math.Round(y0+(y1-y0)*t)), col, prio)
	}
}

// Text writes a label. Wide characters such as emoji take two cells; ones that would be cut off are dropped.
func (c *Canvas) Text(cx, cy int, s string, col theme.RGB, bold bool) {
	x := cx
	for _, r := range s {
		w := ansi.StringWidth(string(r))
		if w == 0 {
			continue
		}
		ce := c.at(x, cy)
		if ce != nil {
			ce.text, ce.fg, ce.bold, ce.mask, ce.cont = r, col, bold, 0, false
			if w == 2 {
				if next := c.at(x+1, cy); next != nil {
					next.text, next.mask, next.cont, next.fg = 0, 0, true, col
				} else {
					ce.text = ' '
				}
			}
		}
		x += w
	}
}

// textWidth is how many cells Text will use.
func textWidth(s string) int { return ansi.StringWidth(s) }

// Label writes text on its own background strip, for the credit and notices over the map.
func (c *Canvas) Label(cx, cy int, s string, fg, bg theme.RGB) {
	c.Text(cx, cy, s, fg, false)
	for i := range textWidth(s) {
		if ce := c.at(cx+i, cy); ce != nil {
			ce.bg, ce.solid = bg, true
		}
	}
}

// Free reports whether n cells are clear of text and of lines at or above prio limit.
func (c *Canvas) Free(cx, cy, n, limit int) bool {
	for i := 0; i < n; i++ {
		ce := c.at(cx+i, cy)
		if ce == nil || ce.text != 0 || ce.cont || ce.prio >= limit {
			return false
		}
	}
	return true
}

// evenOddMask marks the dots inside rings, scanning each dot row. The base map's land has no overlaps,
// so even-odd filling is right for it and handles its lakes.
func (c *Canvas) evenOddMask(rings [][][2]float64) []bool {
	w := c.W * 2
	mask := make([]bool, w*c.H*4)
	for y := 0; y < c.H*4; y++ {
		fy := float64(y) + 0.5
		var xs []float64
		for _, ring := range rings {
			for i := range ring {
				a, b := ring[i], ring[(i+1)%len(ring)]
				if (a[1] <= fy) != (b[1] <= fy) {
					xs = append(xs, a[0]+(fy-a[1])/(b[1]-a[1])*(b[0]-a[0]))
				}
			}
		}
		sort.Float64s(xs)
		for i := 0; i+1 < len(xs); i += 2 {
			fill(mask[y*w:(y+1)*w], xs[i], xs[i+1])
		}
	}
	return mask
}

// unionMask fills by nonzero winding: adds are unioned however they overlap, and cuts are taken out.
// Tiles' water overlaps at their edges, which even-odd filling would count as land.
func (c *Canvas) unionMask(adds, cuts [][][2]float64) []bool {
	type crossing struct {
		x float64
		d int
	}
	type ring struct {
		pts  [][2]float64
		sign int
	}
	orient := func(r [][2]float64) int {
		a := 0.0
		for i := range r {
			p, q := r[i], r[(i+1)%len(r)]
			a += p[0]*q[1] - q[0]*p[1]
		}
		if a < 0 {
			return -1
		}
		return 1
	}
	var rings []ring
	for _, r := range adds {
		rings = append(rings, ring{r, orient(r)})
	}
	for _, r := range cuts {
		rings = append(rings, ring{r, -orient(r)})
	}
	w := c.W * 2
	mask := make([]bool, w*c.H*4)
	for y := 0; y < c.H*4; y++ {
		fy := float64(y) + 0.5
		var xs []crossing
		for _, r := range rings {
			for i := range r.pts {
				a, b := r.pts[i], r.pts[(i+1)%len(r.pts)]
				if (a[1] <= fy) != (b[1] <= fy) {
					d := r.sign
					if a[1] > b[1] {
						d = -d
					}
					xs = append(xs, crossing{a[0] + (fy-a[1])/(b[1]-a[1])*(b[0]-a[0]), d})
				}
			}
		}
		sort.Slice(xs, func(i, j int) bool { return xs[i].x < xs[j].x })
		wind := 0
		for i := 0; i+1 < len(xs); i++ {
			if wind += xs[i].d; wind != 0 {
				fill(mask[y*w:(y+1)*w], xs[i].x, xs[i+1].x)
			}
		}
	}
	return mask
}

func fill(row []bool, from, to float64) {
	x0 := int(math.Max(0, math.Ceil(from-0.5)))
	x1 := int(math.Min(float64(len(row)-1), math.Floor(to-0.5)))
	for x := x0; x <= x1; x++ {
		row[x] = true
	}
}

// setWater shades each cell's background by how many of its 8 dots are sea, which softens the shore.
func (c *Canvas) setWater(water []bool, land, sea theme.RGB) {
	w := c.W * 2
	counts := make([]int, c.W*c.H)
	for y := 0; y < c.H*4; y++ {
		for x := 0; x < w; x++ {
			if water[y*w+x] {
				counts[(y/4)*c.W+x/2]++
			}
		}
	}
	for i, n := range counts {
		c.cells[i].bg = land.Lerp(sea, float64(n)/8)
	}
}

func sgr(fg, bg theme.RGB, bold bool) string {
	b := "22"
	if bold {
		b = "1"
	}
	return fmt.Sprintf("\x1b[%s;38;2;%d;%d;%d;48;2;%d;%d;%dm", b, fg.R, fg.G, fg.B, bg.R, bg.G, bg.B)
}

// Render emits the canvas as terminal rows. Without colour (the mono theme, NO_COLOR) only glyphs are drawn.
func (c *Canvas) Render(colour bool) []string {
	rows := make([]string, c.H)
	for y := 0; y < c.H; y++ {
		var sb strings.Builder
		last := ""
		for x := 0; x < c.W; x++ {
			ce := c.cells[y*c.W+x]
			if colour {
				if s := sgr(ce.fg, ce.bg, ce.bold); s != last {
					sb.WriteString(s)
					last = s
				}
			}
			switch {
			case ce.cont:
			case ce.text != 0:
				sb.WriteRune(ce.text)
			case ce.mask != 0:
				sb.WriteRune(rune(0x2800 + int(ce.mask)))
			default:
				sb.WriteByte(' ')
			}
		}
		if colour {
			sb.WriteString("\x1b[0m")
		}
		rows[y] = sb.String()
	}
	return rows
}
