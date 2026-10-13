package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"meshcore-tui/internal/chat"
	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/store"
)

const (
	decodeDur  = 320 * time.Millisecond
	arriveDur  = 700 * time.Millisecond
	popDur     = 450 * time.Millisecond
	sendLimit  = 20 * time.Second
	historyLen = 300
)

type chatsModel struct {
	infos   []chat.Info
	sel     int
	writing bool
	input   string
	msgs    []store.Message
	scroll  int // messages hidden below the bottom of the pane

	filter    string // "/" narrows the open conversation to matching messages
	filtering bool   // typing the filter
	focus     int64  // a message jumped to from search, marked until the next move

	arrived map[int64]time.Time // incoming messages, for the decode and the bar flash
	acked   map[int64]time.Time // for the ✓ pop
	failed  map[int64]time.Time // for the glitch
	unread  int
}

func newChats() chatsModel {
	return chatsModel{arrived: map[int64]time.Time{}, acked: map[int64]time.Time{}, failed: map[int64]time.Time{}}
}

type sendResultMsg struct {
	send chat.Send
	sent radio.Sent
	err  error
}

type ackTimeoutMsg struct{ wait chat.Wait }

func (m *Model) selectedConvo() (chat.Info, bool) {
	c := &m.chats
	if c.sel < 0 || c.sel >= len(c.infos) {
		return chat.Info{}, false
	}
	return c.infos[c.sel], true
}

// refreshChats reloads the conversation list and the open conversation, marking it read if it's on screen.
func (m *Model) refreshChats() {
	if m.chat == nil || m.chat.Device() == "" {
		return
	}
	c := &m.chats
	var keep store.Convo
	if cur, ok := m.selectedConvo(); ok {
		keep = cur.Convo
	}
	infos, err := m.chat.Conversations()
	if err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
		return
	}
	c.infos, c.sel = infos, min(c.sel, max(len(infos)-1, 0))
	for i, info := range infos {
		if info.Convo == keep {
			c.sel = i
		}
	}
	cur, ok := m.selectedConvo()
	if !ok {
		return
	}
	if m.phase == phaseMain && m.tab == 0 && cur.Unread > 0 {
		m.chat.MarkRead(cur.Convo)
		c.infos[c.sel].Unread = 0
	}
	if c.filter != "" {
		found, _ := m.chat.Search(c.filter, cur.Convo, historyLen)
		c.msgs = make([]store.Message, len(found))
		for i, f := range found {
			c.msgs[len(found)-1-i] = f // search returns newest first; the pane wants oldest first
		}
	} else {
		c.msgs, _ = m.chat.Messages(cur.Convo, historyLen)
	}
	c.unread = 0
	for _, info := range c.infos {
		c.unread += info.Unread
	}
}

func (m *Model) sendCmd(s chat.Send) tea.Cmd {
	r := m.radio
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), sendLimit)
		defer cancel()
		if s.ResetPath {
			if err := r.ResetPath(ctx, s.To); err != nil {
				return sendResultMsg{send: s, err: err}
			}
		}
		if s.Channel {
			return sendResultMsg{send: s, err: r.SendChannel(ctx, s.Idx, s.Text)}
		}
		sent, err := r.SendText(ctx, s.To, s.Text)
		return sendResultMsg{send: s, sent: sent, err: err}
	}
}

func waitCmd(w chat.Wait) tea.Cmd {
	return tea.Tick(w.Delay, func(time.Time) tea.Msg { return ackTimeoutMsg{w} })
}

