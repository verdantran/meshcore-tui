package ui

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/packets"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

const (
	listenFor  = 30 * time.Second // after this, a channel message with no repeats heard says so
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
	input   lineEdit
	msgs    []store.Message
	scroll  int // messages hidden below the bottom of the pane

	filter    lineEdit // "/" narrows the open conversation to matching messages
	filtering bool     // typing the filter
	focus     int64    // a message jumped to from search, marked until the next move

	reading bool  // moving through the open conversation's messages, to reply to one or see its route
	cur     int64 // the message picked while reading

	heard []heardMessage // message packets heard lately, for giving messages their paths

	sigs     []pendingSig         // signatures waiting for their message
	signFlip bool                 // ctrl+s: sign, or don't, against the channel's setting, for this message
	pins     map[string]store.Pin // keys names are trusted to sign with; nil until read
	pinsRev  int
	names    nameIndex

	signedMsgs map[string]store.Message // the message each signature heard was for, by channel and timestamp
	queued     map[int64]time.Time      // channel messages the radio held while the app was away, while their signature may come
	usedSigs   map[[64]byte]usedSig     // valid signatures lately, against replays

	arrived map[int64]time.Time       // incoming messages, for the decode and the bar flash
	acked   map[int64]time.Time       // for the ✓ pop
	echoed  map[int64]time.Time       // a channel message's latest repeat, for its flash
	failed  map[int64]time.Time       // for the glitch
	popped  map[store.Convo]time.Time // when a conversation's unread count went up, for its badge's pop
	unread  int

	draft store.Convo // a conversation started from Contacts, listed until it's left without a message

	stale     bool // the list needs rebuilding once the message being handled is done with
	staleOpen bool // and the open conversation reloading

	mention  *mentionCycle // tab completing an @name in the compose box
	emoji    *emojiPick    // the emoji offered for a :shortcode being typed
	emojiOff int           // 1 + where a shortcode's list was closed with esc, so it stays closed

	chanStage chanStage // adding, sharing or removing a channel
	chanInput lineEdit
	chanName  string
	shareCh   radio.Channel
}

