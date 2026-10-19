package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPruneMessages(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-30 * 24 * time.Hour)
	add := func(device string, c Convo, at time.Time, out bool, st State) int64 {
		m := Message{Convo: c, Out: out, Sender: "x", Text: "t", SentAt: at, RecvAt: at, State: st}
		if err := s.AddMessage(device, &m); err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	old := cutoff.Add(-time.Millisecond)
	pub := ChannelConvo(0)
	gone := []int64{
		add(dev, pub, old, false, Received),
		add(dev, pub, old.Add(-400*24*time.Hour), false, Received),
		add(dev, pub, old, true, Delivered),
		add(dev, pub, old, true, Failed),
		add("other", pub, old, false, Received),
	}
	for range 8 {
		gone = append(gone, add(dev, "dm:ab", old, false, Received))
	}
	kept := []int64{
		add(dev, pub, cutoff, false, Received), // exactly the limit's age
		add(dev, pub, old, true, Pending),
		add(dev, pub, old, true, Sent),
		add(dev, pub, now, false, Received),
		add("other", pub, now, false, Received),
	}
	s.MarkRead(dev, pub)
	add(dev, pub, now, false, Received)
	kept = append(kept, kept[len(kept)-1]+1)
	s.Seen("other", now)
	before, _ := s.LastSeen("solo")
	add("solo", pub, old, false, Received)
	gone = append(gone, kept[len(kept)-1]+1)
	solo, _ := s.LastSeen("solo")
	if !before.IsZero() || !solo.Equal(old) {
		t.Fatalf("solo last seen %v then %v", before, solo)
	}

	n, err := s.CountOlder(cutoff)
	if err != nil || n != len(gone) {
		t.Fatalf("count = %d, %v; want %d", n, err, len(gone))
	}
	defer func(was int) { pruneBatch = was }(pruneBatch)
	pruneBatch = 3 // this radio's twelve take four full batches and an empty one
	n, err = s.PruneMessages(context.Background(), cutoff)
	if err != nil || n != len(gone) {
		t.Fatalf("pruned %d, %v; want %d", n, err, len(gone))
	}
	var left []int64
	rows, _ := s.db.Query(`SELECT id FROM messages ORDER BY id`)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		left = append(left, id)
	}
	rows.Close()
	if len(left) != len(kept) {
		t.Fatalf("left %v, want %v", left, kept)
	}
	for i := range kept {
		if left[i] != kept[i] {
			t.Fatalf("left %v, want %v", left, kept)
		}
	}
	if again, _ := s.CountOlder(cutoff); again != 0 {
		t.Errorf("%d still to go", again)
	}

	// What's read stays read, and the last time with each radio is still known.
	sums, _ := s.Conversations(dev)
	if len(sums) != 1 || sums[0].Convo != pub || sums[0].Unread != 1 {
		t.Errorf("conversations after = %+v", sums)
	}
	if after, _ := s.LastSeen("solo"); !after.Equal(solo) {
		t.Errorf("solo last seen %v, was %v", after, solo)
	}
	if after, _ := s.LastSeen("other"); !after.Equal(now) {
		t.Errorf("other last seen %v", after)
	}
	if n, err := s.PruneMessages(context.Background(), cutoff); n != 0 || err != nil {
		t.Errorf("second prune %d, %v", n, err)
	}

	ctx, stop := context.WithCancel(context.Background())
	stop()
	if _, err := s.PruneMessages(ctx, now.Add(time.Hour)); err == nil {
		t.Error("a stopped prune carried on")
	}
}

func TestPruneUsesTheIndexes(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	for name, q := range map[string]string{
		"devices": devices,
		"count":   `SELECT COUNT(*) FROM messages INDEXED BY messages_recv WHERE device IN (` + devices + `) AND recv_at < 5`,
		"kept":    `SELECT COUNT(*) FROM messages INDEXED BY messages_unfinished WHERE ` + unfinished + ` AND recv_at < 5`,
		"delete":  `DELETE FROM messages WHERE id IN (SELECT id FROM messages INDEXED BY messages_recv WHERE device = 'd' AND recv_at < 5 AND NOT (` + unfinished + `) LIMIT 5)`,
		"seen":    `SELECT device, MAX(recv_at) FROM messages INDEXED BY messages_recv WHERE device = 'd' AND recv_at < 5 GROUP BY device`,
	} {
		if p := plan(t, s, q); strings.Contains(p, "SCAN messages\n") || strings.HasSuffix(p, "SCAN messages") {
			t.Errorf("%s scans:\n%s", name, p)
		}
	}
}
