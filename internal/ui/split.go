package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type splitDir int

const (
	leaf splitDir = iota
	sideBySide
	stacked
)

const (
	minPaneW   = 30
	minPaneH   = 8
	ratioStep  = 0.05
	ratioLimit = 0.2 // neither side of a split gets less than this share
)

// pane is a node in the layout tree: a leaf shows one screen; a split holds two panes.
type pane struct {
	dir   splitDir
	tab   int
	ratio float64 // the first pane's share of the split
	a, b  *pane
}

type rect struct{ x, y, w, h int }

type placed struct {
	p *pane
	r rect
}

type layout struct {
	root, focus *pane
	zoomed      bool
}

func newLayout(tab int) layout {
	p := &pane{tab: tab}
	return layout{root: p, focus: p}
}

func (l *layout) leaves() []*pane {
	var out []*pane
	var walk func(*pane)
	walk = func(p *pane) {
		if p.dir == leaf {
			out = append(out, p)
			return
		}
		walk(p.a)
		walk(p.b)
	}
	walk(l.root)
	return out
}

func (l *layout) split() bool { return l.root.dir != leaf }

func (l *layout) showing(tab int) *pane {
	for _, p := range l.leaves() {
		if p.tab == tab {
			return p
		}
	}
	return nil
}

func (l *layout) parent(of *pane) *pane {
	var found *pane
	var walk func(*pane)
	walk = func(p *pane) {
		if p.dir == leaf || found != nil {
			return
		}
		if p.a == of || p.b == of {
			found = p
			return
		}
		walk(p.a)
		walk(p.b)
	}
	walk(l.root)
	return found
}

// place works out where each leaf goes in a w×h area.
func (l *layout) place(w, h int) []placed {
	var out []placed
	var walk func(*pane, rect)
	walk = func(p *pane, r rect) {
		switch p.dir {
		case leaf:
			out = append(out, placed{p, r})
		case sideBySide:
			aw := int(math.Round(float64(r.w) * p.ratio))
			walk(p.a, rect{r.x, r.y, aw, r.h})
			walk(p.b, rect{r.x + aw, r.y, r.w - aw, r.h})
		case stacked:
			ah := int(math.Round(float64(r.h) * p.ratio))
			walk(p.a, rect{r.x, r.y, r.w, ah})
			walk(p.b, rect{r.x, r.y + ah, r.w, r.h - ah})
		}
	}
	walk(l.root, rect{0, 0, w, h})
	return out
}

func (l *layout) rectOf(p *pane, w, h int) rect {
	for _, pl := range l.place(w, h) {
		if pl.p == p {
			return pl.r
		}
	}
	return rect{}
}

// divide turns the focused leaf into a split, with tab in the new pane, which takes the focus.
func (l *layout) divide(dir splitDir, tab int) {
	old := l.focus
	kept := &pane{tab: old.tab}
	added := &pane{tab: tab}
	*old = pane{dir: dir, ratio: 0.5, a: kept, b: added}
	l.focus, l.zoomed = added, false
}

// close removes the focused leaf; its sibling takes the split's place and the focus.
func (l *layout) close() bool {
	par := l.parent(l.focus)
	if par == nil {
		return false
	}
	sib := par.a
	if sib == l.focus {
		sib = par.b
	}
	*par = *sib
	l.focus = par
	for l.focus.dir != leaf {
		l.focus = l.focus.a
	}
	l.zoomed = false
	return true
}

// neighbour finds the leaf next to the focused one in a direction, the closest that lines up with it.
func (l *layout) neighbour(dx, dy, w, h int) *pane {
	all := l.place(w, h)
	var from rect
	for _, pl := range all {
		if pl.p == l.focus {
			from = pl.r
		}
	}
	var best *pane
	bestDist := math.MaxInt
	for _, pl := range all {
		r := pl.r
		var dist int
		switch {
		case dx > 0 && r.x >= from.x+from.w && overlap(r.y, r.h, from.y, from.h):
			dist = r.x - from.x
		case dx < 0 && r.x+r.w <= from.x && overlap(r.y, r.h, from.y, from.h):
			dist = from.x - r.x
		case dy > 0 && r.y >= from.y+from.h && overlap(r.x, r.w, from.x, from.w):
			dist = r.y - from.y
		case dy < 0 && r.y+r.h <= from.y && overlap(r.x, r.w, from.x, from.w):
			dist = from.y - r.y
		default:
			continue
		}
		if dist < bestDist {
			best, bestDist = pl.p, dist
		}
	}
	return best
}

