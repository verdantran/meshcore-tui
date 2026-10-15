package ui

import (
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"

	"meshcore-tui/internal/radio"
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