func newChats() chatsModel {
	return chatsModel{arrived: map[int64]time.Time{}, acked: map[int64]time.Time{}, failed: map[int64]time.Time{}, echoed: map[int64]time.Time{},
		signedMsgs: map[string]store.Message{}, queued: map[int64]time.Time{}, usedSigs: map[[64]byte]usedSig{}, popped: map[store.Convo]time.Time{}}
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

// refreshChats rebuilds the conversation list and reloads the open conversation, marking it read if it's on screen.
func (m *Model) refreshChats() { m.rebuildChats(true) }

// chatsChanged notes a change in a conversation, for Update to apply once: the list is rebuilt, and the
// open conversation reloaded only if it's the one that changed.
func (m *Model) chatsChanged(cv store.Convo) {
	m.chats.stale = true
	if cur, ok := m.selectedConvo(); ok && cur.Convo == cv {
		m.chats.staleOpen = true
	}
}

func (m *Model) flushChats() {
	if m.chats.stale {
		m.rebuildChats(m.chats.staleOpen)
	}
}

func (m *Model) rebuildChats(reload bool) {
	if m.chat == nil || m.chat.Device() == "" {
		return
	}
	c := &m.chats
	c.stale, c.staleOpen = false, false
	var keep store.Convo
	if cur, ok := m.selectedConvo(); ok {
		keep = cur.Convo
	}
	if keep != c.draft {
		c.draft = "" // moved away from a conversation that was never written in
	}
	infos, err := m.chat.Conversations(c.draft)
	if err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
		return
	}
	was := map[store.Convo]int{}
	for _, info := range c.infos {
		was[info.Convo] = info.Unread
	}
	for _, info := range infos {
		if info.Unread > was[info.Convo] {
			c.popped[info.Convo] = m.now
		}
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
	if m.phase == phaseMain && m.visible(0) && cur.Unread > 0 {
		m.chat.MarkRead(cur.Convo)
		c.infos[c.sel].Unread, c.infos[c.sel].Mention = 0, false
	}
	switch {
	case !reload && cur.Convo == keep:
	case c.filter.text != "":
		found, _ := m.chat.Search(c.filter.text, cur.Convo, historyLen)
		c.msgs = make([]store.Message, len(found))
		for i, f := range found {
			c.msgs[len(found)-1-i] = f // search returns newest first; the pane wants oldest first
		}
	default:
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
		m.moveSigning()
		m.refreshChats()
	case radio.Lost:
		m.chat.Lost()
	case radio.Incoming:
		if e.CLI {
			return nil // console replies belong to the admin screen
		}
		msg, err := m.chat.Incoming(e)
		if err != nil {
			m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
			return nil
		}
		flash(m.chats.arrived, msg.ID, m.now, arriveDur)
		if e.Queued && e.Channel {
			flash(m.chats.queued, msg.ID, m.now, sigHold)
		}
		m.routeArrived(msg)
		m.sigArrived(msg)
		title := m.chat.Title(msg.Convo)
		tone := radio.ToneInfo
		if m.chat.Mentions(msg.Text) {
			tone = radio.ToneWarn
		}
		m.addLog(m.now, fmt.Sprintf("MSG  %s · %s: %s", title, msg.Sender, truncate(msg.Text, 60)), tone)
		m.chatsChanged(msg.Convo)
		return m.considerNote(msg, title)
	case radio.ChannelData:
		m.onChannelData(e)
	case radio.ContactUpdated:
		known := false
		for _, ct := range m.chat.Contacts() {
			known = known || ct.Key == e.Contact.Key
		}
		m.contacts.arrived[e.Contact.Key] = entrance{at: m.now, slide: !known}
		m.chat.ContactUpdated(e.Contact)
		m.chats.stale = m.chats.stale || m.relists(e.Contact)
	case radio.Acked:
		msg, ok, _ := m.chat.Acked(e)
		if ok {
			flash(m.chats.acked, msg.ID, m.now, popDur)
			m.addLog(m.now, fmt.Sprintf("DELIVERED  ✓ %s  %s", m.chat.Title(msg.Convo), seconds(msg.RTT)), radio.ToneOK)
			m.chatsChanged(msg.Convo)
		}
	}
	return nil
}

// relists reports whether an advert changes how a contact's conversation is listed.
func (m *Model) relists(ct radio.Contact) bool {
	cv := store.ContactConvo(ct.Key)
	if cv == m.chats.draft {
		return true
	}
	for _, info := range m.chats.infos {
		if info.Convo == cv {
			return info.Title != ct.Name || (info.Kind == chat.KindRoom) != (ct.Type == radio.TypeRoom)
		}
	}
	return false
}

// sendConvo is the conversation a send is in.
func sendConvo(s chat.Send) store.Convo {
	if s.Channel {
		return store.ChannelConvo(s.Idx)
	}
	return store.ContactConvo(s.To)
}

func (m *Model) chatResult(r sendResultMsg) tea.Cmd {
	msg, wait, err := m.chat.Result(r.send, r.sent, r.err)
	if err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
	}
	if msg.State == store.Failed && msg.ID != 0 {
		flash(m.chats.failed, msg.ID, m.now, glitchDur*3)
		m.fail(fmt.Sprintf("SEND FAILED  %s: %s", m.chat.Title(msg.Convo), radio.Explain(r.err)))
	}
	m.chatsChanged(sendConvo(r.send))
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
	if msg.ID != 0 {
		m.chatsChanged(msg.Convo)
	}
	switch {
	case next != nil:
		how := "retrying"
		if next.ResetPath {
			how = "retrying by flood"
		}
		m.addLog(m.now, fmt.Sprintf("NO ACK  %s  %s (try %d)", m.chat.Title(msg.Convo), how, next.Attempt+1), radio.ToneWarn)
		return m.sendCmd(*next)
	case msg.State == store.Failed:
		flash(m.chats.failed, msg.ID, m.now, glitchDur*3)
		m.fail(fmt.Sprintf("NOT DELIVERED  %s after %d tries", m.chat.Title(msg.Convo), msg.Attempts))
	}
	return nil
}

func (m *Model) composeSend() tea.Cmd {
	cur, ok := m.selectedConvo()
	if !ok || m.chat == nil {
		return nil
	}
	sign := m.signThis()
	if err := m.canSign(); sign && err != nil {
		m.addLog(m.now, "SENDING UNSIGNED  "+err.Error(), radio.ToneWarn)
		sign = false
	}
	_, send, err := m.chat.Compose(cur.Convo, m.chats.input.text, m.now)
	if err != nil {
		m.fail(err.Error())
		return nil
	}
	m.chats.input, m.chats.scroll, m.chats.signFlip, m.chats.emoji = lineEdit{}, 0, false, nil
	m.refreshChats()
	if sign {
		return m.signedSendCmd(send, cur.Title)
	}
	return m.sendCmd(send)
}

