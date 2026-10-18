package mapview

import (
	"math"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/clipperhouse/uax29/v2/graphemes"

	"github.com/verdantran/meshcore-tui/internal/theme"
)

type cell struct {
	mask  uint8 // braille dots set
	prio  int
	fg    theme.RGB
	bg    theme.RGB
	text  string // a whole grapheme cluster, so the terminal joins only what was measured joined
	bold  bool
	solid bool // text with its own background, kept legible over anything
	cont  bool // covered by the wide character in a cell before
}

var labelWidth = ansi.WcWidth

// SetWidth measures labels with the method the terminal draws with, so a joined emoji takes the cells it's drawn in.
func SetWidth(m ansi.Method) { labelWidth = m }

// Canvas is a grid of cells, each a 2×4 braille dot block with one foreground and one background colour.
type Canvas struct {
	W, H  int
	cells []cell
	dirty []bool // rows changed since the canvas was made or cloned, so a copy only re-renders those
}

func NewCanvas(w, h int, bg theme.RGB) *Canvas {
	c := &Canvas{W: w, H: h, cells: make([]cell, w*h), dirty: make([]bool, h)}
	for i := range c.cells {
		c.cells[i].bg = bg
	}
	return c
}

// Clone copies the canvas with every row clean, so drawing on the copy marks just the rows it touches.
func (c *Canvas) Clone() *Canvas {
	d := *c
	d.cells = append([]cell(nil), c.cells...)
	d.dirty = make([]bool, c.H)
	return &d
}

// Dirty reports whether a row has changed since the canvas was cloned.
func (c *Canvas) Dirty(y int) bool { return c.dirty[y] }

func (c *Canvas) at(cx, cy int) *cell {
	if cx < 0 || cy < 0 || cx >= c.W || cy >= c.H {
		return nil
	}
	c.dirty[cy] = true
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
	if ce == nil || ce.text != "" || ce.cont || prio < ce.prio {
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

// Text writes a label a grapheme cluster at a time. Wide ones such as emoji take more than one cell; ones
// that would be cut off become blanks.
func (c *Canvas) Text(cx, cy int, s string, col theme.RGB, bold bool) {
	x := cx
	g := graphemes.FromString(s)
	for g.Next() {
		cl := g.Value()
		w := labelWidth.StringWidth(cl)
		if w == 0 {
			continue
		}
		fits := x >= 0 && x+w <= c.W
		for i := range w {
			ce := c.at(x+i, cy)
			switch {
			case ce == nil:
			case !fits:
				ce.text, ce.fg, ce.bold, ce.mask, ce.cont = " ", col, bold, 0, false
			case i == 0:
				ce.text, ce.fg, ce.bold, ce.mask, ce.cont = cl, col, bold, 0, false
			default:
				ce.text, ce.mask, ce.cont, ce.fg = "", 0, true, col
			}
		}
		x += w
	}
}

// textWidth is how many cells Text will use.
func textWidth(s string) int {
	w := 0
	g := graphemes.FromString(s)
	for g.Next() {
		w += labelWidth.StringWidth(g.Value())
	}
	return w
}

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
		if ce == nil || ce.text != "" || ce.cont || ce.prio >= limit {
			return false
		}
	}
	return true
}

// edge is one side of a ring, for scanline filling: the rows it spans, where it crosses each, and which way it runs.
type edge struct {
	y0, y1 int // dot rows whose centres it crosses, inclusive
	x, dx  float64
	dir    int
}

// edgeTable buckets ring edges by the first dot row they cross, so each row only looks at the edges that
// reach it. Scanning every edge on every row was most of the cost of drawing the map.
func (c *Canvas) edgeTable(rings [][][2]float64, signs []int) [][]edge {
	rows := c.H * 4
	table := make([][]edge, rows)
	for r, ring := range rings {
		for i := range ring {
			a, b := ring[i], ring[(i+1)%len(ring)]
			dir := signs[r]
			if a[1] > b[1] {
				a, b, dir = b, a, -dir
			}
			// Rows whose centre y+0.5 lies in [a.y, b.y).
			y0 := max(int(math.Ceil(a[1]-0.5)), 0)
			y1 := min(int(math.Ceil(b[1]-0.5))-1, rows-1)
			if y0 > y1 {
				continue
			}
			dx := (b[0] - a[0]) / (b[1] - a[1])
			table[y0] = append(table[y0], edge{y0, y1, a[0] + (float64(y0)+0.5-a[1])*dx, dx, dir})
		}
	}
	return table
}

