package ui

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/theme"
)

func (m *Model) View() tea.View {
	var lines []string
	if m.w > 0 && m.h > 0 {
		switch m.phase {
		case phaseConnect:
			lines = m.renderConnect()
		case phaseBoot:
			lines = m.renderBoot()
		default:
			lines = m.renderMain()
		}
	}
	for i := range lines {
		lines[i] = fit(lines[i], m.w)
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.ReportFocus = true
	if !m.th.Mono {
		v.BackgroundColor = m.th.Bg.Color()
	}
	v.WindowTitle = "meshcore-tui"
	if m.ready.Self.Name != "" {
		v.WindowTitle = "meshcore-tui · " + m.ready.Self.Name
	}
	return v
}

// Layout helpers. Every rendered row is fitted to the terminal width exactly.

func visibleWidth(s string) int { return lipgloss.Width(s) }

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:max(n-1, 0)]) + "…"
}

func fit(s string, w int) string {
	if vw := visibleWidth(s); vw > w {
		return ansi.Truncate(s, w, "")
	} else if vw < w {
		return s + strings.Repeat(" ", w-vw)
	}
	return s
}

func centre(block []string, w, h int) []string {
	bw := 0
	for _, l := range block {
		bw = max(bw, visibleWidth(l))
	}
	left := strings.Repeat(" ", max((w-bw)/2, 0))
	out := make([]string, 0, h)
	for i := 0; i < max((h-len(block))/2, 0); i++ {
		out = append(out, "")
	}
	for _, l := range block {
		out = append(out, left+l)
	}
	return out
}

func (m *Model) raised(s string) string {
	if m.th.Mono {
		return lipgloss.NewStyle().Reverse(true).Render(s)
	}
	return lipgloss.NewStyle().Foreground(m.th.Text.Color()).Background(m.th.Raised.Color()).Bold(true).Render(s)
}

// panel draws a titled box. Focused panels use heavy lines in the accent colour.
func (m *Model) panel(title string, body []string, w, h int, accent theme.RGB, focused bool) []string {
	th := m.th
	tl, tr, bl, br, hz, vt := "┌", "┐", "└", "┘", "─", "│"
	edge := th.Line
	if focused {
		tl, tr, bl, br, hz, vt = "┏", "┓", "┗", "┛", "━", "┃"
		edge = accent
	}
	e := th.Fg(edge, false)
	head := th.Fg(accent, true).Render("▌") + th.Fg(th.Muted, true).Render(title+" ")
	top := e.Render(tl+hz) + head + e.Render(strings.Repeat(hz, max(w-4-visibleWidth(head), 0))+hz+tr)
	out := []string{top}
	inner := w - 4
	// A row must never contain a line break, or the panel would draw taller than it thinks.
	var rows []string
	for _, b := range body {
		rows = append(rows, strings.Split(b, "\n")...)
	}
	body = rows
	for i := 0; i < h-2; i++ {
		content := ""
		if i < len(body) {
			content = body[i]
		}
		out = append(out, e.Render(vt)+" "+fit(content, inner)+" "+e.Render(vt))
	}
	out = append(out, e.Render(bl+strings.Repeat(hz, max(w-2, 0))+br))
	return out
}

// Main screen.

const footerRows = 4

func (m *Model) bodyHeight() int { return max(m.h-2-footerRows, 3) }

func (m *Model) renderMain() []string {
	var out []string
	out = append(out, m.header(), m.tabs())
	body := m.body(m.w, m.bodyHeight())
	out = append(out, body...)
	out = append(out, m.footer()...)
	if m.level.Allows(motion.Wipe) && motion.Running(m.mainAt, slideDur, m.now) {
		// The header slides in once the link is up.
		out[0] = wipe(out[0], m.w, motion.EaseOut(motion.Progress(m.mainAt, slideDur, m.now)), m.th)
	}
	return out
}

// wipe reveals a line left to right behind a shaded edge.
func wipe(line string, w int, t float64, th theme.Theme) string {
	x := int(t * float64(w))
	if x >= w {
		return line
	}
	edge := th.Fg(th.Cyan, false).Render("░▒▓█")
	return ansi.Truncate(line, max(x-4, 0), "") + edge
}