func overlap(a, an, b, bn int) bool { return a < b+bn && b < a+an }

// resize grows the focused pane by one step along a direction, using the nearest split that runs that way.
func (l *layout) resize(dir splitDir, grow bool) bool {
	child := l.focus
	for par := l.parent(child); par != nil; child, par = par, l.parent(par) {
		if par.dir != dir {
			continue
		}
		step := ratioStep
		if (par.a == child) != grow {
			step = -step
		}
		par.ratio = math.Round(min(max(par.ratio+step, ratioLimit), 1-ratioLimit)*100) / 100
		return true
	}
	return false
}

// even shares every split out by how many panes each side holds.
func (l *layout) even() {
	var count func(*pane) int
	count = func(p *pane) int {
		if p.dir == leaf {
			return 1
		}
		na, nb := count(p.a), count(p.b)
		p.ratio = float64(na) / float64(na+nb)
		return na + nb
	}
	count(l.root)
}

// String writes the layout for the config file, e.g. "right 50 (*chats, packets)"; * marks the focus.
func (l *layout) String() string {
	var b strings.Builder
	var walk func(*pane)
	walk = func(p *pane) {
		switch p.dir {
		case leaf:
			if p == l.focus {
				b.WriteString("*")
			}
			b.WriteString(strings.ToLower(tabNames[p.tab]))
		default:
			name := map[splitDir]string{sideBySide: "right", stacked: "down"}[p.dir]
			fmt.Fprintf(&b, "%s %d (", name, int(math.Round(p.ratio*100)))
			walk(p.a)
			b.WriteString(", ")
			walk(p.b)
			b.WriteString(")")
		}
	}
	walk(l.root)
	return b.String()
}

// parseLayout reads a layout written by String; anything it can't read gives ok false.
func parseLayout(s string) (layout, bool) {
	toks := strings.Fields(strings.NewReplacer("(", " ( ", ")", " ) ", ",", " , ").Replace(s))
	var l layout
	seen := map[int]bool{}
	var parse func() *pane
	parse = func() *pane {
		if len(toks) == 0 {
			return nil
		}
		t := toks[0]
		toks = toks[1:]
		if t == "right" || t == "down" {
			if len(toks) < 2 || toks[1] != "(" {
				return nil
			}
			pct, err := strconv.Atoi(toks[0])
			if err != nil || pct < 20 || pct > 80 {
				return nil
			}
			toks = toks[2:]
			a := parse()
			if a == nil || len(toks) == 0 || toks[0] != "," {
				return nil
			}
			toks = toks[1:]
			b := parse()
			if b == nil || len(toks) == 0 || toks[0] != ")" {
				return nil
			}
			toks = toks[1:]
			dir := sideBySide
			if t == "down" {
				dir = stacked
			}
			return &pane{dir: dir, ratio: float64(pct) / 100, a: a, b: b}
		}
		focus := strings.HasPrefix(t, "*")
		t = strings.TrimPrefix(t, "*")
		for i, n := range tabNames {
			if strings.EqualFold(n, t) && !seen[i] {
				seen[i] = true
				p := &pane{tab: i}
				if focus {
					l.focus = p
				}
				return p
			}
		}
		return nil
	}
	l.root = parse()
	if l.root == nil || len(toks) > 0 {
		return layout{}, false
	}
	if l.focus == nil {
		l.focus = l.leaves()[0]
	}
	return l, true
}

// The model's side: m.tab is always the focused pane's screen.

func (m *Model) visible(tab int) bool {
	if m.lay.zoomed {
		return m.tab == tab
	}
	return m.lay.showing(tab) != nil
}

func (m *Model) focusPane(p *pane) {
	if p == nil || p == m.lay.focus {
		return
	}
	m.lay.focus = p
	m.tab, m.tabAt = p.tab, m.now
	m.saveLayout()
}

// splitTarget picks what a new pane shows: the first screen in this order that isn't on screen yet.
var splitOrder = []int{3, 0, 1, 6, 4, 5, 2}

