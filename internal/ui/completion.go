package ui

import (
	"fmt"
	"strings"

	"github.com/verdantran/meshcore-tui/internal/console"
)

// completer is tab completion in a console: what console.Suggest offers for the line, and where tab is in
// stepping through the matches.
type completer struct {
	base string // the line tab started stepping from; empty when not stepping
	at   int
}

// tab adds the ghost, or steps through the matches when they part ways; back steps the other way.
func (c *completer) tab(cmds []console.Command, e *lineEdit, back bool) {
	if c.base == "" || e.text != c.current(cmds) {
		c.base = ""
		h := console.Suggest(cmds, e.text)
		if h.Ghost != "" && !back {
			line := e.text + h.Ghost
			words := strings.ToLower(strings.TrimSpace(line))
			if console.Takes(cmds, words) && len(console.Suggest(cmds, line).Matches) < 2 {
				line += " " // straight on to its argument
			}
			e.set(line)
			return
		}
		if len(h.Matches) < 2 {
			return
		}
		c.base, c.at = e.text, -1
		for i, m := range h.Matches {
			if m == strings.ToLower(strings.TrimSpace(e.text)) {
				c.at = i
			}
		}
		if c.at < 0 && back {
			c.at = 0
		}
	}
	ms := console.Suggest(cmds, c.base).Matches
	if back {
		c.at = (c.at - 1 + len(ms)) % len(ms)
	} else {
		c.at = (c.at + 1) % len(ms)
	}
	e.set(ms[c.at])
}

func (c *completer) current(cmds []console.Command) string {
	ms := console.Suggest(cmds, c.base).Matches
	if c.at < 0 || c.at >= len(ms) {
		return ""
	}
	return ms[c.at]
}

func (c *completer) reset() { c.base = "" }

// consolePrompt draws a console's prompt and, under it, what console.Suggest offers: the matches, with the
// one tab has reached raised, or what the command typed does.
func (m *Model) consolePrompt(prompt string, e lineEdit, cmds []console.Command, c completer, wait string, w int) []string {
	th := m.th
	h := console.Suggest(cmds, e.text)
	if c.base != "" {
		h = console.Suggest(cmds, c.base) // keep showing the matches while tab steps through them
		h.Ghost, h.Args = "", ""
	}
	room := w - visibleWidth(prompt)
	if wait != "" {
		room -= visibleWidth(wait) + 2
	}
	line := prompt + e.renderGhost(th, room, h.Ghost, h.Args)
	if wait != "" {
		line += "  " + wait
	}
	var under string
	switch {
	case len(h.Matches) > 1 && h.Args == "": // with the choices already after the cursor, say what it does instead
		reached := c.current(cmds)
		var parts []string
		used := 0
		for i, mt := range h.Matches {
			tail := 0
			if i < len(h.Matches)-1 {
				tail = 10 // room for "+N more"
			}
			if i > 0 && used+visibleWidth(mt)+tail > w {
				parts = append(parts, th.Paint(th.Dim, false, fmt.Sprintf("+%d more", len(h.Matches)-i)))
				break
			}
			cell := th.Paint(th.Muted, false, mt)
			if mt == reached {
				cell = m.raised(mt)
			}
			parts = append(parts, cell)
			used += visibleWidth(mt) + 3
		}
		under = strings.Join(parts, th.Paint(th.Dim, false, " · "))
	case h.Help != "":
		under = th.Paint(th.Dim, false, h.Help)
	}
	return []string{clip(line, w), clip(under, w)}
}
