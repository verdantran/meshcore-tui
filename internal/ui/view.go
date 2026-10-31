package ui

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/clipperhouse/uax29/v2/graphemes"

	"github.com/verdantran/meshcore-tui/internal/mapview"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

func (m *Model) View() tea.View {
	m.mapv.drawing = true
	defer func() { m.mapv.drawing, m.mapv.nodesSeen = false, nil }()
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
		lines[i] = fit(oneLine(lines[i]), m.w)
	}
	if m.phase == phaseMain {
		m.overlayToasts(lines)
		m.overlayFinder(lines)
		for i, l := range lines {
			if strings.Contains(l, "\n") {
				lines[i] = fit(oneLine(l), m.w)
			}
		}
	}
	frame := drawable(strings.Join(lines, "\n"))
	if m.opt.ASCII {
		frame = toASCII(frame)
	} else if m.opt.SplitEmoji {
		frame = splitEmoji(frame)
	}
	if m.opt.TextEmoji > renderWidth.StringWidth(textEmojiProbe) {
		frame = widenTextEmoji(frame, renderWidth == ansi.GraphemeWidth)
	}
	lone, flags := renderWidth.StringWidth(loneProbe), renderWidth.StringWidth(flagProbe)
	loneOff := m.opt.FlagLetters || m.opt.LoneLetter != 0 && m.opt.LoneLetter != lone
	if flagsOff := m.opt.FlagLetters || m.opt.Flag != 0 && m.opt.Flag != flags; loneOff || flagsOff {
		frame = plainLetters(frame, loneOff, flagsOff, lone)
	}
	v := tea.NewView(frame)
	v.AltScreen = true
	v.ReportFocus = true
	if m.mouseOn {
		v.MouseMode = tea.MouseModeCellMotion
	}
	if !m.th.Mono {
		v.BackgroundColor = m.th.Bg.Color()
	}
	v.WindowTitle = "meshcore-tui"
	if m.ready.Self.Name != "" {
		v.WindowTitle = "meshcore-tui · " + m.ready.Self.Name
	}
	return v
}

// drawable keeps a frame's text and colours and drops every other escape sequence and control character,
// so nothing that slipped past the cleaning of text from the mesh can reach the terminal.
func drawable(frame string) string {
	if plainFrame(frame) {
		return frame
	}
	frame = strings.ToValidUTF8(frame, "\uFFFD") // a stray byte could otherwise swallow the escape after it
	var b strings.Builder
	p := ansi.NewParser()
	var state byte
	for len(frame) > 0 {
		seq, _, n, next := ansi.DecodeSequence(frame, state, p)
		switch {
		case ansi.HasCsiPrefix(seq) && p.Command() == 'm', seq == "\n":
			b.WriteString(seq)
		case seq[0] < 0x20 || seq[0] == 0x7f || strings.ContainsRune(seq, 0x1b) || strings.ContainsFunc(seq, func(r rune) bool { return r >= 0x80 && r <= 0x9f }):
		default:
			b.WriteString(seq)
		}
		state, frame = next, frame[n:]
	}
	return b.String()
}

// plainFrame reports a frame of nothing but text, line breaks and CSI sequences, which the renderer parses and
// keeps only for colour; anything else goes through drawable's parser.
func plainFrame(frame string) bool {
	if !utf8.ValidString(frame) {
		return false
	}
	for i := 0; i < len(frame); i++ {
		switch c := frame[i]; {
		case c == 0x1b:
			if i+1 == len(frame) || frame[i+1] != '[' {
				return false
			}
		case c < 0x20 && c != '\n', c == 0x7f:
			return false
		case c == 0xc2 && frame[i+1] <= 0x9f: // U+0080–U+009F, and valid UTF-8 means a byte follows
			return false
		}
	}
	return true
}

// oneLine keeps a row on its row, whatever reached it.
func oneLine(s string) string { return strings.ReplaceAll(s, "\n", " ") }

// renderWidth measures text as the renderer lays it out: per character, as wcwidth does, unless the terminal
// says it clusters graphemes (mode 2027).
var renderWidth = ansi.WcWidth

func setRenderWidth(m ansi.Method) {
	renderWidth = m
	mapview.SetWidth(m)
}

// Layout helpers. Every rendered row is fitted to the terminal width exactly.

func visibleWidth(s string) int { return renderWidth.StringWidth(s) }

// clusters walks s a grapheme or escape sequence at a time. Text is measured with visibleWidth, so cuts
// agree with it; the library's own cuts measure clusters one way and whole strings another, and let rows
// through wider than they say.
func clusters(s string, yield func(seq string, w int, esc bool)) {
	g := graphemes.FromString(s)
	g.AnsiEscapeSequences = true
	for g.Next() {
		seq := g.Value()
		if r, _ := utf8.DecodeRuneInString(seq); r < 0x20 || r >= 0x7f && r < 0xa0 {
			yield(seq, 0, true)
		} else {
			yield(seq, visibleWidth(seq), false)
		}
	}
}

