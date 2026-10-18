package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

const dev = "C04F19"

func TestContactsAndChannelsRoundTrip(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := [32]byte{0x3A, 1, 2, 3}
	in := []radio.Contact{
		{Key: key, Name: "alice", Type: radio.TypeChat, PathLen: 2, Path: []byte{0xA3, 0x1E}, LastAdvert: time.Unix(1700000000, 0), Lat: 51.3, Lon: -2.9},
		{Key: [32]byte{0xA3}, Name: "Dundry", Type: radio.TypeRepeater, PathLen: -1},
	}
	if err := s.SaveContacts(dev, in); err != nil {
		t.Fatal(err)
	}
	out, err := s.Contacts(dev)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Name != "alice" || out[0].Key != key || out[0].PathLen != 2 || out[0].Path[1] != 0x1E || out[1].PathLen != -1 {
		t.Fatalf("contacts = %+v", out)
	}
	// Saving again replaces rather than duplicates.
	if err := s.SaveContacts(dev, in[:1]); err != nil {
		t.Fatal(err)
	}
	if out, _ = s.Contacts(dev); len(out) != 1 {
		t.Fatalf("after replace: %d contacts", len(out))
	}
	if other, _ := s.Contacts("other"); len(other) != 0 {
		t.Fatal("contacts leaked across devices")
	}

	chs := []radio.Channel{{Idx: 0, Name: "Public"}, {Idx: 1, Name: "#bristol", Secret: [16]byte{9}}}
	if err := s.SaveChannels(dev, chs); err != nil {
		t.Fatal(err)
	}
	back, _ := s.Channels(dev)
	if len(back) != 2 || back[1].Secret[0] != 9 {
		t.Fatalf("channels = %+v", back)
	}
}

