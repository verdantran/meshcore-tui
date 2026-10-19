package ui

import (
	"context"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/packets"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

// scriptRadio records sends and acks nothing on its own; tests deliver acks as events.
type scriptRadio struct {
	stubRadio
	failSend error
	mu       sync.Mutex
	texts    []string
	resets   int
	chans    []string
	ack      uint32
}

func (r *scriptRadio) SendText(_ context.Context, _ [32]byte, text string) (radio.Sent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failSend != nil {
		return radio.Sent{}, r.failSend
	}
	r.ack++
	r.texts = append(r.texts, text)
	return radio.Sent{Ack: r.ack, Timeout: 20 * time.Millisecond}, nil
}

func (r *scriptRadio) SendChannel(_ context.Context, _ int, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chans = append(r.chans, text)
	return nil
}

func (r *scriptRadio) ResetPath(context.Context, [32]byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resets++
	return nil
}

var aliceKey = [32]byte{0x3A, 0xAA, 0x01}

var chatReady = radio.Ready{
	Self:   radio.SelfInfo{Name: "MeshCore-k4", PubKey: [32]byte{0xC0, 0x4F}, FreqKHz: 869618, BwHz: 62500, SF: 8},
	Device: radio.DeviceInfo{Model: "Heltec V4", Firmware: "v1.17.1"},
	ContactList: []radio.Contact{
		{Key: aliceKey, Name: "Alice", Type: radio.TypeChat, PathLen: 2, Path: []byte{0xA3, 0x1E}},
		{Key: [32]byte{0xA3}, Name: "Dundry", Type: radio.TypeRepeater, PathLen: -1},
	},
	ChannelList: []radio.Channel{{Idx: 0, Name: "Public"}, {Idx: 1, Name: "#bristol"}},
	Contacts:    2, Channels: 2, Battery: radio.Battery{MilliVolts: 4020},
}

func newChatModel(t *testing.T) (*Model, *clock, *scriptRadio) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &scriptRadio{stubRadio: stubRadio{ch: make(chan radio.Event)}}
	c := &clock{t: time.Date(2026, 10, 13, 10, 3, 0, 0, time.UTC)}
	m := New(Options{Theme: theme.Neon, Motion: motion.Full, Clock: c.now, Store: st,
		Dial: func(Target) radio.Radio { return r }, Start: &Target{Kind: "USB", Addr: "/dev/cu.test"},
		Env:       func(k string) string { return map[string]string{"TERM_PROGRAM": "ghostty"}[k] },
		ExportDir: t.TempDir()})
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	send(m, c, 0, radio.BootStep{Key: "link", Label: "LINK", State: radio.StepOK})
	send(m, c, 100*time.Millisecond, chatReady)
	bootDone(t, m, c)
	return m, c, r
}

// follow runs a command chain the way Bubble Tea would, for the messages this feature sends itself.
// With timeouts false it stops at ack timers, as if the ack arrived first.
func follow(t *testing.T, m *Model, c *clock, cmd tea.Cmd, timeouts bool) {
	t.Helper()
	for depth := 0; cmd != nil && depth < 20; depth++ {
		var next []tea.Cmd
		var walk func(tea.Cmd)
		walk = func(cmd tea.Cmd) {
			if cmd == nil {
				return
			}
			switch msg := cmd().(type) {
			case tea.BatchMsg:
				for _, c := range msg {
					walk(c)
				}
			case sendResultMsg:
				_, nc := m.Update(msg)
				next = append(next, nc)
			case ackTimeoutMsg:
				if timeouts {
					_, nc := m.Update(msg)
					next = append(next, nc)
				}
			}
		}
		walk(cmd)
		if len(next) == 0 {
			return
		}
		cmd = tea.Batch(next...)
		c.t = c.t.Add(time.Second)
	}
}

// startConvo opens a conversation with a contact you haven't messaged, as m on Contacts does.
func startConvo(t *testing.T, m *Model, name string) {
	t.Helper()
	for _, ct := range m.chat.Contacts() {
		if ct.Name == name {
			m.jumpToConvo(store.ContactConvo(ct.Key))
			if cur, _ := m.selectedConvo(); cur.Title == name {
				return
			}
		}
	}
	t.Fatalf("couldn't start a conversation with %s", name)
}