// retry sends a failed message again, signed if the channel's messages are.
func (m *Model) retry(msg store.Message) tea.Cmd {
	cur, ok := m.selectedConvo()
	if !ok || m.chat == nil {
		return nil
	}
	_, send, err := m.chat.Retry(msg, m.now)
	if err != nil {
		m.fail("RETRY  " + err.Error())
		return nil
	}
	delete(m.chats.failed, msg.ID)
	m.chats.reading, m.chats.scroll = false, 0
	m.refreshChats()
	m.addLog(m.now, "RETRYING  "+cur.Title, radio.ToneInfo)
	if cur.Kind == chat.KindChannel && m.signedChannel(cur.Convo) && m.canSign() == nil {
		return m.signedSendCmd(send, cur.Title)
	}
	return m.sendCmd(send)
}

func (m *Model) retryHint() string {
	if i := m.picked(); i >= 0 && m.chats.msgs[i].Out && m.chats.msgs[i].State == store.Failed {
		return m.th.Fg(m.th.Cyan, true).Render("R") + m.th.Fg(m.th.Text, false).Render(" send again · ")
	}
	return ""
}

// newestFailed is the newest message of yours in view that failed, or -1.
func (m *Model) newestFailed() int {
	c := &m.chats
	for i := len(c.msgs) - 1 - c.scroll; i >= 0 && i < len(c.msgs); i-- {
		if c.msgs[i].Out && c.msgs[i].State == store.Failed {
			return i
		}
	}
	return -1
}

// chatsKey handles keys on the Chats screen. It returns handled=false for keys the screen leaves to the app.
func (m *Model) chatsKey(s string) (tea.Cmd, bool) {
	c := &m.chats
	if c.chanStage != chanNone {
		return m.channelKey(s)
	}
	if c.filtering {
		filterKey(&c.filter, &c.filtering, s)
		c.scroll = 0
		m.refreshChats()
		return nil, true
	}
	if c.reading {
		if cmd, handled := m.readingKey(s); handled {
			return cmd, true
		}
	}
	if c.writing {
		if m.emojiKey(s) {
			return nil, true
		}
		switch s {
		case "ctrl+s":
			if cur, ok := m.selectedConvo(); ok && cur.Kind == chat.KindChannel {
				c.signFlip = !c.signFlip
			}
		case "esc":
			c.writing, c.signFlip, c.emoji = false, false, nil
		case "tab":
			m.completeMention()
		case "enter":
			return m.composeSend(), true
		default:
			c.input.key(s)
			m.updateEmoji()
		}
		return nil, true
	}
	switch s {
	case "/":
		c.filtering, c.filter = true, lineEdit{}
	case "esc":
		if c.filter.text == "" {
			return nil, false
		}
		c.filter = lineEdit{}
		m.refreshChats()
	case "up", "k":
		if c.sel > 0 {
			c.sel--
			c.scroll, c.focus, c.filter, c.reading = 0, 0, lineEdit{}, false
			m.refreshChats()
		}
	case "down", "j":
		if c.sel < len(c.infos)-1 {
			c.sel++
			c.scroll, c.focus, c.filter, c.reading = 0, 0, lineEdit{}, false
			m.refreshChats()
		}
	case "enter", "i":
		if len(c.infos) > 0 {
			c.writing = true
		}
	case "n", "e", "x":
		return m.channelKey(s)
	case "r":
		if i := m.newestReceived(); i >= 0 {
			c.reading, c.cur = true, c.msgs[i].ID
			return m.readingKey("r")
		}
		m.addLog(m.now, "REPLY  nothing received here to reply to", radio.ToneInfo)
	case "g", "home":
		if c.sel != 0 && len(c.infos) > 0 {
			c.sel, c.scroll, c.focus, c.filter, c.reading = 0, 0, 0, lineEdit{}, false
			m.refreshChats()
		}
	case "G", "end":
		if last := len(c.infos) - 1; c.sel != last && last >= 0 {
			c.sel, c.scroll, c.focus, c.filter, c.reading = last, 0, 0, lineEdit{}, false
			m.refreshChats()
		}
	case "L":
		ct, ok := m.selectedRoom()
		if !ok {
			return nil, false
		}
		if sess, ok := m.admin.Find(ct.Key); ok && sess.State == admin.LoggedIn {
			return m.logout(ct), true
		}
		m.loginFromChats(ct)
		return nil, true
	case "right":
		if len(c.msgs) > 0 {
			c.reading, c.cur = true, c.msgs[max(len(c.msgs)-1-c.scroll, 0)].ID
		}
	case "M":
		if i := m.newestReceived(); i >= 0 {
			m.showMessagePath(c.msgs[i])
		}
	case "R":
		if i := m.newestFailed(); i >= 0 {
			return m.retry(c.msgs[i]), true
		}
		m.addLog(m.now, "RETRY  nothing here failed to send", radio.ToneInfo)
	case "S":
		m.toggleChannelSigning()
	case "pgup", "ctrl+u":
		c.scroll = min(c.scroll+5, max(len(c.msgs)-1, 0))
	case "pgdown", "ctrl+d":
		c.scroll = max(c.scroll-5, 0)
	default:
		return nil, false
	}
	return nil, true
}

