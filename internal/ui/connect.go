package ui

import (
	"fmt"
	"strings"
	"time"

	"meshcore-tui/internal/motion"
)

type connectModel struct {
	ports    func() []Target
	saved    func() []Target
	items    []Target
	sel      int
	mode     string // "" for a companion radio, or ModeRepeater
	modeAt   time.Time
	editing  bool
	input    string
	openedAt time.Time
}

// newConnect starts in the mode of the device used last.
func newConnect(ports, saved func() []Target) connectModel {
	c := connectModel{ports: ports, saved: saved}
	if saved != nil {
		if s := saved(); len(s) > 0 {
			c.mode = s[0].Mode
		}
	}
	c.rescan()
	return c
}

func (c *connectModel) setMode(mode string) {
	if mode != c.mode {
		c.mode, c.sel = mode, 0
		c.rescan()
	}
}

// rescan lists the mode's saved devices first, then any other detected ports.
func (c *connectModel) rescan() {
	c.items = nil
	known := map[string]bool{}
	if c.saved != nil {
		for _, t := range c.saved() {
			if t.Mode == c.mode && (c.mode == "" || t.Kind == "USB") {
				c.items = append(c.items, t)
				known[t.Kind+t.Addr] = true
			}
		}
	}
	if c.ports != nil {
		for _, t := range c.ports() {
			if !known[t.Kind+t.Addr] {
				t.Mode = c.mode
				c.items = append(c.items, t)
			}
		}
	}
	if c.mode == ModeRepeater {
		c.items = append(c.items, Target{Kind: "SIM", Addr: "demo", Detail: "simulated repeater on its USB console", Mode: ModeRepeater})
	} else {
		c.items = append(c.items,
			Target{Kind: "TCP", Detail: "WiFi companion · enter host[:port]"},
			Target{Kind: "SIM", Addr: "demo", Detail: "simulated radio on a Bristol mesh"},
		)
	}
	c.sel = min(c.sel, len(c.items)-1)
}

// key handles the connect screen; it returns the chosen target once one is picked.
func (c *connectModel) key(s string, now time.Time, forget func(Target)) (Target, bool, bool) {
	if c.editing {
		switch s {
		case "esc":
			c.editing = false
		case "enter":
			if strings.TrimSpace(c.input) != "" {
				return Target{Kind: "TCP", Addr: strings.TrimSpace(c.input)}, true, false
			}
		case "backspace":
			if r := []rune(c.input); len(r) > 0 {
				c.input = string(r[:len(r)-1])
			}
		case "space":
		default:
			if len([]rune(s)) == 1 {
				c.input += s
			}
		}
		return Target{}, false, false
	}
	switch s {
	case "q", "esc":
		return Target{}, false, true
	case "up", "k":
		c.sel = (c.sel + len(c.items) - 1) % len(c.items)
	case "down", "j":
		c.sel = (c.sel + 1) % len(c.items)
	case "r":
		c.rescan()
	case "m", "left", "right", "h", "l":
		c.modeAt = now
		if c.mode == ModeRepeater {
			c.setMode("")
		} else {
			c.setMode(ModeRepeater)
		}
	case "d":
		if t := c.items[c.sel]; t.Saved != "" {
			forget(t)
			c.rescan()
		}
	case "enter":
		t := c.items[c.sel]
		if t.Kind == "TCP" {
			c.editing = true
			return Target{}, false, false
		}
		return t, true, false
	}
	return Target{}, false, false
}

func (m *Model) renderConnect() []string {
	th, c := m.th, &m.connect
	width := min(max(m.w-8, 44), 84)
	inner := width - 4
	body := append([]string{""}, m.modeSwitch(inner)...)
	body = append(body, "")
	for i, t := range c.items {
		kind := fmt.Sprintf("%-3s", t.Kind)
		addr := t.Addr
		if t.Kind == "TCP" {
			addr = "host:port"
			if c.editing && i == c.sel {
				addr = c.input + "█"
			}
		}
		mark := " "
		if t.Saved != "" {
			mark = "★"
		}
		line := fmt.Sprintf("%s%s  %-22s %s", mark, kind, truncate(addr, 22), t.Detail)
		line = truncate(line, inner-2)
		if i == c.sel {
			body = append(body, th.Fg(th.Magenta, true).Render("▌")+m.raised(fit(line, inner-1)))
			continue
		}
		body = append(body, " "+th.Fg(th.Muted, false).Render(line))
	}
	body = append(body, "")
	hint := "↑↓ choose · enter connect · m mode · d forget · r rescan · T theme · q quit"
	if c.editing {
		hint = "type a host, then enter · esc cancel"
	}
	body = append(body, th.Fg(th.Dim, false).Render(hint))
	box := m.panel("SELECT LINK", body, width, len(body)+2, th.Magenta, true)
	block := append(m.logo(c.openedAt), "")
	block = append(block, box...)
	return centre(block, m.w, m.h)
}

var modeHelp = map[string]string{
	"":           "message, map and watch the mesh through your own radio",
	ModeRepeater: "manage a repeater or room server plugged in by USB, through its own console",
}

// modeSwitch shows the two modes with the chosen one lit; a new choice decodes into place.
func (m *Model) modeSwitch(w int) []string {
	th, c := m.th, &m.connect
	part := func(label, mode string) string {
		if c.mode != mode {
			return th.Fg(th.Muted, false).Render(" " + label + " ")
		}
		if m.level.Allows(motion.Decode) && motion.Running(c.modeAt, wipeDur*2, m.now) {
			label = motion.DecodeText(label, 0x30de, motion.Progress(c.modeAt, wipeDur*2, m.now))
		}
		return th.Fg(th.Magenta, true).Render("▌") + m.raised(label) + th.Fg(th.Magenta, true).Render("▐")
	}
	line := th.Fg(th.Dim, true).Render(" MODE  ") + part("COMPANION", "") + "  " + part("REPEATER ADMIN", ModeRepeater)
	return []string{line, th.Fg(th.Dim, false).Render("       " + truncate(modeHelp[c.mode], w-8))}
}
