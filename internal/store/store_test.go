package store

import (
	"path/filepath"
	"testing"
	"time"

	"meshcore-tui/internal/radio"
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
