package ui

import (
	"fmt"
	"strings"
	"time"
)

type connectModel struct {
	ports    func() []Target
	saved    func() []Target
	items    []Target
	sel      int
	editing  bool
	input    string
	openedAt time.Time
}

func newConnect(ports, saved func() []Target) connectModel {
	c := connectModel{ports: ports, saved: saved}
	c.rescan()
	return c
}

// rescan lists saved devices first, then any other detected ports.
func (c *connectModel) rescan() {
	c.items = nil
	known := map[string]bool{}
	if c.saved != nil {
		for _, t := range c.saved() {
			c.items = append(c.items, t)
			known[t.Kind+t.Addr] = true
		}
	}
	if c.ports != nil {
		for _, t := range c.ports() {
			if !known[t.Kind+t.Addr] {
				c.items = append(c.items, t)
			}
		}
	}
	c.items = append(c.items,
		Target{Kind: "TCP", Detail: "WiFi companion · enter host[:port]"},
		Target{Kind: "SIM", Addr: "demo", Detail: "simulated radio on a Bristol mesh"},
	)
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
	var body []string
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
	hint := "↑↓ choose · enter connect · d forget saved · r rescan · T theme · q quit"
	if c.editing {
		hint = "type a host, then enter · esc cancel"
	}
	body = append(body, th.Fg(th.Dim, false).Render(hint))
	box := m.panel("SELECT LINK", body, width, len(body)+2, th.Magenta, true)
	block := append(m.logo(c.openedAt), "")
	block = append(block, box...)
	return centre(block, m.w, m.h)
}