type crossing struct {
	x float64
	d int
}

// scan walks the rows, keeping the edges that cross each, and fills between crossings where inside says so.
func (c *Canvas) scan(table [][]edge, inside func(wind int) bool) []bool {
	w := c.W * 2
	mask := make([]bool, w*c.H*4)
	var active []edge
	var xs []crossing
	for y := range table {
		active = append(active, table[y]...)
		xs = xs[:0]
		live := active[:0]
		for _, e := range active {
			if e.y1 < y {
				continue
			}
			xs = append(xs, crossing{e.x + float64(y-e.y0)*e.dx, e.dir})
			live = append(live, e)
		}
		active = live
		sort.Slice(xs, func(i, j int) bool { return xs[i].x < xs[j].x })
		wind := 0
		for i := 0; i+1 < len(xs); i++ {
			if wind += xs[i].d; inside(wind) {
				fill(mask[y*w:(y+1)*w], xs[i].x, xs[i+1].x)
			}
		}
	}
	return mask
}

// evenOddMask marks the dots inside rings. The base map's land has no overlaps, so even-odd filling is
// right for it and handles its lakes.
func (c *Canvas) evenOddMask(rings [][][2]float64) []bool {
	signs := make([]int, len(rings))
	for i := range signs {
		signs[i] = 1
	}
	return c.scan(c.edgeTable(rings, signs), func(wind int) bool { return wind%2 != 0 })
}

// unionMask fills by nonzero winding: adds are unioned however they overlap, and cuts are taken out.
// Tiles' water overlaps at their edges, which even-odd filling would count as land.
func (c *Canvas) unionMask(adds, cuts [][][2]float64) []bool {
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
	rings := append(append([][][2]float64(nil), adds...), cuts...)
	signs := make([]int, len(rings))
	for i, r := range rings {
		signs[i] = orient(r)
		if i >= len(adds) {
			signs[i] = -signs[i]
		}
	}
	return c.scan(c.edgeTable(rings, signs), func(wind int) bool { return wind != 0 })
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

func appendColour(b []byte, layer int, c theme.RGB) []byte {
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(layer), 10)
	b = append(b, ";2;"...)
	b = strconv.AppendInt(b, int64(c.R), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(c.G), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(c.B), 10)
	return append(b, 'm')
}

// RenderRow emits one row for the terminal. Colours are only sent when they change, and a blank cell only
// needs its background; this keeps a full-screen map to a fraction of the bytes, which the terminal
// program has to read every frame. Without colour (the mono theme, NO_COLOR) only glyphs are drawn.
func (c *Canvas) RenderRow(y int, colour bool) string {
	b := make([]byte, 0, c.W*4)
	var fg, bg theme.RGB
	hasFg, hasBg, bold := false, false, false
	for x := 0; x < c.W; x++ {
		ce := c.cells[y*c.W+x]
		if ce.cont {
			continue
		}
		glyph := ' '
		if ce.text == "" && ce.mask != 0 {
			glyph = rune(0x2800 + int(ce.mask))
		}
		if colour {
			if !hasBg || ce.bg != bg {
				b, bg, hasBg = appendColour(b, 48, ce.bg), ce.bg, true
			}
			if ce.text != "" && ce.text != " " || ce.text == "" && glyph != ' ' {
				if !hasFg || ce.fg != fg {
					b, fg, hasFg = appendColour(b, 38, ce.fg), ce.fg, true
				}
				if ce.bold != bold {
					b, bold = append(b, map[bool]string{true: "\x1b[1m", false: "\x1b[22m"}[ce.bold]...), ce.bold
				}
			}
		}
		if ce.text != "" {
			b = append(b, ce.text...)
		} else {
			b = utf8.AppendRune(b, glyph)
		}
	}
	if colour {
		b = append(b, "\x1b[0m"...)
	}
	return string(b)
}

func (c *Canvas) Render(colour bool) []string {
	rows := make([]string, c.H)
	for y := range rows {
		rows[y] = c.RenderRow(y, colour)
	}
	return rows
}