func (m *Model) chatEvent(e radio.Event) tea.Cmd {
	if m.chat == nil {
		return nil
	}
	switch e := e.(type) {
	case radio.Ready:
		if err := m.chat.Ready(e); err != nil {
			m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
		}
		m.refreshChats()
	case radio.Incoming:
		msg, err := m.chat.Incoming(e)
		if err != nil {
			m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
			return nil
		}
		m.chats.arrived[msg.ID] = m.now
		title := m.chat.Title(msg.Convo)
		tone := radio.ToneInfo
		if m.chat.Mentions(msg.Text) {
			tone = radio.ToneWarn
		}
		m.addLog(m.now, fmt.Sprintf("MSG  %s · %s: %s", title, msg.Sender, truncate(msg.Text, 60)), tone)
		m.refreshChats()
		return m.considerNote(msg, title)
	case radio.ContactUpdated:
		m.chat.ContactUpdated(e.Contact)
		m.refreshChats()
	case radio.Acked:
		msg, ok, _ := m.chat.Acked(e)
		if ok {
			m.chats.acked[msg.ID] = m.now
			m.addLog(m.now, fmt.Sprintf("DELIVERED  ✓ %s  %s", m.chat.Title(msg.Convo), seconds(msg.RTT)), radio.ToneOK)
			m.refreshChats()
		}
	}
	return nil
}

func (m *Model) chatResult(r sendResultMsg) tea.Cmd {
	msg, wait, err := m.chat.Result(r.send, r.sent, r.err)
	if err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
	}
	if msg.State == store.Failed && msg.ID != 0 {
		m.chats.failed[msg.ID] = m.now
		m.fail(fmt.Sprintf("SEND FAILED  %s: %v", m.chat.Title(msg.Convo), r.err))
	}
	m.refreshChats()
	if wait != nil {
		return waitCmd(*wait)
	}
	return nil
}

func (m *Model) chatTimeout(t ackTimeoutMsg) tea.Cmd {
	msg, next, err := m.chat.Timeout(t.wait)
	if err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
	}
	m.refreshChats()
	switch {
	case next != nil:
		how := "retrying"
		if next.ResetPath {
			how = "retrying by flood"
		}
		m.addLog(m.now, fmt.Sprintf("NO ACK  %s  %s (try %d)", m.chat.Title(msg.Convo), how, next.Attempt+1), radio.ToneWarn)
		return m.sendCmd(*next)
	case msg.State == store.Failed:
		m.chats.failed[msg.ID] = m.now
		m.fail(fmt.Sprintf("NOT DELIVERED  %s after %d tries", m.chat.Title(msg.Convo), msg.Attempts))
	}
	return nil
}

func (m *Model) composeSend() tea.Cmd {
	cur, ok := m.selectedConvo()
	if !ok || m.chat == nil {
		return nil
	}
	_, send, err := m.chat.Compose(cur.Convo, m.chats.input, m.now)
	if err != nil {
		m.fail(err.Error())
		return nil
	}
	m.chats.input, m.chats.scroll = "", 0
	m.refreshChats()
	return m.sendCmd(send)
}

