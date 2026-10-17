package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
)

func (m *Model) deviceTitle() string {
	dm := &m.device
	if !dm.loaded {
		return "DEVICE"
	}
	return "DEVICE · " + dm.s.Self.Name + " · " + m.ready.Device.Model + " · fw " + m.ready.Device.Firmware
}

func (m *Model) renderDevice(w, rows int) []string {
	th, dm := m.th, &m.device
	if m.dev() == nil {
		return []string{th.Fg(th.Dim, false).Render("A repeater's own console has no companion settings. Its settings are on the Admin screen.")}
	}
	if !dm.loaded {
		if dm.err != nil {
			return []string{th.Fg(th.Red, false).Render("Couldn't read the settings: " + radio.Explain(dm.err)), "", th.Fg(th.Dim, false).Render("u tries again")}
		}
		return []string{m.waiting("reading the radio's settings…")}
	}
	switch dm.prompt {
	case devEdit:
		return m.devEditLines(w)
	case devPresets:
		return m.presetLines(w)
	case devConfirm:
		return m.devConfirmLines(w)
	case devShowKey:
		return m.keyLines(w)
	}
	out := []string{m.deviceStatus(w), ""}
	list := m.deviceRows()
	dm.sel = min(dm.sel, len(list)-1)
	type line struct {
		group string
		i     int
	}
	var lines []line
	group := ""
	for i, r := range list {
		if r.group != group {
			group = r.group
			lines = append(lines, line{group: group, i: -1})
		}
		lines = append(lines, line{i: i})
	}
	selLine := 0
	for n, l := range lines {
		if l.i == dm.sel {
			selLine = n
		}
	}
	room := rows - len(out) - 3
	start := min(max(selLine-room/2, 0), max(len(lines)-room, 0))
	labelW := min(max(w/3, 22), 34)
	for n := start; n < len(lines) && len(out) < rows-3; n++ {
		l := lines[n]
		if l.i < 0 {
			out = append(out, th.Fg(th.Muted, true).Render(strings.ToUpper(l.group)))
			continue
		}
		r := list[l.i]
		out = append(out, m.devRowLine(r, l.i == dm.sel, labelW, w))
	}
	out = append(out, "")
	hint := list[dm.sel].hint
	if hint == "" {
		hint = map[rowKind]string{rowEdit: "enter edits", rowToggle: "enter changes it", rowAction: "enter"}[list[dm.sel].kind]
	}
	out = append(out, th.Fg(th.Dim, false).Render(clip(hint, w)))
	for len(out) < rows-1 {
		out = append(out, "")
	}
	return append(out, th.Fg(th.Dim, false).Render(clip("↑↓ choose · enter edit, toggle or run · u read again · changes go to the radio straight away", w)))
}

func (m *Model) devRowLine(r devRow, sel bool, labelW, w int) string {
	th, dm, now := m.th, &m.device, m.now
	value := th.Fg(th.Text, false).Render(r.value)
	switch r.kind {
	case rowToggle:
		col := th.Cyan
		if r.value == "off" || r.value == "nobody" {
			col = th.Dim
		}
		value = th.Fg(col, true).Render(r.value)
	case rowAction:
		value = th.Fg(th.Cyan, false).Render(r.value)
	}
	switch {
	case !dm.busy[r.id].IsZero():
		value = th.Fg(th.Yellow, false).Render(motion.RampBar(now, 3) + " " + ansi.Strip(value))
	case m.level.Allows(motion.Flash) && motion.Running(dm.doneAt[r.id], lampDur*2, now):
		value = th.Block(th.Bg, th.Green).Render(" "+ansi.Strip(value)+" ") + th.Fg(th.Green, true).Render(" ✓")
	case motion.Running(dm.failedAt[r.id], lampDur*4, now):
		value += th.Fg(th.Red, true).Render("  [FAIL]")
	}
	name := fmt.Sprintf("  %-*s", labelW, clip(r.label, labelW))
	if sel {
		return clip(th.Fg(th.Magenta, true).Render("▌")+m.raised(name[1:])+" "+value, w)
	}
	return clip(th.Fg(th.Text, false).Render(name)+" "+value, w)
}

