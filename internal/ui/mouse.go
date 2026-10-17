package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// click picks a screen from the tab row, or focuses the split pane clicked in.
func (m *Model) click(c tea.Mouse) tea.Cmd {
	if c.Button != tea.MouseLeft || m.phase != phaseMain || m.pal.open || m.help || m.search.open {
		return nil
	}
	if c.Y == 1 {
		i, ok := m.tabUnder(c.X)
		switch {
		case !ok:
			return nil
		case m.repeaterMode():
			return m.setPane(adminPane(i))
		}
		m.setTab(i)
	} else if y := c.Y - 2; y >= 0 && y < m.mainHeight(m.bodyHeight()) && m.lay.split() && !m.lay.zoomed {
		for _, pl := range m.lay.place(m.w, m.mainHeight(m.bodyHeight())) {
			if r := pl.r; c.X >= r.x && c.X < r.x+r.w && y >= r.y && y < r.y+r.h {
				m.focusPane(pl.p)
			}
		}
	}
	if m.tab == 1 && !m.repeaterMode() {
		return m.onSelectContact()
	}
	return nil
}

// tabUnder is the screen whose label is at column x of the tab row, laid out as tabs draws it.
func (m *Model) tabUnder(x int) (int, bool) {
	names := tabNames
	if m.repeaterMode() {
		names = paneNames
	}
	at := 1
	for i, n := range names {
		w := len(fmt.Sprintf("%d %s", i+1, strings.ToUpper(n)))
		if x >= at && x < at+w {
			return i, true
		}
		at += w + visibleWidth(tabGap)
	}
	return 0, false
}