// chatsKey handles keys on the Chats screen. It returns handled=false for keys the screen leaves to the app.
func (m *Model) chatsKey(s string) (tea.Cmd, bool) {
	c := &m.chats
	if c.filtering {
		switch s {
		case "esc":
			c.filter, c.filtering = "", false
		case "enter":
			c.filtering = false
		case "backspace":
			if r := []rune(c.filter); len(r) > 0 {
				c.filter = string(r[:len(r)-1])
			}
		case "space":
			c.filter += " "
		default:
			if len([]rune(s)) == 1 {
				c.filter += s
			}
		}
		c.scroll = 0
		m.refreshChats()
		return nil, true
	}
	if c.writing {
		switch s {
		case "esc":
			c.writing = false
		case "enter":
			return m.composeSend(), true
		case "backspace":
			if r := []rune(c.input); len(r) > 0 {
				c.input = string(r[:len(r)-1])
			}
		case "space":
			c.input += " "
		default:
			if len([]rune(s)) == 1 {
				c.input += s
			}
		}
		return nil, true
	}
	switch s {
	case "/":
		c.filtering, c.filter = true, ""
	case "esc":
		if c.filter == "" {
			return nil, false
		}
		c.filter = ""
		m.refreshChats()
	case "up", "k":
		if c.sel > 0 {
			c.sel--
			c.scroll, c.focus, c.filter = 0, 0, ""
			m.refreshChats()
		}
	case "down", "j":
		if c.sel < len(c.infos)-1 {
			c.sel++
			c.scroll, c.focus, c.filter = 0, 0, ""
			m.refreshChats()
		}
	case "enter", "i":
		if len(c.infos) > 0 {
			c.writing = true
		}
	case "pgup", "K":
		c.scroll = min(c.scroll+5, max(len(c.msgs)-1, 0))
	case "pgdown", "J":
		c.scroll = max(c.scroll-5, 0)
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) chatsAnimating() bool {
	now := m.now
	for _, t := range m.chats.arrived {
		if motion.Running(t, arriveDur, now) {
			return true
		}
	}
	for _, t := range m.chats.acked {
		if motion.Running(t, popDur, now) {
			return true
		}
	}
	for _, t := range m.chats.failed {
		if motion.Running(t, glitchDur*3, now) {
			return true
		}
	}
	if m.tab == 0 {
		for _, msg := range m.chats.msgs {
			waiting := msg.State == store.Pending || msg.State == store.Sent && !strings.HasPrefix(string(msg.Convo), "ch:")
			if msg.Out && waiting {
				return true // the waiting ramp runs until the message settles
			}
		}
	}
	return false
}

func seconds(d time.Duration) string { return fmt.Sprintf("%.1fs", d.Seconds()) }

// renderChats lays out the conversation list, the open conversation and the compose line.
func (m *Model) renderChats(inner, rows int) []string {
	th, c := m.th, &m.chats
	if m.chat == nil || len(c.infos) == 0 {
		return []string{th.Fg(th.Dim, false).Render("No conversations yet. Channels and contacts appear here once the radio has synced.")}
	}
	listW := min(max(inner/4, 20), 32)
	paneW := inner - listW - 3
	left := m.convoList(listW, rows)
	right := m.convoPane(paneW, rows)
	sep := th.Fg(th.Line, false).Render(" │ ")
	out := make([]string, rows)
	for i := range out {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = fit(l, listW) + sep + r
	}
	return out
}

func (m *Model) convoList(w, rows int) []string {
	th, c := m.th, &m.chats
	var out []string
	// Two groups: channels, then everyone else (rooms are marked with ■ among direct conversations).
	group := -1
	selRow := 0
	for i, info := range c.infos {
		g := 1
		if info.Kind == chat.KindChannel {
			g = 0
		}
		if g != group {
			if len(out) > 0 {
				out = append(out, "")
			}
			out = append(out, th.Fg(th.Muted, true).Render([]string{"CHANNELS", "DIRECT"}[g]))
			group = g
		}
		badge := ""
		if info.Unread > 0 {
			badge = th.Block(th.Bg, th.Magenta).Render(fmt.Sprintf(" %d ", info.Unread))
		}
		if info.Mention {
			badge = th.Fg(th.Magenta, true).Render("@") + badge
		}
		icon := map[chat.Kind]string{chat.KindChannel: "#", chat.KindDirect: "●", chat.KindRoom: "■"}[info.Kind]
		title := strings.TrimPrefix(info.Title, "#")
		name := truncate(title, w-4-visibleWidth(badge))
		if i == c.sel {
			selRow = len(out)
			row := fit(" "+icon+" "+name, w-1-visibleWidth(badge))
			out = append(out, th.Fg(th.Magenta, true).Render("▌")+m.raised(row)+badge)
			continue
		}
		col := th.Muted
		if info.Last == nil {
			col = th.Dim
		}
		iconCol := th.Dim
		if info.Kind == chat.KindRoom {
			iconCol = th.Violet
		}
		out = append(out, " "+th.Fg(iconCol, false).Render(icon)+" "+th.Fg(col, info.Unread > 0).Render(fit(name, w-3-visibleWidth(badge)))+badge)
	}
	// Keep the selection in view.
	if len(out) > rows {
		start := min(max(selRow-rows/2, 0), len(out)-rows)
		out = out[start : start+rows]
	}
	return out
}

func (m *Model) convoPane(w, rows int) []string {
	th, c := m.th, &m.chats
	cur, _ := m.selectedConvo()

	// Title line: the conversation and how messages will travel.
	title := th.Fg(th.Magenta, true).Render("▌") + th.Fg(th.Text, true).Render(cur.Title)
	route := ""
	switch cur.Kind {
	case chat.KindChannel:
		route = "channel · flood"
	default:
		route = "not a contact · can't reply"
		if ct, ok := m.chat.Contact(cur.Convo); ok {
			route = "flood (no stored route)"
			if ct.PathLen >= 0 {
				var hops []string
				for _, b := range ct.Path {
					hops = append(hops, fmt.Sprintf("%02X", b))
				}
				route = fmt.Sprintf("direct · %d hops %s", ct.PathLen, strings.Join(hops, "·"))
			}
		}
	}
	head := title + th.Fg(th.Dim, false).Render("  "+route)
	if c.filter != "" || c.filtering {
		cursor := ""
		if c.filtering {
			cursor = th.Fg(th.Text, true).Render("█")
		}
		head = title + th.Fg(th.Yellow, true).Render("  / ") + th.Fg(th.Text, false).Render(c.filter) + cursor +
			th.Fg(th.Dim, false).Render(fmt.Sprintf("  · %d matching · esc clears", len(c.msgs)))
	}

	// Compose line, always at the bottom.
	limit := 0
	if m.chat != nil {
		limit = m.chat.MaxBytes(cur.Convo)
	}
	used := len(c.input)
	counterCol := th.Dim
	switch {
	case used > limit:
		counterCol = th.Red
	case used*10 >= limit*9:
		counterCol = th.Yellow
	}
	counter := th.Fg(counterCol, used*10 >= limit*9).Render(fmt.Sprintf("%d/%d", used, limit))
	var compose string
	if c.writing {
		text := c.input
		room := w - 4 - visibleWidth(counter)
		if r := []rune(text); len(r) > room {
			text = "…" + string(r[len(r)-room+1:])
		}
		compose = th.Fg(th.Magenta, true).Render("▸ ") + th.Fg(th.Text, false).Render(text) + th.Fg(th.Text, true).Render("█")
	} else {
		compose = th.Fg(th.Dim, false).Render("▸ enter to write · ↑↓ conversations · / filter · ctrl+f search all · K/J scroll")
	}
	compose = fit(compose, w-visibleWidth(counter)-1) + " " + counter

	// Messages, newest at the bottom.
	var lines []string
	for _, msg := range c.msgs {
		lines = append(lines, m.messageLines(msg, w)...)
	}
	avail := rows - 3
	end := len(lines)
	if c.scroll > 0 {
		// Scroll counts messages; skip the last c.scroll messages' lines.
		skip := 0
		for i := len(c.msgs) - 1; i >= len(c.msgs)-c.scroll && i >= 0; i-- {
			skip += len(m.messageLines(c.msgs[i], w))
		}
		end = max(len(lines)-skip, 0)
	}
	start := max(end-avail, 0)
	body := lines[start:end]
	if len(c.msgs) == 0 {
		body = []string{th.Fg(th.Dim, false).Render("No messages yet. Press enter to write the first one.")}
		if c.filter != "" {
			body = []string{th.Fg(th.Dim, false).Render("Nothing here matches.")}
		}
	}
	out := []string{head, ""}
	for i := 0; i < avail-len(body); i++ {
		out = append(out, "")
	}
	out = append(out, body...)
	return append(out, compose)
}

func (m *Model) messageLines(msg store.Message, w int) []string {
	th, now := m.th, m.now
	bar := " "
	mention := !msg.Out && m.chat != nil && m.chat.Mentions(msg.Text)
	switch {
	case msg.ID == m.chats.focus:
		bar = th.Fg(th.Cyan, true).Render("▌")
	case mention:
		bar = th.Fg(th.Magenta, true).Render("▌")
	}
	if at, ok := m.chats.arrived[msg.ID]; ok && m.level.Allows(motion.Flash) && motion.Running(at, arriveDur, now) {
		bar = th.Fg(th.Magenta.Lerp(th.Bg, motion.Progress(at, arriveDur, now)), true).Render("▌")
	}
	stamp := th.Fg(th.Dim, false).Render(msg.RecvAt.Local().Format("15:04") + " ")
	nameCol := th.Cyan
	if msg.Out {
		nameCol = th.Magenta
	}
	name := th.Fg(nameCol, true).Render(msg.Sender)
	meta := m.messageMeta(msg)

	text := msg.Text
	if at, ok := m.chats.arrived[msg.ID]; ok && m.level.Allows(motion.Decode) && motion.Running(at, decodeDur, now) {
		text = motion.DecodeText(text, msg.ID, motion.Progress(at, decodeDur, now))
	}
	if at, ok := m.chats.failed[msg.ID]; ok && m.level.Allows(motion.Glitch) && motion.Running(at, glitchDur*3, now) {
		text = motion.GlitchText(text, msg.ID, int(now.Sub(at)/motion.FrameDur))
	}
	headLine := bar + stamp + name
	gap := w - visibleWidth(headLine) - visibleWidth(meta)
	out := []string{headLine + strings.Repeat(" ", max(gap, 1)) + meta}
	for _, l := range strings.Split(ansi.Wrap(text, max(w-8, 10), ""), "\n") {
		out = append(out, bar+"      "+m.highlight(l, m.chats.filter, th.Text))
	}
	return out
}

// messageMeta is the right-hand detail: route and signal for received messages, delivery state for sent ones.
func (m *Model) messageMeta(msg store.Message) string {
	th, now := m.th, m.now
	if !msg.Out {
		parts := []string{}
		if msg.Hops < 0 {
			parts = append(parts, "direct")
		} else {
			parts = append(parts, fmt.Sprintf("%d hops", msg.Hops))
		}
		meta := th.Fg(th.Dim, false).Render(strings.Join(parts, " "))
		if msg.HasSNR {
			meta += th.Fg(th.Dim, false).Render(" · ") + th.Fg(th.SNR(float64(msg.SNR)), false).Render(fmt.Sprintf("SNR %+.2f", msg.SNR))
		}
		if msg.Signed {
			meta += th.Fg(th.Violet, true).Render(" · signed")
		}
		return meta
	}
	ramp := func(label string) string {
		r := "…"
		if m.level.Allows(motion.Ramp) {
			r = motion.RampBar(now, 3)
		}
		return th.Fg(th.Yellow, true).Render(r) + th.Fg(th.Yellow, false).Render(" "+label)
	}
	switch msg.State {
	case store.Pending:
		if msg.Attempts > 0 {
			return ramp(fmt.Sprintf("retrying · try %d", msg.Attempts+1))
		}
		return ramp("sending")
	case store.Sent:
		if strings.HasPrefix(string(msg.Convo), "ch:") {
			return th.Fg(th.Dim, false).Render("sent")
		}
		how := "direct"
		if msg.Flood {
			how = "flood"
		}
		return ramp(fmt.Sprintf("awaiting ack · %s · try %d", how, msg.Attempts))
	case store.Delivered:
		popping := motion.Running(m.chats.acked[msg.ID], popDur, now) && m.level.Allows(motion.Flash)
		col := th.Green
		if popping {
			col = th.Text.Lerp(th.Green, motion.Progress(m.chats.acked[msg.ID], popDur, now))
		}
		return th.Fg(col, true).Render("✓") + th.Fg(th.Green, popping).Render(" "+seconds(msg.RTT))
	case store.Failed:
		return th.Fg(th.Red, true).Render(fmt.Sprintf("[FAIL] not delivered after %d tries", max(msg.Attempts, 1)))
	}
	return ""
}