func TestMessagesUnreadAndDelivery(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "mc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ch := ChannelConvo(1)
	dm := ContactConvo([32]byte{0x3A})
	now := time.UnixMilli(1760000000000)
	add := func(m Message) Message {
		t.Helper()
		if err := s.AddMessage(dev, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	add(Message{Convo: ch, Sender: "Carol", Text: "morning", RecvAt: now, Hops: 3})
	add(Message{Convo: ch, Sender: "Bob", Text: "hi", RecvAt: now.Add(time.Second), Hops: 2, SNR: 2.25, HasSNR: true})
	out := add(Message{Convo: dm, Out: true, Sender: "me", Text: "hello", RecvAt: now.Add(2 * time.Second), State: Pending})

	sums, err := s.Conversations(dev)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 2 || sums[0].Convo != dm || sums[1].Unread != 2 || sums[1].Last.Text != "hi" {
		t.Fatalf("summaries = %+v", sums)
	}
	if err := s.MarkRead(dev, ch); err != nil {
		t.Fatal(err)
	}
	sums, _ = s.Conversations(dev)
	if sums[1].Unread != 0 {
		t.Fatalf("unread after MarkRead = %d", sums[1].Unread)
	}

	if u, _ := s.Unfinished(dev); len(u) != 1 || u[0].ID != out.ID {
		t.Fatalf("unfinished = %+v", u)
	}
	out.State, out.Ack, out.Attempts, out.RTT = Delivered, 0xDEADBEEF, 2, 1450*time.Millisecond
	if err := s.UpdateDelivery(out); err != nil {
		t.Fatal(err)
	}
	ms, _ := s.Messages(dev, dm, 10)
	if len(ms) != 1 || ms[0].State != Delivered || ms[0].RTT != 1450*time.Millisecond || ms[0].Ack != 0xDEADBEEF || ms[0].Attempts != 2 {
		t.Fatalf("after delivery = %+v", ms)
	}
	if u, _ := s.Unfinished(dev); len(u) != 0 {
		t.Fatal("delivered message still unfinished")
	}

	got, _ := s.Messages(dev, ch, 1)
	if len(got) != 1 || got[0].Text != "hi" || !got[0].HasSNR || got[0].SNR != 2.25 {
		t.Fatalf("latest-one query = %+v", got)
	}
}

func TestSearchAllAndUnread(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	ch, dm := ChannelConvo(1), ContactConvo([32]byte{0x3A})
	for _, m := range []Message{
		{Convo: ch, Sender: "Carol", Text: "Purdown is back"},
		{Convo: ch, Sender: "Bob", Text: "100% battery_now"},
		{Convo: dm, Sender: "Alice", Text: "purdown route works"},
	} {
		m := m
		s.AddMessage(dev, &m)
	}
	got, _ := s.Search(dev, "purdown", "", 10)
	if len(got) != 2 || got[0].Text != "purdown route works" {
		t.Fatalf("global search = %+v", got)
	}
	if got, _ := s.Search(dev, "purdown", ch, 10); len(got) != 1 {
		t.Fatalf("channel search = %+v", got)
	}
	// LIKE wildcards in the query are literal.
	if got, _ := s.Search(dev, "100%", "", 10); len(got) != 1 {
		t.Fatalf("%% should be literal: %+v", got)
	}
	if got, _ := s.Search(dev, "y_n", "", 10); len(got) != 1 {
		t.Fatalf("_ should be literal: %+v", got)
	}
	if got, _ := s.Search(dev, "Carol", "", 10); len(got) != 1 {
		t.Fatalf("sender search = %+v", got)
	}
	if all, _ := s.All(dev, ""); len(all) != 3 {
		t.Fatalf("all = %d", len(all))
	}
	if u, _ := s.Unread(dev, ch); len(u) != 2 {
		t.Fatalf("unread = %d", len(u))
	}
	s.MarkRead(dev, ch)
	if u, _ := s.Unread(dev, ch); len(u) != 0 {
		t.Fatalf("unread after read = %d", len(u))
	}
}

func TestUpsertAllAndFailures(t *testing.T) {
	s, _ := Open(":memory:")
	c := radio.Contact{Key: [32]byte{0x3A}, Name: "Alice", PathLen: -1}
	if err := s.UpsertContact(dev, c); err != nil {
		t.Fatal(err)
	}
	c.Name = "Alice B"
	s.UpsertContact(dev, c)
	if cs, _ := s.Contacts(dev); len(cs) != 1 || cs[0].Name != "Alice B" {
		t.Fatalf("upsert = %+v", cs)
	}
	if UnknownConvo([6]byte{0xEE, 1}) != "dm?:EE0100000000" {
		t.Errorf("unknown convo = %s", UnknownConvo([6]byte{0xEE, 1}))
	}
	m := Message{Convo: ChannelConvo(2), Text: "x"}
	s.AddMessage(dev, &m)
	if got, _ := s.All(dev, ChannelConvo(2)); len(got) != 1 {
		t.Fatalf("all in one conversation = %d", len(got))
	}
	if got, _ := s.Search(dev, "   ", "", 5); got != nil {
		t.Error("a blank search finds nothing")
	}

	// Every query reports errors once the database is gone.
	s.Close()
	checks := map[string]error{}
	_, checks["contacts"] = s.Contacts(dev)
	checks["save contacts"] = s.SaveContacts(dev, []radio.Contact{c})
	checks["save channels"] = s.SaveChannels(dev, nil)
	_, checks["channels"] = s.Channels(dev)
	checks["add"] = s.AddMessage(dev, &m)
	_, checks["messages"] = s.Messages(dev, ChannelConvo(2), 5)
	_, checks["conversations"] = s.Conversations(dev)
	_, checks["unfinished"] = s.Unfinished(dev)
	checks["mark read"] = s.MarkRead(dev, ChannelConvo(2))
	for what, err := range checks {
		if err == nil {
			t.Errorf("%s after close should fail", what)
		}
	}
	nested, err := Open(filepath.Join(t.TempDir(), "new", "folder", "x.db"))
	if err != nil {
		t.Errorf("missing folders should be created: %v", err)
	} else {
		nested.Close()
	}
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, nil, 0o644)
	if _, err := Open(filepath.Join(file, "x.db")); err == nil {
		t.Error("a database under a file can't be opened")
	}
}

func TestRepeatsAndMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// A database from before repeats were counted.
	if _, err := old.Exec(`CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, device TEXT NOT NULL, convo TEXT NOT NULL,
		out INTEGER NOT NULL, sender TEXT NOT NULL, text TEXT NOT NULL, sent_at INTEGER NOT NULL, recv_at INTEGER NOT NULL,
		hops INTEGER NOT NULL, snr REAL NOT NULL, has_snr INTEGER NOT NULL, signed INTEGER NOT NULL,
		state INTEGER NOT NULL, ack INTEGER NOT NULL, attempts INTEGER NOT NULL, rtt_ms INTEGER NOT NULL, flood INTEGER NOT NULL);
		INSERT INTO messages VALUES (1,'d','ch:0',1,'me','old',0,0,-1,0,0,0,1,0,1,0,1);`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, err := s.AddRepeat(1); err != nil || n != 1 {
		t.Fatalf("repeat on a migrated database = %d, %v", n, err)
	}
	s.AddRepeat(1)
	msgs, _ := s.Messages("d", ChannelConvo(0), 5)
	if len(msgs) != 1 || msgs[0].Repeats != 2 {
		t.Errorf("messages = %+v", msgs)
	}
	if _, err := s.AddRepeat(99); err == nil {
		t.Error("no such message")
	}
	s.Close()
	if s2, err := Open(path); err != nil {
		t.Errorf("reopening an up-to-date database: %v", err)
	} else {
		s2.Close()
	}
}

func TestStatusHistory(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, b := [32]byte{1}, [32]byte{2}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	old := StatusRow{At: now.Add(-100 * 24 * time.Hour), Data: []byte{9}}
	if err := s.AddStatus(a, old); err != nil {
		t.Fatal(err)
	}
	for i, at := range []time.Time{now.Add(-2 * time.Hour), now.Add(-time.Hour), now} {
		if err := s.AddStatus(a, StatusRow{At: at, Room: i == 1, Data: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	s.AddStatus(b, StatusRow{At: now, Data: []byte{7}})

	all, _ := s.Status(a, time.Time{})
	if len(all) != 3 || !all[1].Room || all[2].Data[0] != 2 || !all[0].At.Equal(now.Add(-2*time.Hour)) {
		t.Fatalf("history = %+v (the 100-day-old row should be gone)", all)
	}
	if recent, _ := s.Status(a, now.Add(-90*time.Minute)); len(recent) != 2 {
		t.Errorf("since = %d rows", len(recent))
	}
	if err := s.ClearStatus(a); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.Status(a, time.Time{}); len(rows) != 0 {
		t.Error("cleared")
	}
	if rows, _ := s.Status(b, time.Time{}); len(rows) != 1 {
		t.Error("another node's history stays")
	}
	s.Close()
	if err := s.AddStatus(a, old); err == nil {
		t.Error("closed store")
	}
	if _, err := s.Status(a, now); err == nil {
		t.Error("closed store")
	}
}

func TestMessagePaths(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	ch := ChannelConvo(0)
	add := func(sender, text string, at time.Time, out bool) Message {
		m := Message{Convo: ch, Sender: sender, Text: text, RecvAt: at, SentAt: at, Out: out}
		if err := s.AddMessage(dev, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	old := add("Bob", "hi", now.Add(-time.Hour), false)
	bob := add("Bob", "hi", now, false)
	add("me", "hi", now, true)
	add("Carol", "other", now, false)
	got, ok, err := s.Unrouted(dev, ch, "Bob", "hi", now.Add(-time.Minute))
	if err != nil || !ok || got.ID != bob.ID {
		t.Fatalf("unrouted = %+v %v %v", got, ok, err)
	}
	if err := s.SetPath(bob.ID, []byte{0x5F, 0x77}, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Unrouted(dev, ch, "Bob", "hi", now.Add(-time.Minute)); ok {
		t.Error("routed now, and the older one is outside the window")
	}
	if got, ok, _ := s.Unrouted(dev, ch, "", "", now.Add(-2*time.Hour)); !ok || got.Sender != "Carol" {
		t.Errorf("any sender and text: %+v", got)
	}
	msgs, _ := s.Messages(dev, ch, 10)
	for _, m := range msgs {
		if m.ID == bob.ID && (m.PathHash != 1 || len(m.Path) != 2 || m.Path[1] != 0x77) {
			t.Errorf("path read back = %+v", m)
		}
		if m.ID == old.ID && m.Path != nil {
			t.Error("no path")
		}
	}
}

func TestSignaturesAndPins(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	ch := ChannelConvo(1)
	m := Message{Convo: ch, Sender: "Alice", Text: "hi", SentAt: at, RecvAt: at.Add(2 * time.Second)}
	s.AddMessage(dev, &m)
	other := Message{Convo: ch, Sender: "Bob", Text: "yo", SentAt: at.Add(time.Second), RecvAt: at.Add(2 * time.Second)}
	s.AddMessage(dev, &other)
	got, err := s.AwaitingSig(dev, ch, at)
	if err != nil || len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("awaiting = %+v %v", got, err)
	}
	key := [32]byte{0xAA, 1}
	if err := s.SetSig(m.ID, SigValid, key[:]); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AwaitingSig(dev, ch, at); len(got) != 0 {
		t.Error("signed now")
	}
	msgs, _ := s.Messages(dev, ch, 10)
	if msgs[0].Sig != SigValid || [32]byte(msgs[0].SigKey) != key || msgs[1].Sig != Unsigned {
		t.Errorf("read back = %+v", msgs)
	}

	if err := s.SetPin("Alice 🛰", key, at); err != nil {
		t.Fatal(err)
	}
	pins, _ := s.Pins()
	if p, ok := pins["Alice 🛰"]; !ok || p.Key != key || !p.At.Equal(at) {
		t.Errorf("pins = %+v", pins)
	}
	s.Unpin("Alice 🛰")
	if pins, _ := s.Pins(); len(pins) != 0 {
		t.Error("unpinned")
	}
	s.Close()
	if _, err := s.Pins(); err == nil {
		t.Error("closed")
	}
}