// take is the longest start of s at most n cells wide, and its width, with tail written where it was cut.
// Escape sequences are all kept, so styles still close and the tail is drawn in the style of the text it ends.
func take(s string, n int, tail string) (string, int) {
	var b strings.Builder
	w, full := 0, false
	clusters(s, func(seq string, sw int, esc bool) {
		if !esc && (full || w+sw > n) {
			if !full {
				b.WriteString(tail)
			}
			full = true
			return
		}
		b.WriteString(seq)
		w += sw
	})
	return b.String(), w
}

// skip drops the first n cells of s. A wide character cut in two leaves spaces for its remaining cells, so
// what follows stays in its column; escape sequences are kept, so styles carry on.
func skip(s string, n int) string {
	var b strings.Builder
	w := 0
	clusters(s, func(seq string, sw int, esc bool) {
		switch {
		case esc || w >= n:
			b.WriteString(seq)
		case w+sw > n:
			b.WriteString(strings.Repeat(" ", w+sw-n))
		}
		w += sw
	})
	return b.String()
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if visibleWidth(s) <= n {
		return s
	}
	head, _ := take(s, n-1, "…")
	return head
}

func fit(s string, w int) string {
	vw := visibleWidth(s)
	if vw > w {
		s, vw = take(s, w, "")
	}
	return s + strings.Repeat(" ", max(w-vw, 0))
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
	paint := func(s string) string { return th.Paint(edge, false, s) }
	head := th.Paint(accent, true, "▌") + th.Paint(th.Muted, true, truncate(title, w-6)+" ")
	top := paint(tl+hz) + head + paint(strings.Repeat(hz, max(w-4-visibleWidth(head), 0))+hz+tr)
	out := []string{top}
	inner := w - 4
	// A row must never contain a line break, or the panel would draw taller than it thinks.
	var rows []string
	for _, b := range body {
		rows = append(rows, strings.Split(b, "\n")...)
	}
	body = rows
	side := paint(vt)
	for i := 0; i < h-2; i++ {
		content := ""
		if i < len(body) {
			content = body[i]
		}
		out = append(out, side+" "+fit(content, inner)+" "+side)
	}
	out = append(out, paint(bl+strings.Repeat(hz, max(w-2, 0))+br))
	return out
}

// Main screen.

const footerRows = 4

func (m *Model) bodyHeight() int { return max(m.h-2-footerRows, 3) }

// mainHeight is what's left of the body for the screens once the frames pane takes its share.
func (m *Model) mainHeight(h int) int {
	if m.debug {
		return h - max(h*2/5, 6)
	}
	return h
}

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
	return fit(line, max(x-4, 0)) + edge
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
	pct := batt.PercentOf(m.chemistry(r.Self.PubKey))
	power := seg(fmt.Sprintf("%.2fV ", float64(batt.MilliVolts)/1000), th.Cyan, true) + batteryIcon(th, pct, seg) + seg(fmt.Sprintf(" %d%%", pct), th.Cyan, true)
	if m.chemistry(r.Self.PubKey) == radio.Wired {
		power = seg("wired", th.Cyan, true)
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
		power,
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
	if m.repeaterMode() {
		return m.repeaterTabs()
	}
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
	return " " + strings.Join(parts, th.Fg(th.Line, false).Render(tabGap))
}

const tabGap = " ╱ "

// repeaterTabs lists a repeater's screens, which are its admin panes.
func (m *Model) repeaterTabs() string {
	th, a := m.th, &m.adm
	var parts []string
	for i, n := range paneNames {
		label := strings.ToUpper(n)
		if adminPane(i) == a.pane {
			if m.level.Allows(motion.Decode) && motion.Running(a.paneAt, wipeDur*2, m.now) {
				label = motion.DecodeText(label, int64(i), motion.Progress(a.paneAt, wipeDur*2, m.now))
			}
			parts = append(parts, th.Fg(th.Magenta, true).Underline(true).Render(fmt.Sprintf("%d %s", i+1, label)))
			continue
		}
		parts = append(parts, th.Fg(th.Dim, false).Render(fmt.Sprintf("%d %s", i+1, label)))
	}
	return " " + strings.Join(parts, th.Fg(th.Line, false).Render(tabGap))
}

var pending = map[int]string{
	0: "Message history isn't available: the store couldn't be opened.",
	1: "The contact table, path editor, traces and sharing arrive in milestone 3.",
	3: "The live packet inspector arrives in milestone 4. Raw packets already scroll along the footer.",
}