// newestReceived is the newest received message in view, or -1.
func (m *Model) newestReceived() int {
	c := &m.chats
	for i := len(c.msgs) - 1 - c.scroll; i >= 0 && i < len(c.msgs); i-- {
		if !c.msgs[i].Out {
			return i
		}
	}
	return -1
}

// picked is the index of the message picked while reading, or -1 if it's gone (filtered out, say).
func (m *Model) picked() int {
	for i, msg := range m.chats.msgs {
		if msg.ID == m.chats.cur {
			return i
		}
	}
	return -1
}

// readingKey moves through the open conversation's messages: ↑↓ pick one, r replies to it, M shows its route.
func (m *Model) readingKey(s string) (tea.Cmd, bool) {
	c := &m.chats
	i := m.picked()
	if i < 0 {
		c.reading = false
		return nil, false
	}
	switch s {
	case "up", "k":
		i = max(i-1, 0)
	case "down", "j":
		i = min(i+1, len(c.msgs)-1)
	case "g", "home":
		i = 0
	case "G", "end":
		i = len(c.msgs) - 1
	case "left", "esc":
		c.reading = false
		return nil, true
	case "R":
		if msg := c.msgs[i]; msg.Out && msg.State == store.Failed {
			return m.retry(msg), true
		}
		m.addLog(m.now, "RETRY  only a message that failed can be sent again", radio.ToneInfo)
		return nil, true
	case "r", "enter":
		msg := c.msgs[i]
		if msg.Out && msg.State == store.Failed && s == "enter" {
			return m.retry(msg), true
		}
		c.reading, c.writing, c.input = false, true, lineEdit{}
		if cur, ok := m.selectedConvo(); ok && cur.Kind == chat.KindChannel && !msg.Out {
			c.input.set("@[" + msg.Sender + "] ") // in a channel, a reply names who it's to
		}
		return nil, true
	case "M":
		m.showMessagePath(c.msgs[i])
		return nil, true
	case "K":
		m.pinPicked(c.msgs[i])
		return nil, true
	case "y":
		msg := c.msgs[i]
		m.addLog(m.now, "COPIED  "+msg.Sender+"'s message, to the clipboard", radio.ToneOK)
		return m.copyText(untrusted.Text(msg.Text)), true
	default:
		return nil, false
	}
	c.cur = c.msgs[i].ID
	if below := len(c.msgs) - 1 - i; below < c.scroll {
		c.scroll = below // it was hidden under the bottom of the pane
	}
	return nil, true
}

// flash marks a message for an animation lasting d, dropping marks whose animation is over.
func flash(marks map[int64]time.Time, id int64, now time.Time, d time.Duration) {
	for k, at := range marks {
		if now.Sub(at) >= d {
			delete(marks, k)
		}
	}
	marks[id] = now
}

const maxSignedMsgs = 2000