// deviceStatus is the battery and storage line at the top.
func (m *Model) deviceStatus(w int) string {
	th, dm := m.th, &m.device
	b := dm.storage.Battery
	parts := th.Fg(th.Dim, false).Render("Battery ") + m.batteryText(m.ready.Self.PubKey, b, true)
	if dm.storage.TotalKB > 0 {
		pct := float64(dm.storage.UsedKB) / float64(dm.storage.TotalKB)
		bar := int(pct*10 + 0.5)
		parts += th.Fg(th.Dim, false).Render("   Storage ") + th.Fg(th.Cyan, false).Render(strings.Repeat("█", bar)+strings.Repeat("░", 10-bar)) +
			th.Fg(th.Text, false).Render(fmt.Sprintf(" %d of %d KB", dm.storage.UsedKB, dm.storage.TotalKB))
	}
	parts += th.Fg(th.Dim, false).Render("   Key ") + th.Fg(th.Muted, false).Render(radio.KeyHex(dm.s.Self.PubKey[:4]))
	return clip(parts, w)
}

func (m *Model) devEditLines(w int) []string {
	th, dm := m.th, &m.device
	rows := m.deviceRows()
	r := rows[dm.sel]
	out := []string{th.Fg(th.Muted, true).Render("EDIT " + strings.ToUpper(r.label)), "",
		th.Fg(th.Magenta, true).Render("▸ ") + dm.input.render(th, w-2, r.id == "import"), ""} // a private key never shows on screen
	if r.hint != "" {
		for _, l := range strings.Split(ansi.Wrap(r.hint, w, " "), "\n") {
			out = append(out, th.Fg(th.Dim, false).Render(l))
		}
	}
	return append(out, "", th.Fg(th.Dim, false).Render("enter sends it to the radio · esc cancels"))
}

func (m *Model) presetLines(w int) []string {
	th, dm := m.th, &m.device
	out := []string{th.Fg(th.Muted, true).Render("RADIO PRESETS") + th.Fg(th.Dim, false).Render("  ·  ↑↓ choose · enter switch · esc cancel"), ""}
	cur := presetName(dm.s.Radio)
	for i, p := range presets {
		mark := "  "
		if p.Name == cur {
			mark = th.Fg(th.Green, true).Render("● ")
		}
		line := fmt.Sprintf("%-38s %s", p.Name, paramsText(p.Params))
		if i == dm.pick {
			out = append(out, clip(th.Fg(th.Magenta, true).Render("▌")+mark+m.raised(line), w))
			continue
		}
		out = append(out, clip(" "+mark+th.Fg(th.Text, false).Render(line), w))
	}
	return append(out, "", th.Fg(th.Dim, false).Render(clip("Use what your local mesh uses; ask on its channel if you're not sure.", w)))
}

func (m *Model) devConfirmLines(w int) []string {
	th, c := m.th, m.device.confirm
	out := []string{th.Fg(th.Red, true).Render(c.title), ""}
	for _, l := range strings.Split(ansi.Wrap(c.warning, w, " "), "\n") {
		out = append(out, th.Fg(th.Yellow, false).Render(l))
	}
	out = append(out, "")
	if c.word == "" {
		return append(out, th.Fg(th.Text, false).Render("y goes ahead · any other key cancels"))
	}
	return append(out, th.Fg(th.Text, false).Render("Type "+c.word+" to go ahead:"),
		th.Fg(th.Magenta, true).Render("▸ ")+m.device.input.render(th, w-2, false), "",
		th.Fg(th.Dim, false).Render("esc cancels"))
}

func (m *Model) keyLines(w int) []string {
	th, dm := m.th, &m.device
	out := []string{th.Fg(th.Red, true).Render("PRIVATE KEY") + th.Fg(th.Dim, false).Render("  ·  c copies · any key hides it"), ""}
	if dm.key == "" {
		return append(out, m.waiting("reading it from the radio…"))
	}
	for _, l := range strings.Split(ansi.Hardwrap(dm.key, min(w, 64), false), "\n") {
		out = append(out, th.Fg(th.Yellow, true).Render(l))
	}
	return append(out, "", th.Fg(th.Dim, false).Render("Keep it somewhere safe. It isn't written to the log or saved by this app."))
}
