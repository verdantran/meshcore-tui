package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/verdantran/meshcore-tui/internal/mapview"
)

// mapToggle is one line of the map's show menu: something drawn that can be switched off.
type mapToggle struct {
	label, key string // key is its own shortcut on the map, if it has one
	on         bool
	flip       func()
}

func (m *Model) mapToggles() []mapToggle {
	l := &m.mapv.layers
	kind := func(label string, k mapview.Kind) mapToggle {
		return mapToggle{label: label, on: !l.Hidden[k], flip: func() { l.Hidden[k] = !l.Hidden[k] }}
	}
	return []mapToggle{
		kind("▲ repeaters", mapview.KindRepeater),
		kind("● companions", mapview.KindChat),
		kind("■ room servers", mapview.KindRoom),
		kind("● sensors and others", mapview.KindOther),
		{label: "recent routes", on: l.Routes, flip: func() { l.Routes = !l.Routes }},
		{label: "traffic", key: "t", on: l.Traffic, flip: func() {
			l.Traffic, m.mapv.window, m.mapv.hub = !l.Traffic, 0, ""
		}},
		{label: "names", key: "n", on: l.Labels, flip: func() { l.Labels = !l.Labels }},
		{label: "roads", key: "r", on: l.Roads, flip: func() { l.Roads = !l.Roads }},
	}
}

// mapFiltered reports anything switched off in the show menu that's on by default.
func (m *Model) mapFiltered() bool {
	l := m.mapv.layers
	return l.Hidden != [mapview.KindOther + 1]bool{} || !l.Routes
}

// showKey handles the show menu while it's open, reporting whether it took the key.
func (m *Model) showKey(s string) bool {
	mv := &m.mapv
	items := m.mapToggles()
	switch s {
	case "up", "k":
		mv.showSel = (mv.showSel + len(items) - 1) % len(items)
	case "down", "j":
		mv.showSel = (mv.showSel + 1) % len(items)
	case "space", "enter":
		items[mv.showSel].flip()
	case "esc":
		mv.showing = false
	default:
		return false
	}
	return true
}

// overlayShow lays the show menu over the top right of the map.
func (m *Model) overlayShow(rows []string, w int) {
	th := m.th
	const inner = 26
	if w < inner+6 || len(rows) < 12 {
		return
	}
	edge := th.Fg(th.Line, false)
	box := []string{edge.Render("┌─") + th.Fg(th.Muted, true).Render(" SHOW ") + edge.Render(strings.Repeat("─", inner-7)+"┐")}
	for i, t := range m.mapToggles() {
		mark := "[ ]"
		if t.on {
			mark = "[x]"
		}
		line := " " + mark + " " + t.label
		if t.key != "" {
			line = fit(line, inner-2) + t.key
		}
		cell := th.Block(th.Text, th.Panel).Render(fit(line, inner))
		if !t.on {
			cell = th.Block(th.Dim, th.Panel).Render(fit(line, inner))
		}
		if i == m.mapv.showSel {
			cell = m.raised(fit(line, inner))
		}
		box = append(box, edge.Render("│")+cell+edge.Render("│"))
	}
	box = append(box, edge.Render("└")+th.Fg(th.Dim, false).Render(fit(" space toggle · esc close", inner))+edge.Render("┘"))
	x := w - inner - 3
	for i, b := range box {
		row := i + 1
		rows[row] = ansi.Truncate(rows[row], x, "") + b + ansi.TruncateLeft(rows[row], x+inner+2, "")
	}
}