// trimSigned forgets which message old signatures were for, once there are many.
func (m *Model) trimSigned() {
	if len(m.chats.signedMsgs) <= maxSignedMsgs {
		return
	}
	for k, msg := range m.chats.signedMsgs {
		if m.now.Sub(msg.RecvAt) > time.Hour {
			delete(m.chats.signedMsgs, k)
		}
	}
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
	for _, t := range m.chats.echoed {
		if motion.Running(t, popDur, now) {
			return true
		}
	}
	for _, t := range m.chats.popped {
		if motion.Running(t, popDur, now) {
			return true
		}
	}
	if m.visible(0) {
		for _, msg := range m.chats.msgs {
			channel := strings.HasPrefix(string(msg.Convo), "ch:")
			listening := channel && msg.State == store.Sent && msg.Repeats == 0 && now.Sub(msg.SentAt) < listenFor
			waiting := msg.State == store.Pending || msg.State == store.Sent && !channel || listening
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
	if c.chanStage != chanNone && m.chat != nil {
		return m.channelLines(inner, rows)
	}
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
			badge = m.unreadBadge(info)
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
		if m.signedChannel(cur.Convo) {
			route += " · ✍ signing your messages (S)"
		}
	default:
		route = "not a contact · can't reply"
		if ct, ok := m.chat.Contact(cur.Convo); ok {
			route = "flood (no stored route)"
			if ct.PathLen >= 0 {
				route = "direct · " + strings.TrimSpace(plural(ct.PathLen, "hop")+" "+pathText(ct.Path, 1)) + " · M shows it"
			}
		}
	}
	head := title + th.Fg(th.Dim, false).Render("  "+route)
	if c.filter.text != "" || c.filtering {
		head = title + th.Fg(th.Yellow, true).Render("  / ") + filterText(th, c.filter, c.filtering, 24) +
			th.Fg(th.Dim, false).Render(fmt.Sprintf("  · %d matching · esc clear", len(c.msgs)))
	}

	// Compose line, always at the bottom.
	used, limit := m.composeLoad()
	counterCol := th.Dim
	switch {
	case used > limit:
		counterCol = th.Red
	case used*10 >= limit*9:
		counterCol = th.Yellow
	}
	if counterCol != th.Dim && m.level.Allows(motion.Flash) {
		counterCol = counterCol.Lerp(th.Bg, 0.55*motion.Pulse(m.now, pulsePeriod))
	}
	counter := th.Fg(counterCol, used*10 >= limit*9).Render(fmt.Sprintf("%d/%d", used, limit))
	if hint := m.mentionHint(); hint != "" {
		counter = th.Fg(th.Cyan, false).Render(hint) + counter
	}
	if c.writing && cur.Kind == chat.KindChannel {
		if m.signThis() {
			counter = th.Fg(th.Green, true).Render("✍ signed ") + th.Fg(th.Dim, false).Render("ctrl+s ") + counter
		} else {
			counter = th.Fg(th.Dim, false).Render("ctrl+s signs ") + counter
		}
	}
	var compose string
	if c.writing {
		compose = th.Fg(th.Magenta, true).Render("▸ ") + c.input.render(th, w-3-visibleWidth(counter), false)
	} else {
		compose = th.Fg(th.Dim, false).Render("▸ enter write · ↑↓ conversations · → pick a message · r reply · / filter · ctrl+f search all")
		if c.reading {
			compose = th.Fg(th.Magenta, true).Render("▸ ") + th.Fg(th.Text, false).Render("↑↓ messages · ") +
				th.Fg(th.Cyan, true).Render("r") + th.Fg(th.Text, false).Render(" reply · ") +
				m.retryHint() +
				th.Fg(th.Cyan, true).Render("M") + th.Fg(th.Text, false).Render(" route on the map · ") +
				th.Fg(th.Cyan, true).Render("y") + th.Fg(th.Text, false).Render(" copy · ") +
				th.Fg(th.Dim, false).Render("← back to conversations")
		}
	}
	compose = fit(compose, w-visibleWidth(counter)-1) + " " + counter

	// Messages, newest at the bottom.
	var lines []string
	for _, msg := range c.msgs {
		lines = append(lines, m.messageLines(msg, w)...)
	}
	avail := rows - 3
	if p := m.picked(); c.reading && p >= 0 {
		// Scroll up just far enough to show the picked message when it's above the top of the pane.
		height := func(i int) int { return len(m.messageLines(c.msgs[i], w)) }
		need, hidden := 0, 0
		for i := p; i < len(c.msgs); i++ {
			need += height(i)
		}
		for i := len(c.msgs) - c.scroll; i < len(c.msgs); i++ {
			hidden += height(i)
		}
		for c.scroll < len(c.msgs)-1-p && need-hidden > avail {
			c.scroll++
			hidden += height(len(c.msgs) - c.scroll)
		}
	}
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
		if c.filter.text != "" {
			body = []string{th.Fg(th.Dim, false).Render("Nothing here matches.")}
		}
	}
	out := []string{head, m.roomLine(w)}
	for i := 0; i < avail-len(body); i++ {
		out = append(out, "")
	}
	out = append(out, body...)
	if c.writing && c.emoji != nil {
		pick := m.emojiLines(w)
		if len(pick) < len(out)-2 {
			out = append(out[:len(out)-len(pick)], pick...)
		}
	}
	return append(out, compose)
}