func (m *Model) body(w, h int) []string {
	th := m.th
	mainH := m.mainHeight(h)
	var debug []string
	if m.debug {
		debug = m.panel("FRAMES", m.frameLines(h-mainH-2, w-4), w, h-mainH, th.Cyan, false)
	}

	if m.help {
		lines, room := m.helpLines(), mainH-2
		title := "HELP"
		if m.opt.Version != "" {
			title += " · meshcore-tui " + m.opt.Version
		}
		if len(lines) > room {
			start := min(m.helpScroll, len(lines)-room)
			lines = lines[start : start+room]
			title += fmt.Sprintf(" · %d%% · j k scroll", (start+room)*100/len(m.helpLines()))
		}
		box := m.panel(title, lines, w, mainH, th.Cyan, true)
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
	if m.dangerWaiting(tab) {
		accent = th.Red
		if m.level.Allows(motion.Flash) {
			accent = th.Red.Lerp(th.Bg, 0.6*motion.Pulse(now, pulsePeriod))
		}
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
	case tab == 2 && m.phase == phaseMain:
		title, content = m.mapTitle(), m.renderMap(iw, ih)
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
				box[i] = " " + scrambleRow(box[i], rng)
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
	if m.repeaterMode() {
		status = " " + m.repeaterStatus()
	} else if !s.lastRx.IsZero() {
		snr := s.currentSNR(now)
		status += dot + th.Fg(th.Muted, false).Render("last rx "+ago(now.Sub(s.lastRx))+" ") +
			th.Fg(th.SNR(s.snrTo), true).Render(fmt.Sprintf("SNR %+.2f", snr)) +
			th.Fg(th.Muted, false).Render(fmt.Sprintf("  RSSI %d", s.rssi))
	}
	contacts, channels := m.ready.Contacts, m.ready.Channels
	if m.chat != nil && m.chat.Device() != "" {
		contacts, channels = m.chat.ContactCount(), len(m.chat.Channels()) // they change after connecting
	}
	if !m.repeaterMode() {
		status += dot + th.Fg(th.Muted, false).Render(fmt.Sprintf("%d contacts · %d channels", contacts, channels))
	}
	status += dot + th.Fg(th.Dim, false).Render("theme "+m.themeTo.Name+" · motion "+m.level.String())

	logLine := ""
	if n := len(m.log); n > 0 {
		logLine = " " + m.logRow(m.log[n-1], true)
	}
	keys := th.Fg(th.Dim, false).Render(" 1-7 screens · ctrl+w panes · : command · ? help · ctrl+f search · T theme · v motion · F12 frames · q quit")
	if m.repeaterMode() {
		keys = th.Fg(th.Dim, false).Render(" 1-9 screens · ←→ next · : command · ? help · T theme · v motion · q quit")
	}
	if m.pal.open {
		keys = m.paletteLine()
	}
	if m.asking != nil {
		keys = " " + m.askLine()
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
func (m *Model) batteryText(node [32]byte, b radio.Battery, bold bool) string {
	th, pct := m.th, b.PercentOf(m.chemistry(node))
	if m.chemistry(node) == radio.Wired {
		return th.Fg(th.Text, bold).Render(fmt.Sprintf("%-18s", "wired"))
	}
	return th.Fg(th.Text, bold).Render(fmt.Sprintf("%.2f V ", float64(b.MilliVolts)/1000)) + batteryIcon(th, pct, m.plainPaint) +
		th.Fg(th.Text, bold).Render(fmt.Sprintf(" %-4s", fmt.Sprintf("%d%%", pct)))
}

func (m *Model) chemistry(node [32]byte) radio.Chemistry {
	if c := radio.Chemistry(m.adminConfig().Batteries[radio.KeyHex(node[:])]); slices.Contains(radio.Chemistries, c) {
		return c
	}
	return radio.LiIon
}

// cycleBattery moves a node on to the next battery chemistry, which sets the curve its percentage is read from.
func (m *Model) cycleBattery(node [32]byte, name string) {
	c := m.chemistry(node).Next()
	m.adminConfig().SetBattery(radio.KeyHex(node[:]), string(c))
	if m.opt.Config != nil {
		m.saveConfig()
	}
	m.addLog(m.now, "BATTERY  "+name+" · "+c.String(), radio.ToneInfo)
}

// col fits s to exactly w cells, cut with … or padded. Names often hold emoji, which take two cells,
// so padding by character count would push the columns after them out of line.
func col(s string, w int) string {
	return fit(truncate(s, w), w)
}

// repeaterStatus is the footer's summary of a repeater on USB: its latest poll and the watch.
func (m *Model) repeaterStatus() string {
	th := m.th
	dot := th.Fg(th.Dim, false).Render(" · ")
	_, sess, ok := m.openNode()
	if !ok {
		return th.Fg(th.Muted, false).Render("repeater console")
	}
	p, ok := sess.Latest()
	if !ok {
		return th.Fg(th.Muted, false).Render("waiting for its first status")
	}
	st := statsOf(p)
	out := th.Fg(th.Muted, false).Render("up "+uptime(st.UpTimeSecs)) + dot +
		th.Fg(th.Muted, false).Render(fmt.Sprintf("%s received · %s sent", commas(int(st.PacketsRecv)), commas(int(st.PacketsSent)))) + dot +
		th.Fg(th.Muted, false).Render("polled "+ago(m.now.Sub(p.At))+", every "+every(m.watchEvery()))
	if n := len(m.historyOf(sess.Key)); n > 0 {
		out += dot + th.Fg(th.Muted, false).Render(plural(n, "poll")+" kept")
	}
	return out
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return commas(n) + " " + noun + "s"
}