func selectConvo(t *testing.T, m *Model, title string) {
	t.Helper()
	for range m.chats.infos {
		key(m, 'k')
	}
	for i := 0; i < 10; i++ {
		if cur, _ := m.selectedConvo(); cur.Title == title {
			return
		}
		key(m, 'j')
	}
	t.Fatalf("couldn't select %s", title)
}

func TestChatsReceiveSendAndDeliver(t *testing.T) {
	m, c, r := newChatModel(t)
	var from [6]byte
	copy(from[:], aliceKey[:6])
	send(m, c, 0, radio.Incoming{At: c.t, From: from, Hops: -1, SentAt: c.t, Text: "are you on the mesh tonight?", SNR: 9.5, HasSNR: true})
	tick(m, c, time.Second)
	got := plain(screen(t, m))
	for _, want := range []string{"CHANNELS", "DIRECT", "Public", "bristol", "Alice", "1 unread"} {
		if !strings.Contains(got, want) {
			t.Fatalf("chats missing %q:\n%s", want, got)
		}
	}

	selectConvo(t, m, "Alice")
	tick(m, c, time.Second)
	got = plain(screen(t, m))
	if strings.Contains(got, "unread") {
		t.Errorf("opening the conversation should clear unread:\n%s", got)
	}
	for _, want := range []string{"are you on the mesh tonight?", "direct · 2 hops A3·1E", "SNR +9.50"} {
		if !strings.Contains(got, want) {
			t.Errorf("conversation missing %q:\n%s", want, got)
		}
	}

	key(m, tea.KeyEnter)
	typeKeys(m, "yes, on now")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	follow(t, m, c, cmd, false)
	if len(r.texts) != 1 || r.texts[0] != "yes, on now" {
		t.Fatalf("radio sends = %v", r.texts)
	}
	if got := plain(screen(t, m)); !strings.Contains(got, "awaiting ack") {
		t.Errorf("sent message should await its ack:\n%s", got)
	}
	send(m, c, 0, radio.Acked{Ack: 1, RTT: 1200 * time.Millisecond})
	tick(m, c, time.Second)
	if got := plain(screen(t, m)); !strings.Contains(got, "✓ 1.2s") {
		t.Errorf("delivered message should show ✓ and the round trip:\n%s", got)
	}
	for _, size := range [][2]int{{80, 24}, {200, 60}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		if got := plain(screen(t, m)); !strings.Contains(got, "yes, on now") {
			t.Errorf("%v: conversation lost at this size:\n%s", size, got)
		}
	}
}

func TestChatsRetryFloodThenFail(t *testing.T) {
	m, c, r := newChatModel(t)
	startConvo(t, m, "Alice")
	key(m, tea.KeyEnter)
	typeKeys(m, "anyone there")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	follow(t, m, c, cmd, true)
	if len(r.texts) != 3 || r.resets != 1 {
		t.Fatalf("expected 3 attempts with one route reset, got %d sends and %d resets", len(r.texts), r.resets)
	}
	tick(m, c, time.Second)
	if got := plain(screen(t, m)); !strings.Contains(got, "[FAIL] not delivered after 3 tries") {
		t.Errorf("failed message:\n%s", got)
	}

	// R sends it again as a new message, and the failed copy goes.
	key(m, tea.KeyEscape)
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	follow(t, m, c, cmd, false)
	if len(r.texts) != 4 || r.texts[3] != "anyone there" {
		t.Fatalf("retry sends = %v", r.texts)
	}
	tick(m, c, time.Second)
	got := plain(screen(t, m))
	if strings.Contains(got, "[FAIL]") || strings.Count(got, "anyone there") != 1 || !strings.Contains(got, "awaiting ack") {
		t.Errorf("retried message:\n%s", got)
	}
	send(m, c, 0, radio.Acked{Ack: 4, RTT: time.Second})
	tick(m, c, time.Second)
	if got := plain(screen(t, m)); !strings.Contains(got, "✓") {
		t.Errorf("the retry is delivered:\n%s", got)
	}

	// With nothing failed, R says so.
	m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	if !strings.Contains(m.log[len(m.log)-1].text, "nothing here failed") {
		t.Error("R with nothing failed")
	}
}