func (m *Model) messageLines(msg store.Message, w int) []string {
	th, now := m.th, m.now
	bar := " "
	mention := !msg.Out && m.chat != nil && m.chat.Mentions(msg.Text)
	picked := m.chats.reading && msg.ID == m.chats.cur
	switch {
	case picked:
		bar = th.Fg(th.Magenta, true).Render("▶")
	case msg.ID == m.chats.focus:
		bar = th.Fg(th.Cyan, true).Render("▌")
	case mention:
		bar = th.Fg(th.Magenta, true).Render("▌")
	}
	if at, ok := m.chats.arrived[msg.ID]; ok && m.level.Allows(motion.Flash) && motion.Running(at, arriveDur, now) {
		bar = th.Fg(th.Magenta.Lerp(th.Bg, motion.Progress(at, arriveDur, now)), true).Render("▌")
	}
	stamp := th.Fg(th.Dim, false).Render(msg.RecvAt.Local().Format("15:04") + " ")
	nameCol := m.nameColour(msg.Sender)
	if msg.Out {
		nameCol = th.Magenta
	}
	meta := m.messageMeta(msg)
	if picked {
		if _, pinned := m.pinOf(msg.Sender); msg.Sig == store.SigValid && !msg.Out && !pinned {
			meta = th.Fg(th.Cyan, true).Render("K") + th.Fg(th.Dim, false).Render(" pin key · ") + meta
		}
		meta = th.Fg(th.Cyan, true).Render("r") + th.Fg(th.Dim, false).Render(" reply · ") +
			th.Fg(th.Cyan, true).Render("M") + th.Fg(th.Dim, false).Render(" route · ") +
			th.Fg(th.Cyan, true).Render("y") + th.Fg(th.Dim, false).Render(" copy   ") + meta
	}

	text, marks := emphasis(msg.Text)
	if at, ok := m.chats.arrived[msg.ID]; ok && m.level.Allows(motion.Decode) && motion.Running(at, decodeDur, now) {
		text, marks = motion.DecodeText(text, msg.ID, motion.Progress(at, decodeDur, now)), nil
	}
	if at, ok := m.chats.failed[msg.ID]; ok && m.level.Allows(motion.Glitch) && motion.Running(at, glitchDur*3, now) {
		text, marks = motion.GlitchText(text, msg.ID, int(now.Sub(at)/motion.FrameDur)), nil
	}
	room := w - visibleWidth(bar+stamp) - visibleWidth(meta) - 1
	headLine := bar + stamp + th.Fg(nameCol, true).Render(clip(msg.Sender, max(min(senderMax, room), 4)))
	gap := w - visibleWidth(headLine) - visibleWidth(meta)
	out := []string{headLine + strings.Repeat(" ", max(gap, 1)) + meta}
	lines, lineMarks := wrapEmph(text, marks, max(w-8, 10), func(s string, w int) string { return renderWidth.Wrap(s, w, "") })
	for i, l := range lines {
		out = append(out, bar+"      "+m.highlight(l, m.chats.filter.text, th.Text, lineMarks[i]))
	}
	if picked {
		for i := range out {
			out[i] = m.rowBackground(out[i], w)
		}
	}
	return out
}

// rowBackground lays the raised colour under a whole styled row, out to its full width, keeping the colours
// of what's on it. In the mono theme it reverses the row instead.
func (m *Model) rowBackground(row string, w int) string {
	row = fit(row, w)
	if m.th.Mono {
		return "\x1b[7m" + strings.NewReplacer("\x1b[0m", "\x1b[0m\x1b[7m", "\x1b[m", "\x1b[m\x1b[7m").Replace(row) + "\x1b[0m"
	}
	r := m.th.Raised
	bg := fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r.R, r.G, r.B)
	return bg + strings.NewReplacer("\x1b[0m", "\x1b[0m"+bg, "\x1b[m", "\x1b[m"+bg, "\x1b[49m", bg).Replace(row) + "\x1b[0m"
}

