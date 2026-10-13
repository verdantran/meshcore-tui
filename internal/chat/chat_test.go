package chat

import (
	"errors"
	"strings"
	"testing"
	"time"

	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/store"
)

var (
	alice = radio.Contact{Key: [32]byte{0x3A, 0xAA}, Name: "Alice", Type: radio.TypeChat, PathLen: 2, Path: []byte{0xA3, 0x1E}}
	room  = radio.Contact{Key: [32]byte{0x6C, 0xCC}, Name: "Clifton Room", Type: radio.TypeRoom, PathLen: -1}
	rep   = radio.Contact{Key: [32]byte{0xA3}, Name: "Dundry", Type: radio.TypeRepeater, PathLen: -1}
	t0    = time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
)

func newService(t *testing.T, retries int, flood bool) *Service {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, retries, flood)
	err = s.Ready(radio.Ready{
		Self:        radio.SelfInfo{Name: "MeshCore-k4", PubKey: [32]byte{0xC0}},
		ContactList: []radio.Contact{alice, room, rep},
		ChannelList: []radio.Channel{{Idx: 0, Name: "Public"}, {Idx: 1, Name: "#bristol"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIncomingIsFiledByConversation(t *testing.T) {
	s := newService(t, 3, true)
	ch, err := s.Incoming(radio.Incoming{At: t0, Channel: true, ChannelIdx: 1, Text: "Carol: morning: all", Hops: 3})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Convo != store.ChannelConvo(1) || ch.Sender != "Carol" || ch.Text != "morning: all" {
		t.Fatalf("channel message = %+v", ch)
	}
	dm, _ := s.Incoming(radio.Incoming{At: t0, From: alice.Prefix(), Text: "hi", Hops: -1})
	if dm.Convo != store.ContactConvo(alice.Key) || dm.Sender != "Alice" {
		t.Fatalf("dm = %+v", dm)
	}
	stranger, _ := s.Incoming(radio.Incoming{At: t0, From: [6]byte{0xEE, 1, 2, 3, 4, 5}, Text: "who's this"})
	if !strings.HasPrefix(string(stranger.Convo), "dm?:EE01") {
		t.Fatalf("unknown sender convo = %q", stranger.Convo)
	}

	infos, err := s.Conversations()
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, i := range infos {
		titles = append(titles, i.Title)
	}
	// Channels first, then conversations with messages (newest first), then the remaining chat and room contacts.
	want := []string{"Public", "#bristol", "EE0102030405", "Alice", "Clifton Room"}
	if strings.Join(titles, ",") != strings.Join(want, ",") {
		t.Fatalf("conversation order = %v, want %v", titles, want)
	}
	if infos[1].Unread != 1 || infos[4].Kind != KindRoom {
		t.Fatalf("unread/kind wrong: %+v", infos)
	}
}

func TestDeliveredFirstTime(t *testing.T) {
	s := newService(t, 3, true)
	m, send, err := s.Compose(store.ContactConvo(alice.Key), "  hello  ", t0)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.Pending || send.To != alice.Key || send.Text != "hello" || send.ResetPath {
		t.Fatalf("compose = %+v / %+v", m, send)
	}
	m, wait, err := s.Result(send, radio.Sent{Ack: 77, Timeout: 4 * time.Second}, nil)
	if err != nil || m.State != store.Sent || wait == nil || wait.Delay != 5*time.Second {
		t.Fatalf("result = %+v wait=%+v err=%v", m, wait, err)
	}
	m, ok, _ := s.Acked(radio.Acked{Ack: 77, RTT: 1200 * time.Millisecond})
	if !ok || m.State != store.Delivered || m.RTT != 1200*time.Millisecond || m.Attempts != 1 {
		t.Fatalf("acked = %+v ok=%v", m, ok)
	}
	// A timeout after delivery is a no-op.
	if _, again, _ := s.Timeout(*wait); again != nil {
		t.Fatal("timeout after delivery should not retry")
	}
	ms, _ := s.Messages(store.ContactConvo(alice.Key), 10)
	if len(ms) != 1 || ms[0].State != store.Delivered {
		t.Fatalf("stored = %+v", ms)
	}
}

func TestRetriesThenFloodsThenFails(t *testing.T) {
	s := newService(t, 3, true)
	_, send, _ := s.Compose(store.ContactConvo(alice.Key), "anyone?", t0)
	var resets []bool
	for attempt := 0; attempt < 3; attempt++ {
		if send.Attempt != attempt {
			t.Fatalf("attempt %d got send for %d", attempt, send.Attempt)
		}
		resets = append(resets, send.ResetPath)
		_, wait, _ := s.Result(send, radio.Sent{Ack: uint32(100 + attempt), Timeout: time.Second}, nil)
		m, next, _ := s.Timeout(*wait)
		if attempt < 2 {
			if next == nil || m.State != store.Pending {
				t.Fatalf("attempt %d: expected a retry, got %+v", attempt, m)
			}
			send = *next
			continue
		}
		if next != nil || m.State != store.Failed || m.Attempts != 3 {
			t.Fatalf("after the last attempt: %+v next=%v", m, next)
		}
	}
	if resets[0] || resets[1] || !resets[2] {
		t.Fatalf("only the last attempt should flood: %v", resets)
	}
}

func TestLateAckForEarlierAttemptStillDelivers(t *testing.T) {
	s := newService(t, 3, true)
	_, send, _ := s.Compose(store.ContactConvo(alice.Key), "hello", t0)
	_, wait, _ := s.Result(send, radio.Sent{Ack: 1, Timeout: time.Second}, nil)
	_, next, _ := s.Timeout(*wait)
	s.Result(*next, radio.Sent{Ack: 2, Timeout: time.Second}, nil)
	m, ok, _ := s.Acked(radio.Acked{Ack: 1, RTT: 3 * time.Second})
	if !ok || m.State != store.Delivered {
		t.Fatalf("late ack = %+v ok=%v", m, ok)
	}
	if _, ok, _ := s.Acked(radio.Acked{Ack: 2}); ok {
		t.Fatal("the second ack should find nothing left to settle")
	}
}

func TestLimitsAndErrors(t *testing.T) {
	s := newService(t, 3, true)
	if got := s.MaxBytes(store.ChannelConvo(0)); got != radio.MaxText-len("MeshCore-k4")-2 {
		t.Fatalf("channel limit = %d", got)
	}
	if _, _, err := s.Compose(store.ContactConvo(alice.Key), strings.Repeat("é", 81), t0); !errors.Is(err, ErrTooLong) {
		t.Fatalf("161 bytes should be too long, got %v", err)
	}
	if _, _, err := s.Compose(store.UnknownConvo([6]byte{0xEE}), "hi", t0); err == nil {
		t.Fatal("replying to a non-contact should fail")
	}
	_, send, _ := s.Compose(store.ContactConvo(alice.Key), "hi", t0)
	m, wait, _ := s.Result(send, radio.Sent{}, errors.New("table full"))
	if m.State != store.Failed || wait != nil {
		t.Fatalf("radio error = %+v", m)
	}
	_, ch, _ := s.Compose(store.ChannelConvo(1), "hello channel", t0)
	if !ch.Channel || ch.Idx != 1 {
		t.Fatalf("channel send = %+v", ch)
	}
	m, wait, _ = s.Result(ch, radio.Sent{}, nil)
	if m.State != store.Sent || wait != nil {
		t.Fatalf("channel result = %+v wait=%v", m, wait)
	}
}

func TestUnfinishedFromLastSessionFail(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	s := New(st, 3, true)
	ready := radio.Ready{Self: radio.SelfInfo{Name: "me", PubKey: [32]byte{0xC0}}, ContactList: []radio.Contact{alice}}
	s.Ready(ready)
	s.Compose(store.ContactConvo(alice.Key), "sent just before quitting", t0)

	s2 := New(st, 3, true)
	s2.Ready(ready)
	ms, _ := s2.Messages(store.ContactConvo(alice.Key), 10)
	if len(ms) != 1 || ms[0].State != store.Failed {
		t.Fatalf("leftover = %+v", ms)
	}
}
