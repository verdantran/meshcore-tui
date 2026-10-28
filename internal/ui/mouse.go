package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// click picks a screen from the tab row, or focuses the split pane clicked in.
func (m *Model) click(c tea.Mouse) tea.Cmd {
	if c.Button != tea.MouseLeft || m.phase != phaseMain || m.overlayUp() {
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
	} else if y := c.Y - 2; y >= 0 && y < m.mainHeight(m.bodyHeight()) {
		r := rect{0, 0, m.w, m.mainHeight(m.bodyHeight())}
		if m.lay.split() && !m.lay.zoomed {
			for _, pl := range m.lay.place(r.w, r.h) {
				if pr := pl.r; c.X >= pr.x && c.X < pr.x+pr.w && y >= pr.y && y < pr.y+pr.h {
					m.focusPane(pl.p)
					r = pr
				}
			}
		}
		if m.tab == 4 && y == r.y+1 {
			if p, ok := m.paneUnder(c.X - r.x - 2); ok {
				return m.setPane(p)
			}
		}
		if b := m.chats.board; m.tab == 0 && b != nil {
			// The chats screen's content starts inside the box's edge; the board is in the pane right of the list.
			listW := min(max((r.w-4)/4, 20), 32)
			if sq, ok := b.boardSquareAt(c.X-r.x-2-listW-3, y-r.y-1); ok {
				b.chat = false
				return m.pressSquare(sq)
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

// paneUnder is the Admin pane whose name is at column x of the open node's pane bar, laid out as paneBar draws it.
func (m *Model) paneUnder(x int) (adminPane, bool) {
	if !m.adm.opened || m.adm.local || m.adm.prompt != noPrompt {
		return 0, false
	}
	at := 0
	for i, n := range paneNames {
		w := len(n) + 2
		if x >= at && x < at+w {
			return adminPane(i), true
		}
		at += w + 1
	}
	return 0, false
}

// wheel scrolls what's under the keyboard: history in Chats, the zoom on the map, the list elsewhere.
func (m *Model) wheel(c tea.Mouse) tea.Cmd {
	up := c.Button == tea.MouseWheelUp
	if !up && c.Button != tea.MouseWheelDown || m.phase != phaseMain {
		return nil
	}
	if m.help {
		m.helpKey(map[bool]string{true: "k", false: "j"}[up])
		return nil
	}
	if m.overlayUp() {
		return nil
	}
	k := map[bool]tea.KeyPressMsg{true: {Code: tea.KeyUp}, false: {Code: tea.KeyDown}}[up]
	switch {
	case m.tab == 0 && !m.chats.reading:
		k = map[bool]tea.KeyPressMsg{true: {Code: tea.KeyPgUp}, false: {Code: tea.KeyPgDown}}[up]
	case m.tab == 2 && !m.repeaterMode():
		k = map[bool]tea.KeyPressMsg{true: {Code: '+', Text: "+"}, false: {Code: '-', Text: "-"}}[up]
	}
	cmd, _ := m.key(k)
	return cmd
}

// overlayUp reports a prompt, picker or text field on top of the screen, which the mouse leaves alone.
func (m *Model) overlayUp() bool {
	switch {
	case m.pal.open, m.help, m.search.open, m.asking != nil, m.paneKey, m.typing():
		return true
	case m.tab == 0:
		return m.chats.chanStage != chanNone
	case m.tab == 1:
		return m.contacts.overlay != noOverlay
	case m.tab == 4:
		return m.adm.prompt != noPrompt
	case m.tab == 5:
		return m.device.prompt != devNone
	}
	return false
}
