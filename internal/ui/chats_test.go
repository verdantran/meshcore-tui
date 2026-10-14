package ui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/store"
	"meshcore-tui/internal/theme"
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
	tick(m, c, time.Second)
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
	selectConvo(t, m, "Alice")
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
	if m.pal.input != "msg bristol " {
		t.Fatalf("msg completion = %q", m.pal.input)
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
