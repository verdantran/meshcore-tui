package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"

	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/theme"
)

// typing reports whether a text field has the keyboard, so typed and pasted text goes into it rather than
// being taken as commands.
func (m *Model) typing() bool {
	switch {
	case m.phase == phaseConnect:
		return m.connect.editing
	case m.pal.open || m.search.open:
		return true
	case m.phase != phaseMain:
		return false
	}
	switch m.tab {
	case 0:
		c := &m.chats
		return c.writing || c.filtering || c.chanStage != chanNone
	case 1:
		return m.contacts.filtering || m.contacts.overlay == importOverlay
	case 3:
		return m.inspector.filtering
	case 4:
		a := &m.adm
		switch a.prompt {
		case promptPassword, promptEdit, promptConfirm, promptRegion:
			return true
		}
		return a.opened && a.pane == paneConsole
	case 5:
		return m.device.prompt == devEdit || m.device.prompt == devConfirm
	}
	return false
}

// typeText puts text into the field with the keyboard a character at a time, as if typed. That lets emoji
// built from several characters (flags, skin tones, ❤️) through fields that take one character per key.
// Line breaks and tabs become spaces, so a paste never sends a message by itself.
func (m *Model) typeText(text string) tea.Cmd {
	var cmds []tea.Cmd
	for _, r := range text {
		k := tea.KeyPressMsg{Code: r, Text: string(r)}
		switch {
		case r == ' ' || r == '\n' || r == '\r' || r == '\t':
			k = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		case unicode.IsControl(r) || r == utf8.RuneError:
			continue
		}
		cmd, _ := m.key(k)
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// paste puts pasted text into the field with the keyboard. In Chats, pasting with nowhere to type starts a
// message with it.
func (m *Model) paste(text string) tea.Cmd {
	if !m.typing() && m.phase == phaseMain && m.tab == 0 && len(m.chats.infos) > 0 && m.chats.chanStage == chanNone {
		m.chats.reading, m.chats.writing = false, true
	}
	if !m.typing() {
		m.addLog(m.now, "PASTE  there's nowhere to type here", radio.ToneInfo)
		return nil
	}
	return m.typeText(text)
}

// dropLast removes the last character as it's seen, for backspace: a whole emoji, flag or accented letter,
// however many code points it's made of.
func dropLast(s string) string {
	last := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		last, _ = g.Positions()
	}
	return s[:last]
}

// lineEdit is a line of typed text with a cursor that moves a whole character at a time.
type lineEdit struct {
	text string
	pos  int // byte offset into text
}

func newLine(s string) lineEdit { return lineEdit{text: s, pos: len(s)} }

func (e *lineEdit) set(s string) { *e = newLine(s) }

func (e *lineEdit) insert(s string) {
	e.text = e.text[:e.pos] + s + e.text[e.pos:]
	e.pos += len(s)
}

// replace swaps text[from:e.pos] for s, leaving the cursor after it.
func (e *lineEdit) replace(from int, s string) {
	e.text = e.text[:from] + s + e.text[e.pos:]
	e.pos = from + len(s)
}

func (e lineEdit) before() string { return e.text[:e.pos] }

// key applies an editing key, reporting whether it was one.
func (e *lineEdit) key(s string) bool {
	switch s {
	case "left":
		e.pos = prevBoundary(e.text, e.pos)
	case "right":
		e.pos = nextBoundary(e.text, e.pos)
	case "alt+left", "ctrl+left", "alt+b":
		e.pos = wordStart(e.text, e.pos)
	case "alt+right", "ctrl+right", "alt+f":
		e.pos = wordEnd(e.text, e.pos)
	case "home", "ctrl+a":
		e.pos = 0
	case "end", "ctrl+e":
		e.pos = len(e.text)
	case "backspace", "ctrl+h":
		e.replace(prevBoundary(e.text, e.pos), "")
	case "delete", "ctrl+d":
		end := nextBoundary(e.text, e.pos)
		e.text = e.text[:e.pos] + e.text[end:]
	case "alt+backspace", "ctrl+backspace":
		e.replace(wordStart(e.text, e.pos), "")
	case "ctrl+u":
		e.replace(0, "")
	case "ctrl+k":
		e.text = e.text[:e.pos]
	case "space":
		e.insert(" ")
	default:
		if len([]rune(s)) != 1 {
			return false
		}
		e.insert(s)
	}
	return true
}

// boundaries lists where each character starts, and the end.
func boundaries(s string) []int {
	out := []int{}
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		from, _ := g.Positions()
		out = append(out, from)
	}
	return append(out, len(s))
}

func prevBoundary(s string, pos int) int {
	prev := 0
	for _, b := range boundaries(s) {
		if b >= pos {
			break
		}
		prev = b
	}
	return prev
}

func nextBoundary(s string, pos int) int {
	for _, b := range boundaries(s) {
		if b > pos {
			return b
		}
	}
	return len(s)
}

func wordStart(s string, pos int) int {
	i := len(strings.TrimRight(s[:pos], " "))
	return strings.LastIndex(s[:i], " ") + 1
}

func wordEnd(s string, pos int) int {
	rest := strings.TrimLeft(s[pos:], " ")
	i := len(s) - len(rest)
	if j := strings.Index(rest, " "); j >= 0 {
		return i + j
	}
	return len(s)
}

// render draws the text with the cursor, scrolled to keep the cursor in view within w cells. mask hides
// each character, for passwords.
func (e lineEdit) render(th theme.Theme, w int, mask bool) string {
	var cells []string
	at := -1
	g := uniseg.NewGraphemes(e.text)
	for g.Next() {
		if from, _ := g.Positions(); from >= e.pos && at < 0 {
			at = len(cells)
		}
		if mask {
			cells = append(cells, "•")
		} else {
			cells = append(cells, g.Str())
		}
	}
	if at < 0 {
		at = len(cells)
	}
	cur := " "
	if at < len(cells) {
		cur = cells[at]
	}
	width := func(cs []string) int { return visibleWidth(strings.Join(cs, "")) }
	before, after := cells[:at], cells[min(at+1, len(cells)):]
	w = max(w-visibleWidth(cur), 2)
	lead := ""
	for width(before) > w-1 && len(before) > 0 {
		before, lead = before[1:], "…"
	}
	for width(after) > w-width(before)-len(lead) && len(after) > 0 {
		after = after[:len(after)-1]
	}
	text := th.Fg(th.Text, false)
	return th.Fg(th.Dim, false).Render(lead) + text.Render(strings.Join(before, "")) +
		th.Block(th.Bg, th.Text).Render(cur) + text.Render(strings.Join(after, ""))
}