func (m *Model) header() string {
	th, now, r := m.th, m.now, m.ready
	panelBg := lipgloss.NewStyle()
	if !th.Mono {
		panelBg = panelBg.Background(th.Panel.Color())
	}
	seg := func(s string, c theme.RGB, bold bool) string {
		st := th.Fg(c, bold)
		if !th.Mono {
			st = st.Background(th.Panel.Color())
		}
		return st.Render(s)
	}
	name := r.Self.Name
	if m.level.Allows(motion.Decode) {
		name = motion.DecodeText(name, 0xbeef, motion.Progress(m.mainAt, slideDur*2, now))
	}
	tag := th.Block(th.Bg, th.Magenta).Render(" " + name + " ")
	// A lamp lights as a solid block, holds, then fades back into the header.
	lamp := func(label string, at time.Time, on theme.RGB) string {
		text := " " + label + " ● "
		if !motion.Running(at, headerLampDur, now) {
			return seg(text, th.Dim, false)
		}
		if !m.level.Allows(motion.Flash) {
			return th.Block(th.Bg, on).Render(text)
		}
		fade := max(motion.Progress(at, headerLampDur, now)-0.3, 0) / 0.7
		return th.Block(th.Bg.Lerp(th.Dim, fade), on.Lerp(th.Panel, fade)).Render(text)
	}
	batt := radio.Battery{MilliVolts: uint16(m.stats.currentBatt(now))}
	if batt.MilliVolts == 0 {
		batt = r.Battery
	}
	state := seg(" ● LINKED", th.Green, true)
	switch m.linked {
	case linkLost:
		state = seg(" "+string(motion.RampRune(now))+" RECONNECTING", th.Yellow, true)
	case linkDown:
		state = seg(" ✕ DOWN", th.Red, true)
	}
	// Details in order of importance; the least important drop first when the terminal is narrow.
	details := []string{
		seg(fmt.Sprintf("%.2fV ", float64(batt.MilliVolts)/1000), th.Cyan, true) + batteryIcon(th, batt.Percent(), seg) + seg(fmt.Sprintf(" %d%%", batt.Percent()), th.Cyan, true),
		seg(r.Self.Params(), th.Cyan, true),
		seg(r.Device.Model, th.Muted, false),
		seg("fw "+r.Device.Firmware, th.Muted, false),
		seg(m.radio.Transport()+" "+m.radio.Describe(), th.Muted, false),
	}
	order := []int{2, 3, 1, 0, 4} // display order: model, firmware, params, battery, link
	dot := seg(" · ", th.Dim, false)
	base := tag + lamp("RX", m.stats.rxLamp, th.Cyan) + lamp("TX", m.stats.txLamp, th.Magenta)
	var line string
	for keep := len(details); keep >= 0; keep-- {
		var parts []string
		for _, i := range order {
			if i < keep {
				parts = append(parts, details[i])
			}
		}
		line = base
		if len(parts) > 0 {
			line += seg(" ", th.Dim, false) + strings.Join(parts, dot)
		}
		if visibleWidth(line)+visibleWidth(state)+2 <= m.w {
			break
		}
	}
	pad := m.w - visibleWidth(line) - visibleWidth(state) - 1
	return line + panelBg.Render(strings.Repeat(" ", max(pad, 1))) + state + panelBg.Render(" ")
}

func (m *Model) tabs() string {
	th := m.th
	var parts []string
	for i, n := range tabNames {
		label := fmt.Sprintf("%d %s", i+1, n)
		if i == m.tab {
			parts = append(parts, th.Fg(th.Magenta, true).Underline(true).Render(label))
		} else if m.visible(i) {
			parts = append(parts, th.Fg(th.Cyan, false).Render(label))
		} else {
			parts = append(parts, th.Fg(th.Dim, false).Render(label))
		}
	}
	return " " + strings.Join(parts, th.Fg(th.Line, false).Render(" ╱ "))
}

var pending = map[int]string{
	0: "Message history isn't available: the store couldn't be opened.",
	1: "The contact table, path editor, traces and sharing arrive in milestone 3.",
	2: "The map arrives in milestone 7; the design work so far is in prototypes/map.",
	3: "The live packet inspector arrives in milestone 4. Raw packets already scroll along the footer.",
}

func (m *Model) body(w, h int) []string {
	th := m.th
	mainH := h
	var debug []string
	if m.debug {
		dh := max(h*2/5, 6)
		mainH = h - dh
		debug = m.panel("FRAMES", m.frameLines(dh-2, w-4), w, dh, th.Cyan, false)
	}

	if m.help {
		box := m.panel("HELP", m.helpLines(), w, mainH, th.Cyan, true)
		return append(box, debug...)
	}
	if m.search.open {
		box := m.panel("SEARCH ALL CONVERSATIONS", m.searchLines(w-4, mainH-2), w, mainH, th.Cyan, true)
		return append(box, debug...)
	}
	return append(m.panes(w, mainH), debug...)
}

