package chat

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
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

	infos, err := s.Conversations("")
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, i := range infos {
		titles = append(titles, i.Title)
	}
	// Channels first, then conversations with messages, newest first. Contacts and rooms without any aren't listed.
	want := []string{"Public", "#bristol", "EE0102030405", "Alice"}
	if strings.Join(titles, ",") != strings.Join(want, ",") {
		t.Fatalf("conversation order = %v, want %v", titles, want)
	}
	if infos[1].Unread != 1 || infos[3].Kind != KindDirect {
		t.Fatalf("unread/kind wrong: %+v", infos)
	}
	s.Incoming(radio.Incoming{At: t0, From: room.Prefix(), Text: "a post", Signed: true})
	infos, _ = s.Conversations("")
	if infos[2].Title != "Clifton Room" || infos[2].Kind != KindRoom {
		t.Fatalf("a room with posts is listed as a room: %+v", infos)
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

func TestRetryResendsAFailedMessage(t *testing.T) {
	s := newService(t, 1, false)
	m, send, _ := s.Compose(store.ContactConvo(alice.Key), "anyone?", t0)
	if _, _, err := s.Retry(m, t0); err == nil {
		t.Fatal("a message still sending isn't retried")
	}
	_, wait, _ := s.Result(send, radio.Sent{Ack: 7, Timeout: time.Second}, nil)
	failed, _, _ := s.Timeout(*wait)
	later := t0.Add(time.Hour)
	again, send, err := s.Retry(failed, later)
	if err != nil || again.ID == failed.ID || send.ID != again.ID || send.Attempt != 0 || send.Text != "anyone?" {
		t.Fatalf("retry = %+v, %+v, %v", again, send, err)
	}
	ms, _ := s.Messages(store.ContactConvo(alice.Key), 10)
	if len(ms) != 1 || ms[0].ID != again.ID || ms[0].State != store.Pending || !ms[0].SentAt.Equal(later) {
		t.Fatalf("stored = %+v", ms)
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

func TestMentionsAndExport(t *testing.T) {
	s := newService(t, 3, true)
	for text, want := range map[string]bool{
		"hey @MeshCore-k4 you there": true, "@meshcore-k4": true, "ping @MeshCore-k4x": false,
		"MeshCore-k4 without the at": false, "@MeshCore-k4, and @MeshCore-k42": true,
		"@[MeshCore-k4] are you hearing Purdown?": true, "hi @[ meshcore-k4 ]": true, "@[MeshCore-k42] not you": false,
		"@[MeshCore-k4": false, "@[] nobody": false,
	} {
		if got := s.Mentions(text); got != want {
			t.Errorf("Mentions(%q) = %v, want %v", text, got, want)
		}
	}
	s.Incoming(radio.Incoming{At: t0, Channel: true, ChannelIdx: 1, Text: "Carol: @meshcore-k4 is Purdown up?", Hops: 3, SNR: 6.5, HasSNR: true})
	s.Incoming(radio.Incoming{At: t0, From: alice.Prefix(), Text: "hi", Hops: -1})
	infos, _ := s.Conversations("")
	if !infos[1].Mention || infos[0].Mention {
		t.Fatalf("mention flags: %+v", infos[:2])
	}

	var text strings.Builder
	n, err := s.Export(&text, "", false)
	if err != nil || n != 2 {
		t.Fatalf("export n=%d err=%v", n, err)
	}
	for _, want := range []string{"# #bristol", "Carol: @meshcore-k4 is Purdown up?  (3 hops)", "# Alice", "Alice: hi  (direct)"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text export missing %q:\n%s", want, text.String())
		}
	}
	var js strings.Builder
	s.Export(&js, store.ChannelConvo(1), true)
	if !strings.Contains(js.String(), `"conversation": "#bristol"`) || !strings.Contains(js.String(), `"snr": 6.5`) || strings.Contains(js.String(), "Alice") {
		t.Errorf("json export:\n%s", js.String())
	}
}

func TestContactChangesAndTitles(t *testing.T) {
	s := newService(t, 3, true)
	moved := alice
	moved.PathLen, moved.Path = 1, []byte{0x77}
	if err := s.ContactUpdated(moved); err != nil {
		t.Fatal(err)
	}
	eve := radio.Contact{Key: [32]byte{0xE5}, Name: "Eve", Type: radio.TypeChat, PathLen: -1}
	s.ContactUpdated(eve)
	if got, ok := s.Contact(store.ContactConvo(alice.Key)); !ok || got.PathLen != 1 {
		t.Errorf("updated contact = %+v", got)
	}
	if len(s.Contacts()) != 4 {
		t.Fatalf("contacts = %d", len(s.Contacts()))
	}
	if c, ok := s.ContactByPrefix(eve.Prefix()); !ok || c.Name != "Eve" {
		t.Error("prefix lookup")
	}
	s.RemoveContact(eve.Key)
	if len(s.Contacts()) != 3 {
		t.Error("remove")
	}
	if err := s.ReplaceContacts([]radio.Contact{alice}); err != nil || len(s.Contacts()) != 1 {
		t.Error("replace")
	}
	if s.Title(store.ChannelConvo(9)) != "channel 9" || s.Title(store.UnknownConvo([6]byte{0xAB})) != "AB0000000000" {
		t.Error("titles for unknown conversations")
	}
	if len(s.Channels()) != 2 || s.Self() != "MeshCore-k4" {
		t.Error("channels and self")
	}
	if _, _, err := s.Compose(store.ChannelConvo(9), "hi", t0); err == nil {
		t.Error("a channel that isn't on the radio can't be sent to")
	}
	if _, _, err := s.Compose(store.ChannelConvo(0), "   ", t0); err == nil {
		t.Error("blank messages are refused")
	}
	empty := New(nil, 0, false)
	if empty.Mentions("@anyone") {
		t.Error("with no name, nothing mentions you")
	}
	// An ack that matches nothing, and a stale timeout, are ignored.
	if _, ok, _ := s.Acked(radio.Acked{Ack: 999}); ok {
		t.Error("unknown ack")
	}
	if m, next, _ := s.Timeout(Wait{ID: 12345}); next != nil || m.ID != 0 {
		t.Error("unknown timeout")
	}
	if m, wait, _ := s.Result(Send{ID: 12345}, radio.Sent{}, nil); wait != nil || m.ID != 0 {
		t.Error("result for a settled message")
	}
	if _, err := s.Search("anything", "", 5); err != nil {
		t.Error(err)
	}
	if s.MarkRead(store.ChannelConvo(0)) != nil {
		t.Error("mark read")
	}
}

func TestStoreFailuresSurface(t *testing.T) {
	st, _ := store.Open(":memory:")
	s := New(st, 3, true)
	st.Close()
	if err := s.Ready(radio.Ready{Self: radio.SelfInfo{Name: "me"}}); err == nil {
		t.Error("Ready should report a broken store")
	}
	if _, err := s.Incoming(radio.Incoming{Text: "x"}); err == nil {
		t.Error("Incoming should report a broken store")
	}
	if _, err := s.Conversations(""); err == nil {
		t.Error("Conversations should report a broken store")
	}
	var sb strings.Builder
	if _, err := s.Export(&sb, "", false); err == nil {
		t.Error("Export should report a broken store")
	}
}

func TestRoomPostsNameTheirAuthor(t *testing.T) {
	s := newService(t, 3, true)
	for _, c := range []struct {
		author [4]byte
		want   string
	}{{[4]byte{0x3A, 0xAA}, "Alice"}, {[4]byte{0xC0}, "MeshCore-k4"}, {[4]byte{0xEE, 1, 2, 3}, "EE010203"}, {[4]byte{}, "Clifton Room"}} {
		m, _ := s.Incoming(radio.Incoming{At: t0, From: room.Prefix(), Signed: true, Author: c.author, Text: "post"})
		if m.Convo != store.ContactConvo(room.Key) || m.Sender != c.want {
			t.Errorf("author %X → %+v", c.author, m)
		}
	}
	m, _ := s.Incoming(radio.Incoming{At: t0, From: alice.Prefix(), Author: [4]byte{0xC0}, Text: "dm"})
	if m.Sender != "Alice" {
		t.Error("only room posts carry an author")
	}
}

func TestOnlyConversationsWithMessagesAreListed(t *testing.T) {
	s := newService(t, 3, true)
	titles := func(open store.Convo) string {
		infos, _ := s.Conversations(open)
		var out []string
		for _, i := range infos {
			out = append(out, i.Title)
		}
		return strings.Join(out, ",")
	}
	if got := titles(""); got != "Public,#bristol" {
		t.Errorf("before any messages = %s", got)
	}
	if got := titles(store.ContactConvo(alice.Key)); got != "Public,#bristol,Alice" {
		t.Errorf("starting one with Alice = %s", got)
	}
	if got := titles(store.ContactConvo(room.Key)); got != "Public,#bristol,Clifton Room" {
		t.Errorf("opening a room = %s", got)
	}
	if got := titles(store.ContactConvo(rep.Key)); got != "Public,#bristol" {
		t.Errorf("a repeater isn't someone to message = %s", got)
	}
	s.Incoming(radio.Incoming{At: t0, From: alice.Prefix(), Text: "hi"})
	s.Incoming(radio.Incoming{At: t0, From: room.Prefix(), Text: "first post", Signed: true})
	if got := titles(""); got != "Public,#bristol,Clifton Room,Alice" && got != "Public,#bristol,Alice,Clifton Room" {
		t.Errorf("after Alice writes and the room posts = %s", got)
	}
}

func TestChannelEchoes(t *testing.T) {
	s := newService(t, 3, true)
	m, _, _ := s.Compose(store.ChannelConvo(1), "anyone on Dundry?", t0)
	s.Compose(store.ChannelConvo(1), "second", t0.Add(time.Second))
	for i, c := range []struct {
		idx          int
		sender, text string
		after        time.Duration
		ok           bool
		repeats      int
	}{
		{1, "MeshCore-k4", "anyone on Dundry?", time.Second, true, 1},
		{1, "MeshCore-k4", "anyone on Dundry?", 2 * time.Second, true, 2},
		{1, "Carol", "anyone on Dundry?", time.Second, false, 0},       // someone else saying the same
		{0, "MeshCore-k4", "anyone on Dundry?", time.Second, false, 0}, // another channel
		{1, "MeshCore-k4", "something else", time.Second, false, 0},
		{1, "MeshCore-k4", "anyone on Dundry?", EchoWindow + time.Minute, false, 0}, // long after
	} {
		id, n, ok, err := s.Echo(c.idx, c.sender, c.text, t0.Add(c.after))
		if err != nil || ok != c.ok || ok && (id != m.ID || n != c.repeats) {
			t.Errorf("case %d: id %d n %d ok %v err %v", i, id, n, ok, err)
		}
	}
	msgs, _ := s.store.Messages(s.device, store.ChannelConvo(1), 5)
	if msgs[0].Repeats != 2 || msgs[1].Repeats != 0 {
		t.Errorf("stored repeats = %d, %d", msgs[0].Repeats, msgs[1].Repeats)
	}
	if len(s.echoes) != 0 {
		t.Error("old sends stop being listened for")
	}
}

func TestFindMentions(t *testing.T) {
	got := FindMentions("@[Bob Smith] and @alice, but not email@ or @[unclosed")
	want := []Mention{{0, 12, "Bob Smith"}, {17, 23, "alice"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("mentions = %+v", got)
	}
}