func TestChatsLimitAndPaletteSend(t *testing.T) {
	m, c, r := newChatModel(t)
	selectConvo(t, m, "Public")
	key(m, tea.KeyEnter)
	typeKeys(m, strings.Repeat("x", 150))
	if got := plain(screen(t, m)); !strings.Contains(got, "150/147") {
		t.Errorf("counter should show the channel limit after the name prefix:\n%s", got)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	follow(t, m, c, cmd, false)
	if len(r.chans) != 0 {
		t.Fatal("an over-long message must not be sent")
	}
	if l := m.log[len(m.log)-1]; l.tone != radio.ToneFail || !strings.Contains(l.text, "too long") {
		t.Fatalf("expected a too-long failure, got %+v", l)
	}
	key(m, tea.KeyEscape)

	typeKeys(m, ":msg bri")
	key(m, tea.KeyTab)
	if m.pal.input.text != "msg bristol " {
		t.Fatalf("msg completion = %q", m.pal.input.text)
	}
	typeKeys(m, "hello channel")
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	follow(t, m, c, cmd, false)
	if len(r.chans) != 1 || r.chans[0] != "hello channel" {
		t.Fatalf("channel sends = %v", r.chans)
	}
	tick(m, c, time.Second)
	got := plain(screen(t, m))
	if !strings.Contains(got, "hello channel") || !strings.Contains(got, "sent") {
		t.Errorf("channel message not shown as sent:\n%s", got)
	}
}

func TestStartingAConversationFromContacts(t *testing.T) {
	h := newHarness(t)
	h.keys("1")
	titles := func() string {
		var out []string
		for _, i := range h.m.chats.infos {
			out = append(out, i.Title)
		}
		return strings.Join(out, ",")
	}
	if strings.Contains(titles(), "Bob") || strings.Contains(titles(), "Dan") {
		t.Fatalf("contacts who haven't written aren't listed: %s", titles())
	}
	h.keys("2")
	h.pick("Dan")
	h.keys("m")
	if cur, _ := h.m.selectedConvo(); cur.Title != "Dan" || h.m.tab != 0 {
		t.Fatalf("m opens a new conversation: %s", titles())
	}
	h.keys("k") // leaving without writing drops it again
	if strings.Contains(titles(), "Dan") {
		t.Errorf("an empty conversation left behind is dropped: %s", titles())
	}
	h.m.jumpToConvo(store.ContactConvo(h.contact("Dan").Key))
	h.keys("i")
	h.typeLine("hello Dan")
	h.until("sent", func() bool { return strings.Contains(h.screen(), "hello Dan") })
	h.keys("k")
	h.key(tea.KeyEscape)
	if !strings.Contains(titles(), "Dan") {
		t.Errorf("once written to, it stays: %s", titles())
	}

	// :msg reaches someone not yet messaged too.
	h.keys(":")
	h.typeLine("msg bob hello Bob")
	h.until("bob", func() bool { return strings.Contains(titles(), "Bob") })
	h.keys(":")
	h.typeLine("msg nobody hi")
	if !strings.Contains(h.lastLog(), "no conversation or contact called") {
		t.Errorf("unknown: %s", h.lastLog())
	}
}

func TestChannelRepeatsAreHeard(t *testing.T) {
	h := newHarness(t)
	pub, _ := h.channel("Public")
	h.m.jumpToConvo(store.ChannelConvo(pub.Idx))
	h.keys("i")
	h.typeLine("testing from the shed")
	h.until("a repeat", func() bool {
		msgs := h.m.chats.msgs
		return len(msgs) > 0 && msgs[len(msgs)-1].Repeats > 0
	})
	scr := h.screen()
	if !strings.Contains(scr, "✓ sent · heard") {
		t.Fatalf("repeats shown:\n%s", scr)
	}
	// It's saved: a fresh read of the conversation still has the count.
	msgs, _ := h.m.chat.Messages(store.ChannelConvo(pub.Idx), 5)
	if msgs[len(msgs)-1].Repeats == 0 {
		t.Error("repeats are stored")
	}

	now := h.m.now
	for _, c := range []struct {
		msg  store.Message
		want string
	}{
		{store.Message{Convo: "ch:0", Out: true, State: store.Sent, SentAt: now}, "listening for repeats"},
		{store.Message{Convo: "ch:0", Out: true, State: store.Sent, SentAt: now.Add(-time.Minute)}, "no repeats heard"},
		{store.Message{Convo: "ch:0", Out: true, State: store.Sent, SentAt: now, Repeats: 1}, "heard 1 repeat"},
		{store.Message{Convo: "ch:0", Out: true, State: store.Sent, SentAt: now, Repeats: 4}, "heard 4 repeats"},
	} {
		if got := ansi.Strip(h.m.repeatsMeta(c.msg)); !strings.Contains(got, c.want) {
			t.Errorf("%+v → %q, want %q", c.msg, got, c.want)
		}
	}
	h.m.chat = nil
	h.m.heardEcho(packets.Decoded{Decrypted: true, Sender: "MeshCore-k4", Text: "x"}) // no chat service: nothing to count
}

func TestBracketMentionsAreHighlighted(t *testing.T) {
	h := newHarness(t)
	pub, _ := h.channel("Public")
	bristol, _ := h.channel("#bristol")
	h.m.jumpToConvo(store.ChannelConvo(bristol.Idx)) // reading elsewhere, so Public's message stays unread
	h.m.Update(eventMsg{radio.Incoming{At: h.m.now, Channel: true, ChannelIdx: pub.Idx, SentAt: h.m.now,
		Text: "Carol: @[MeshCore-k4] are you hearing @[Bob Smith]?"}})
	h.settle()
	if !strings.Contains(plainLog(h), "MSG  Public · Carol") {
		t.Fatalf("message arrived:\n%s", plainLog(h))
	}
	for _, info := range h.m.chats.infos {
		if info.Title == "Public" && !info.Mention {
			t.Error("the conversation is marked as mentioning you")
		}
	}
	h.m.jumpToConvo(store.ChannelConvo(pub.Idx))
	scr := h.screen()
	if !strings.Contains(scr, "@MeshCore-k4 are you hearing @Bob Smith?") || strings.Contains(scr, "@[") {
		t.Fatalf("mentions shown without brackets:\n%s", scr)
	}
	line := h.m.highlight("@[MeshCore-k4] and @[Bob Smith]", "", h.m.th.Text, nil)
	you := h.m.th.Block(h.m.th.Text, h.m.th.Tint(h.m.th.Magenta)).Render("@MeshCore-k4")
	other := h.m.th.Block(h.m.th.Text, h.m.th.Tint(h.m.th.Violet)).Render("@Bob Smith")
	if !strings.Contains(line, you) || !strings.Contains(line, other) {
		t.Errorf("you on magenta, others on violet: %q", line)
	}
}

func TestMentionCompletion(t *testing.T) {
	h := newHarness(t)
	bristol, _ := h.channel("#bristol")
	h.m.jumpToConvo(store.ChannelConvo(bristol.Idx))
	h.keys("i")
	h.keys("hi @ca")
	h.key(tea.KeyTab)
	if h.m.chats.input.text != "hi @[Carol] " {
		t.Fatalf("completed = %q", h.m.chats.input.text)
	}
	h.m.chats.input, h.m.chats.mention = newLine("and @"), nil
	h.key(tea.KeyTab)
	first := h.m.chats.input.text
	if !strings.HasPrefix(first, "and @[") || !strings.Contains(h.screen(), "tab: 1 of") {
		t.Fatalf("first of many = %q\n%s", first, h.screen())
	}
	h.key(tea.KeyTab)
	if h.m.chats.input.text == first || !strings.Contains(h.screen(), "tab: 2 of") {
		t.Errorf("tab again cycles: %q", h.m.chats.input.text)
	}
	h.m.chats.input.set("@[Ali")
	h.key(tea.KeyTab)
	if h.m.chats.input.text != "@[Alice] " {
		t.Errorf("bracket partial = %q", h.m.chats.input.text)
	}
	for _, in := range []string{"no at sign", "@[Alice] done", "@zzz", "@Al ice"} {
		h.m.chats.input, h.m.chats.mention = newLine(in), nil
		h.key(tea.KeyTab)
		if h.m.chats.input.text != in {
			t.Errorf("%q changed to %q", in, h.m.chats.input.text)
		}
	}
	h.key(tea.KeyEscape)
}

// :msg refuses a name that could mean more than one conversation, rather than guessing which.
func TestMsgRefusesAnAmbiguousName(t *testing.T) {
	h := newHarness(t)
	h.keys("1")
	bob := h.contact("Bob")
	twin := radio.Contact{Key: [32]byte{0xB0, 0xB0}, Name: "Bob", Type: radio.TypeChat}
	city := radio.Contact{Key: [32]byte{0xB1}, Name: "Bristol", Type: radio.TypeChat}
	h.m.chat.ReplaceContacts(append(h.m.chat.Contacts(), twin, city))
	bristol, _ := h.channel("#bristol")
	sent := func() int { msgs, _ := h.m.chat.Messages(store.ChannelConvo(bristol.Idx), 100); return len(msgs) }
	before := sent()

	h.keys(":")
	h.typeLine("msg bristol meet at six")
	if l := h.lastLog(); !strings.Contains(l, "could be the channel #bristol or Bristol (B1000000)") {
		t.Errorf("channel and contact: %s", l)
	}
	h.keys(":")
	h.typeLine("msg bob hi")
	if l := h.lastLog(); !strings.Contains(l, radio.KeyHex(bob.Key[:4])) || !strings.Contains(l, "B0B00000") {
		t.Errorf("two contacts: %s", l)
	}
	if sent() != before || h.m.queued != nil {
		t.Fatal("nothing is sent while the name is ambiguous")
	}

	h.keys(":")
	h.typeLine("msg #bristol meet at six")
	h.until("sent", func() bool { return sent() == before+1 })
	h.keys(":")
	h.typeLine("msg dan hello")
	h.until("dan", func() bool { cur, _ := h.m.selectedConvo(); return cur.Title == "Dan" })
}

// TestChatListKeptInStep: the conversation list, built from what's held rather than read again for every
// event, keeps its unread counts, mentions, order and filter right, and matches the store on reconnecting.
func TestChatListKeptInStep(t *testing.T) {
	h := newHarness(t)
	h.openConvo("Public")
	info := func(title string) chat.Info {
		t.Helper()
		for _, i := range h.m.chats.infos {
			if i.Title == title {
				return i
			}
		}
		t.Fatalf("no conversation %s", title)
		return chat.Info{}
	}

	base, total := info("#bristol").Unread, h.m.chats.unread
	h.say(1, "Carol", "morning @[MeshCore-k4]")
	h.say(1, "Dan", "hello")
	if b := info("#bristol"); b.Unread != base+2 || !b.Mention || h.m.chats.unread != total+2 {
		t.Fatalf("#bristol = %+v, %d unread in all", b, h.m.chats.unread)
	}
	h.say(0, "Bob", "in the open one")
	if p := info("Public"); p.Unread != 0 || h.lastMessage().Text != "in the open one" {
		t.Fatalf("the open conversation is read as it arrives: %+v", p)
	}

	alice := h.contact("Alice")
	h.incoming(radio.Incoming{From: alice.Prefix(), Hops: -1, Text: "first"})
	h.incoming(radio.Incoming{From: [6]byte{0xEE, 1, 2, 3, 4, 5}, Hops: -1, Text: "stranger"})
	if n := len(h.m.chats.infos); h.m.chats.infos[n-2].Title != "EE0102030405" || h.m.chats.infos[n-1].Title != "Alice" {
		t.Fatalf("direct conversations, newest first: %+v", h.m.chats.infos)
	}
	before := info("Alice").Unread
	h.incoming(radio.Incoming{From: alice.Prefix(), Hops: -1, Text: "again"})
	if n := len(h.m.chats.infos); h.m.chats.infos[n-2].Title != "Alice" || h.m.chats.infos[n-2].Unread != before+1 {
		t.Fatalf("Alice moves up: %+v", h.m.chats.infos)
	}

	// An advert that changes nothing listed leaves the list alone; a new name is shown.
	listed := &h.m.chats.infos[0]
	for _, ct := range h.m.chat.Contacts() {
		if ct.Type == radio.TypeRepeater {
			ct.LastAdvert = h.clk.t
			h.m.Update(eventMsg{radio.ContactUpdated{Contact: ct}})
			break
		}
	}
	if &h.m.chats.infos[0] != listed {
		t.Error("an advert from someone not listed rebuilt the list")
	}
	alice.Name = "Alice B"
	h.m.Update(eventMsg{radio.ContactUpdated{Contact: alice}})
	info("Alice B")

	// A filter in the open conversation follows what arrives.
	h.keys("/zz")
	h.key(tea.KeyEnter)
	h.say(0, "Bob", "zz top")
	h.say(0, "Bob", "nothing")
	if msgs := h.m.chats.msgs; len(msgs) != 1 || msgs[0].Text != "zz top" {
		t.Fatalf("filtered: %+v", msgs)
	}
	h.key(tea.KeyEscape)

	// A signature lands on its message, in the open conversation and in another.
	zed := newSigner("zed")
	h.m.contacts.heard = append(h.m.contacts.heard, heardNode{radio.Contact{Key: zed.pub, Name: "Zed"}, h.clk.t})
	ts := h.say(0, "Zed", "signed here")
	h.datagram(0, zed.datagram("Public", ts, "signed here"))
	if m := h.m.chats.msgs[len(h.m.chats.msgs)-1]; m.Sig == store.Unsigned {
		t.Errorf("the open conversation shows the signature: %+v", m)
	}
	ts = h.say(1, "Zed", "signed there")
	h.datagram(1, zed.datagram("#bristol", ts, "signed there"))
	if b := info("#bristol"); b.Unread != base+3 || !b.Mention {
		t.Fatalf("#bristol after a signature = %+v", b)
	}

	// Reconnecting reads the list afresh, and it's the same.
	reconnect := func() {
		r := h.m.ready
		r.ContactList, r.ChannelList = h.m.chat.Contacts(), h.m.chat.Channels()
		h.m.Update(eventMsg{r})
		h.settle()
	}
	held := slices.Clone(h.m.chats.infos)
	reconnect()
	if !reflect.DeepEqual(held, h.m.chats.infos) {
		t.Fatalf("after reconnecting\n%+v\nwant\n%+v", h.m.chats.infos, held)
	}
	for i := range 50 {
		h.m.Update(eventMsg{radio.Incoming{At: h.clk.t, SentAt: h.clk.t, Channel: true, ChannelIdx: 1, Queued: true, Text: fmt.Sprintf("Q: %d", i)}})
	}
	if b := info("#bristol"); b.Unread != base+53 {
		t.Fatalf("a burst of queued messages counts: %+v", b)
	}

	// Reading clears the count and the mention; leaving a channel takes it off the list.
	h.openConvo("#bristol")
	if b := info("#bristol"); b.Unread != 0 || b.Mention || h.m.chats.unread != total-base+3 {
		t.Fatalf("read: %+v, %d unread in all", b, h.m.chats.unread)
	}
	h.keys("xy")
	h.until("left", func() bool { _, ok := h.channel("#bristol"); return !ok })
	for _, i := range h.m.chats.infos {
		if i.Convo == store.ChannelConvo(1) {
			t.Fatal("a channel left is still listed")
		}
	}
	held = slices.Clone(h.m.chats.infos)
	reconnect()
	if !reflect.DeepEqual(held, h.m.chats.infos) {
		t.Fatalf("after leaving and reconnecting\n%+v\nwant\n%+v", h.m.chats.infos, held)
	}
}

// BenchmarkIncomingWithHistory is the cost of one channel message arriving with history behind it,
// which shouldn't grow with the history.
func BenchmarkIncomingWithHistory(b *testing.B) {
	h := newHarness(b)
	st, dev := h.m.opt.Store, h.m.chat.Device()
	for i := range 20000 {
		at := h.clk.t.Add(time.Duration(i-20000) * time.Second)
		m := store.Message{Convo: store.ChannelConvo(i % 3), Sender: "Bob", Text: fmt.Sprint("history ", i), SentAt: at, RecvAt: at}
		if i%5 == 0 {
			m.Convo = store.UnknownConvo([6]byte{byte(i % 200), 1})
		}
		st.AddMessage(dev, &m)
	}
	h.m.Update(eventMsg{h.m.ready})
	h.settle()
	h.openConvo("Public")
	b.ResetTimer()
	for i := range b.N {
		h.m.Update(eventMsg{radio.Incoming{At: h.clk.t, SentAt: h.clk.t, Channel: true, ChannelIdx: 1, Text: fmt.Sprint("Bob: ", i)}})
	}
}