// screenBox draws one screen in its box.
func (m *Model) screenBox(tab, w, h int, focused bool) []string {
	th, now := m.th, m.now
	accent := th.Magenta
	glitching := m.level.Allows(motion.Glitch) && motion.Running(m.glitch, glitchDur, now)
	if glitching || m.linked != linkUp {
		accent = th.Red
	}
	if !focused {
		accent = th.Muted
	}
	iw, ih := w-4, h-2
	var title string
	var content []string
	switch {
	case tab == 0 && m.chat != nil:
		title = tabNames[0]
		if m.chats.unread > 0 {
			title += fmt.Sprintf(" · %d UNREAD", m.chats.unread)
		}
		content = m.renderChats(iw, ih)
	case tab == 3:
		title, content = m.inspectorTitle(), m.renderInspector(iw, ih)
	case tab == 5 && m.phase == phaseMain:
		title, content = m.deviceTitle(), m.renderDevice(iw, ih)
	case tab == 6 && m.phase == phaseMain:
		title, content = m.statsTitle(), m.renderStats(iw, ih)
	case tab == 4 && m.chat != nil:
		title, content = m.adminTitle(), m.renderAdmin(iw, ih)
	case tab == 1 && m.chat != nil:
		title, content = m.contactsTitle(), m.renderContacts(iw, ih)
	default:
		title = tabNames[tab]
		content = append(content, th.Fg(th.Yellow, true).Render("[PENDING] ")+th.Fg(th.Muted, false).Render(pending[tab]))
		content = append(content, "", th.Fg(th.Muted, true).Render("SYSTEM LOG"))
		room := ih - len(content)
		start := max(len(m.log)-room, 0)
		for _, l := range m.log[start:] {
			content = append(content, m.logRow(l, false))
		}
	}
	if m.lay.zoomed {
		title += " · ZOOMED"
	}
	box := m.panel(title, content, w, h, accent, focused)
	if !focused {
		return box
	}
	return m.effects(box, w)
}

// effects applies the panel glitch and the screen-switch wipe.
func (m *Model) effects(box []string, w int) []string {
	th, now := m.th, m.now
	glitching := m.level.Allows(motion.Glitch) && motion.Running(m.glitch, glitchDur, now)
	if glitching {
		rng := rand.New(rand.NewSource(int64(now.Sub(m.glitch) / motion.FrameDur)))
		for i := range box {
			if rng.Intn(3) == 0 {
				box[i] = " " + box[i]
			}
		}
	}
	if m.level.Allows(motion.Wipe) && motion.Running(m.tabAt, wipeDur, now) {
		t := motion.EaseOut(motion.Progress(m.tabAt, wipeDur, now))
		for i := range box {
			box[i] = wipe(box[i], w, t, th)
		}
	}
	return box
}

func (m *Model) logRow(l logLine, typed bool) string {
	th := m.th
	tone := map[radio.Tone]theme.RGB{radio.ToneInfo: th.Cyan, radio.ToneOK: th.Green, radio.ToneWarn: th.Yellow, radio.ToneFail: th.Red}[l.tone]
	text := l.text
	cursor := ""
	if typed && m.level.Allows(motion.Typing) {
		var done bool
		text, done = motion.Typewriter(l.text, l.at, logCPS, m.now)
		if !done {
			cursor = th.Fg(th.Text, true).Render("█")
		}
	}
	return th.Fg(th.Dim, false).Render("["+l.at.Format("15:04:05")+"] ") + th.Fg(tone, true).Render("▸ ") + th.Fg(th.Text, false).Render(text) + cursor
}

