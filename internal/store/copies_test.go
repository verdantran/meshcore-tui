package store

import (
	"context"
	"testing"
	"time"
)

func TestCopiesAreKeptWithTheirMessage(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	out := Message{Convo: ChannelConvo(1), Out: true, Sender: "me", Text: "test", SentAt: t0, RecvAt: t0, State: Sent}
	in := Message{Convo: ChannelConvo(1), Sender: "Zed", Text: "hello", SentAt: t0, RecvAt: t0, SNR: 4, HasSNR: true}
	s.AddMessage("dev", &out)
	s.AddMessage("dev", &in)
	for _, c := range []Copy{{At: t0.Add(time.Second), SNR: -3.5, RSSI: -110, Path: []byte{0xA3}, PathHash: 1}, {At: t0.Add(2 * time.Second), SNR: 6.25, RSSI: -80, Path: []byte{0x5F, 0x77}, PathHash: 1}} {
		if err := s.AddCopy("dev", out.ID, c); err != nil {
			t.Fatal(err)
		}
	}
	s.AddCopy("dev", in.ID, Copy{At: t0, SNR: 4, RSSI: -95})
	cs, err := s.Copies("dev", out.ID)
	if err != nil || len(cs) != 2 || cs[0].RSSI != -110 || cs[1].SNR != 6.25 || string(cs[1].Path) != "\x5F\x77" || !cs[0].At.Equal(t0.Add(time.Second)) {
		t.Fatalf("copies = %+v %v", cs, err)
	}
	msgs, _ := s.Messages("dev", ChannelConvo(1), 10)
	if !msgs[0].HasEcho || msgs[0].EchoSNR != 6.25 || msgs[1].HasEcho {
		t.Errorf("best repeat: sent %v %v, received %v", msgs[0].HasEcho, msgs[0].EchoSNR, msgs[1].HasEcho)
	}

	s.DeleteMessage(out.ID)
	if cs, _ := s.Copies("dev", out.ID); len(cs) != 0 {
		t.Errorf("a deleted message's copies stayed: %+v", cs)
	}
	s.DeleteConvo("dev", ChannelConvo(1))
	if cs, _ := s.Copies("dev", in.ID); len(cs) != 0 {
		t.Errorf("a cleared conversation's copies stayed: %+v", cs)
	}
}

func TestOldCopiesArePruned(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	m := Message{Convo: ChannelConvo(0), Sender: "Zed", Text: "old", SentAt: t0, RecvAt: t0}
	s.AddMessage("dev", &m)
	s.AddCopy("dev", m.ID, Copy{At: t0, SNR: 1, RSSI: -90})
	if _, err := s.PruneMessages(context.Background(), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM copies`).Scan(&n)
	if n != 0 {
		t.Errorf("%d copies left after pruning", n)
	}
}