// messageMeta is the right-hand detail: route and signal for received messages, delivery state for sent ones.
func (m *Model) messageMeta(msg store.Message) string {
	th, now := m.th, m.now
	if !msg.Out {
		parts := []string{}
		switch {
		case msg.Hops < 0:
			parts = append(parts, "direct")
		case len(msg.Path) > 0:
			parts = append(parts, plural(msg.Hops, "hop")+" via "+pathText(msg.Path, msg.PathHash))
		default:
			parts = append(parts, plural(msg.Hops, "hop"))
		}
		meta := th.Fg(th.Dim, false).Render(strings.Join(parts, " "))
		if msg.HasSNR {
			meta += th.Fg(th.Dim, false).Render(" · ") + th.Fg(th.SNR(float64(msg.SNR)), false).Render(fmt.Sprintf("SNR %+.2f", msg.SNR))
		}
		if msg.Signed {
			meta += m.roomAuthor(msg)
		}
		if b := m.sigBadge(msg); b != "" {
			meta += th.Fg(th.Dim, false).Render(" · ") + b
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
			if b := m.sigBadge(msg); b != "" {
				return b + th.Fg(th.Dim, false).Render(" · ") + m.repeatsMeta(msg)
			}
			return m.repeatsMeta(msg)
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
		return th.Fg(th.Red, true).Render(fmt.Sprintf("[FAIL] not delivered after %d tries", max(msg.Attempts, 1))) +
			th.Fg(th.Dim, false).Render(" · R sends it again")
	}
	return ""
}

// openDraft lists and selects a conversation with a contact who hasn't been messaged yet.
func (m *Model) openDraft(cv store.Convo) {
	c := &m.chats
	c.draft = cv
	if infos, err := m.chat.Conversations(cv); err == nil {
		c.infos = infos
		for i, info := range infos {
			if info.Convo == cv {
				c.sel = i
			}
		}
	}
	c.scroll = 0
	m.refreshChats()
}

// repeatsMeta says whether a channel message we sent was heard coming back from repeaters.
func (m *Model) repeatsMeta(msg store.Message) string {
	th, now := m.th, m.now
	switch {
	case msg.Repeats > 0:
		label := fmt.Sprintf("✓ sent · heard %d repeats", msg.Repeats)
		if msg.Repeats == 1 {
			label = "✓ sent · heard 1 repeat"
		}
		col := th.Green
		if at := m.chats.echoed[msg.ID]; m.level.Allows(motion.Flash) && motion.Running(at, popDur, now) {
			col = th.Text.Lerp(th.Green, motion.Progress(at, popDur, now))
		}
		return th.Fg(col, true).Render(label)
	case now.Sub(msg.SentAt) < listenFor:
		r := "…"
		if m.level.Allows(motion.Ramp) {
			r = motion.RampBar(now, 3)
		}
		return th.Fg(th.Cyan, true).Render(r) + th.Fg(th.Cyan, false).Render(" sent · listening for repeats")
	}
	return th.Fg(th.Yellow, false).Render("sent · no repeats heard")
}

// heardEcho counts a heard copy of a channel message we sent.
func (m *Model) heardEcho(dec packets.Decoded) {
	ch, ok := m.packetChannel(dec)
	if !ok {
		return
	}
	id, _, ok, err := m.chat.Echo(ch.Idx, dec.Sender, dec.Text, m.now)
	if err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
	}
	if ok {
		flash(m.chats.echoed, id, m.now, popDur)
		m.chatsChanged(store.ChannelConvo(ch.Idx))
	}
}

// mentionCycle is a tab completion in progress: the names that fit, and which one is showing.
type mentionCycle struct {
	at      int // where the @ is in the input
	matches []string
	i       int
	last    lineEdit // the input as completed, so a further tab cycles rather than starting again
}

// mentionNames are the people who can be mentioned here: contacts, and anyone who has written in
// this conversation, since most people on a channel aren't contacts.
func (m *Model) mentionNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if k := strings.ToLower(name); name != "" && !seen[k] {
			seen[k] = true
			out = append(out, name)
		}
	}
	for i := len(m.chats.msgs) - 1; i >= 0; i-- {
		if msg := m.chats.msgs[i]; !msg.Out {
			add(msg.Sender) // most recent first: who you're likely replying to
		}
	}
	if m.chat != nil {
		for _, ct := range m.chat.Contacts() {
			if ct.Type == radio.TypeChat {
				add(ct.Name)
			}
		}
	}
	return out
}