func (m *Model) splitPane(dir splitDir, tab int) error {
	if tab < 0 {
		for _, t := range splitOrder {
			if m.lay.showing(t) == nil {
				tab = t
				break
			}
		}
	}
	if tab < 0 {
		return fmt.Errorf("every screen is already showing")
	}
	if m.lay.showing(tab) != nil {
		return fmt.Errorf("%s is already showing", strings.ToLower(tabNames[tab]))
	}
	r := m.lay.rectOf(m.lay.focus, m.w, m.bodyHeight())
	if dir == sideBySide && r.w/2 < minPaneW || dir == stacked && r.h/2 < minPaneH {
		return fmt.Errorf("no room to split this pane; make the window bigger or close a pane")
	}
	m.lay.divide(dir, tab)
	m.tab, m.tabAt = tab, m.now
	m.saveLayout()
	return nil
}

func (m *Model) closePane() error {
	if !m.lay.close() {
		return fmt.Errorf("this is the only pane")
	}
	m.tab, m.tabAt = m.lay.focus.tab, m.now
	m.saveLayout()
	return nil
}

func (m *Model) saveLayout() {
	if m.opt.Config == nil {
		return
	}
	s := ""
	if m.lay.split() {
		s = m.lay.String()
	}
	if m.opt.Config.UI.Layout != s {
		m.opt.Config.UI.Layout = s
		m.saveConfig()
	}
}

// paneKeyPressed handles the key after ctrl+w.
func (m *Model) paneKeyPressed(s string) tea.Cmd {
	h := m.bodyHeight()
	var err error
	switch s {
	case "v", "|", "%":
		err = m.splitPane(sideBySide, -1)
	case "s", "\"":
		err = m.splitPane(stacked, -1)
	case "q", "c", "x":
		err = m.closePane()
	case "left", "h":
		m.focusPane(m.lay.neighbour(-1, 0, m.w, h))
	case "right", "l":
		m.focusPane(m.lay.neighbour(1, 0, m.w, h))
	case "up", "k":
		m.focusPane(m.lay.neighbour(0, -1, m.w, h))
	case "down", "j":
		m.focusPane(m.lay.neighbour(0, 1, m.w, h))
	case "w", "ctrl+w", "tab":
		all := m.lay.leaves()
		for i, p := range all {
			if p == m.lay.focus {
				m.focusPane(all[(i+1)%len(all)])
				break
			}
		}
	case ">", "<", "+", "-", "=":
		dir, grow := sideBySide, s == ">"
		if s == "+" || s == "-" {
			dir, grow = stacked, s == "+"
		}
		if s == "=" {
			m.lay.even()
		} else if !m.lay.resize(dir, grow) {
			err = fmt.Errorf("nothing to resize that way")
		}
		m.saveLayout()
	case "z":
		if m.lay.split() {
			m.lay.zoomed = !m.lay.zoomed
			m.tabAt = m.now
		}
	}
	if err != nil {
		m.fail("PANES  " + err.Error())
	}
	return nil
}

func splitCommand() command {
	return command{name: "split", args: "right|down [screen]", help: "show another screen beside or below this one",
		run: func(m *Model, a []string) error {
			if len(a) == 0 {
				return fmt.Errorf("split needs right or down")
			}
			dir := map[string]splitDir{"right": sideBySide, "down": stacked}[a[0]]
			if dir == leaf {
				return fmt.Errorf("split %s? try right or down", a[0])
			}
			tab := -1
			if len(a) > 1 {
				for i, n := range tabNames {
					if strings.EqualFold(n, a[1]) {
						tab = i
					}
				}
				if tab < 0 {
					return fmt.Errorf("no screen called %q", a[1])
				}
			}
			return m.splitPane(dir, tab)
		},
		complete: func(*Model) []string {
			out := []string{"right", "down"}
			for _, n := range tabNames {
				out = append(out, strings.ToLower(n))
			}
			return out
		}}
}

// panes draws each screen in its own box; the focused one has heavy lines.
func (m *Model) panes(w, h int) []string {
	if !m.lay.split() || m.lay.zoomed {
		return m.screenBox(m.tab, w, h, true)
	}
	grid := make([]string, h)
	for _, pl := range m.lay.place(w, h) {
		box := m.screenBox(pl.p.tab, pl.r.w, pl.r.h, pl.p == m.lay.focus)
		for i := 0; i < pl.r.h && pl.r.y+i < h; i++ {
			line := ""
			if i < len(box) {
				line = box[i]
			}
			grid[pl.r.y+i] += fit(line, pl.r.w)
		}
	}
	return grid
}