func (m *Model) footer() []string {
	th, now, s := m.th, m.now, m.stats
	dot := th.Fg(th.Dim, false).Render(" · ")
	noun := "packets"
	if s.packets == 1 {
		noun = "packet"
	}
	status := " "
	if m.chats.unread > 0 {
		status += th.Fg(th.Magenta, true).Render(fmt.Sprintf("● %d unread", m.chats.unread)) + dot
	}
	status += th.Fg(th.Muted, false).Render(fmt.Sprintf("%d %s heard", s.packets, noun))
	if !s.lastRx.IsZero() {
		snr := s.currentSNR(now)
		status += dot + th.Fg(th.Muted, false).Render("last rx "+ago(now.Sub(s.lastRx))+" ") +
			th.Fg(th.SNR(s.snrTo), true).Render(fmt.Sprintf("SNR %+.2f", snr)) +
			th.Fg(th.Muted, false).Render(fmt.Sprintf("  RSSI %d", s.rssi))
	}
	contacts, channels := m.ready.Contacts, m.ready.Channels
	if m.chat != nil && m.chat.Device() != "" {
		contacts, channels = len(m.chat.Contacts()), len(m.chat.Channels()) // they change after connecting
	}
	status += dot + th.Fg(th.Muted, false).Render(fmt.Sprintf("%d contacts · %d channels", contacts, channels))
	status += dot + th.Fg(th.Dim, false).Render("theme "+m.themeTo.Name+" · motion "+m.level.String())

	logLine := ""
	if n := len(m.log); n > 0 {
		logLine = " " + m.logRow(m.log[n-1], true)
	}
	keys := th.Fg(th.Dim, false).Render(" 1-7 screens · ctrl+w panes · : command · ? help · ctrl+f search · T theme · v motion · F12 frames · q quit")
	if m.pal.open {
		keys = m.paletteLine()
	}
	if m.paneKey {
		keys = th.Fg(th.Magenta, true).Render(" PANES ") + th.Fg(th.Muted, false).Render(" v split right · s split down · ←↑↓→ move · w next · q close · z zoom · < > - + = size")
	}
	tick := ""
	if m.tickerOn {
		tick = m.ticker.render(th, m.level, now, m.w)
	}
	return []string{status, logLine, tick, keys}
}

func ago(d time.Duration) string {
	switch {
	case d < time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}

func (m *Model) frameLines(n, w int) []string {
	th := m.th
	var out []string
	start := max(len(m.frames)-n, 0)
	for _, f := range m.frames[start:] {
		arrow, c := "→", th.Magenta
		if f.Dir == radio.FromRadio {
			arrow, c = "←", th.Cyan
		}
		hex := ""
		if len(f.Bytes) > 0 {
			hex = "  " + hexBytes(f.Bytes, func(i int) bool { return i >= f.Hidden[0] && i < f.Hidden[1] })
		}
		line := th.Fg(th.Dim, false).Render(f.At.Format("15:04:05.000")+" ") + th.Fg(c, true).Render(arrow) +
			th.Fg(th.Muted, false).Render(fmt.Sprintf(" %02X ", f.Code)) + th.Fg(th.Text, false).Render(f.Name) +
			th.Fg(th.Dim, false).Render(hex)
		out = append(out, line)
	}
	if len(out) == 0 {
		out = append(out, th.Fg(th.Dim, false).Render("no frames yet"))
	}
	return out
}

// batteryIcon draws a five-cell battery filled to the charge: green, then yellow below half, red below a fifth.
// paint styles each piece, so the header can keep its background.
func batteryIcon(th theme.Theme, pct int, paint func(s string, c theme.RGB, bold bool) string) string {
	cells := min(max((pct+10)/20, 0), 5)
	if pct > 0 {
		cells = max(cells, 1) // an almost-flat battery still shows a sliver
	}
	c := th.Green
	switch {
	case pct < 20:
		c = th.Red
	case pct < 50:
		c = th.Yellow
	}
	return paint(strings.Repeat("█", cells), c, false) + paint(strings.Repeat("░", 5-cells), th.Dim, false) + paint("▌", th.Dim, false)
}

// plainPaint styles without a background, for use outside the header.
func (m *Model) plainPaint(s string, c theme.RGB, bold bool) string {
	return m.th.Fg(c, bold).Render(s)
}

// batteryText is "4.11 V ████░▌ 87%", always 18 cells wide so it lines up in columns.
func (m *Model) batteryText(b radio.Battery, bold bool) string {
	th := m.th
	return th.Fg(th.Text, bold).Render(fmt.Sprintf("%.2f V ", float64(b.MilliVolts)/1000)) + batteryIcon(th, b.Percent(), m.plainPaint) +
		th.Fg(th.Text, bold).Render(fmt.Sprintf(" %-4s", fmt.Sprintf("%d%%", b.Percent())))
}

// col fits s to exactly w cells, cut with … or padded. Names often hold emoji, which take two cells,
// so padding by character count would push the columns after them out of line.
func col(s string, w int) string {
	if visibleWidth(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", max(w-visibleWidth(s), 0))
}