// nameMatches reports whether what's been typed after @ could be the start of a name. Names often begin
// with an emoji, which is awkward to type, so the letters can start at any word, or after the emoji.
func nameMatches(name, typed string) bool {
	name, typed = strings.ToLower(name), strings.ToLower(typed)
	if strings.HasPrefix(name, typed) {
		return true
	}
	if strings.HasPrefix(strings.TrimLeftFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), typed) {
		return true
	}
	for _, word := range strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if strings.HasPrefix(word, typed) {
			return true
		}
	}
	return false
}

// nameColour gives each sender their own colour, the same every time, from the theme's colours other than
// magenta, which is yours, and red, which means trouble.
func (m *Model) nameColour(name string) theme.RGB {
	th := m.th
	colours := []theme.RGB{th.Cyan, th.Green, th.Yellow, th.Violet, th.Cyan.Lerp(th.Green, 0.5), th.Yellow.Lerp(th.Red, 0.4),
		th.Violet.Lerp(th.Cyan, 0.5), th.Green.Lerp(th.Yellow, 0.5), th.Violet.Lerp(th.Magenta, 0.35), th.Cyan.Lerp(th.Text, 0.45)}
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(name)))
	return colours[h.Sum32()%uint32(len(colours))]
}

// completeMention turns the @name being typed into @[Name], the form the MeshCore apps recognise.
func (m *Model) completeMention() {
	c := &m.chats
	if mc := c.mention; mc != nil && c.input == mc.last && len(mc.matches) > 1 {
		mc.i = (mc.i + 1) % len(mc.matches)
		c.input.replace(mc.at, m.mentionText(mc.matches[mc.i]))
		mc.last = c.input
		return
	}
	at := strings.LastIndex(c.input.before(), "@")
	if at < 0 {
		return
	}
	partial := c.input.before()[at+1:]
	bracket := strings.HasPrefix(partial, "[")
	partial = strings.TrimPrefix(partial, "[")
	if strings.Contains(partial, "]") || !bracket && strings.Contains(partial, " ") {
		return // the @ is already finished, or this is past it
	}
	var matches []string
	for _, name := range m.mentionNames() {
		if nameMatches(name, partial) {
			matches = append(matches, name)
		}
	}
	if len(matches) == 0 {
		return
	}
	c.input.replace(at, m.mentionText(matches[0]))
	c.mention = &mentionCycle{at: at, matches: matches, last: c.input}
}

// mentionText is a completed @name, with a space after unless the text after the cursor brings one.
func (m *Model) mentionText(name string) string {
	if strings.HasPrefix(m.chats.input.text[m.chats.input.pos:], " ") {
		return "@[" + name + "]"
	}
	return "@[" + name + "] "
}

// mentionHint tells the writer more names fit, while a completion is showing.
func (m *Model) mentionHint() string {
	c := &m.chats
	if mc := c.mention; mc != nil && c.input == mc.last && len(mc.matches) > 1 {
		return fmt.Sprintf("tab: %d of %d names ", mc.i+1, len(mc.matches))
	}
	return ""
}

// composeLoad is the bytes typed against what the conversation takes.
func (m *Model) composeLoad() (used, limit int) {
	cur, ok := m.selectedConvo()
	if ok && m.chat != nil {
		limit = m.chat.MaxBytes(cur.Convo)
	}
	return len(m.chats.input.text), limit
}

// unreadBadge is a conversation's unread count; a new message makes it pop, ▪ then ■ then the number,
// at the width it ends at so the name beside it holds still.
func (m *Model) unreadBadge(info chat.Info) string {
	th := m.th
	text := fmt.Sprintf(" %d ", info.Unread)
	at := m.chats.popped[info.Convo]
	if !m.level.Allows(motion.Flash) || !motion.Running(at, popDur, m.now) {
		return th.Block(th.Bg, th.Magenta).Render(text)
	}
	switch p := motion.Progress(at, popDur, m.now); {
	case p < 1.0/3:
		return th.Fg(th.Magenta, true).Render(fit(" ▪", len(text)))
	case p < 2.0/3:
		return th.Fg(th.Magenta, true).Render(fit(" ■", len(text)))
	}
	return th.Block(th.Bg, th.Magenta.Lerp(th.Text, 0.4)).Render(text)
}
