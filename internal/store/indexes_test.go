package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

// plan is how SQLite would run a query, one step per line.
func plan(t *testing.T, s *Store, q string, args ...any) string {
	t.Helper()
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN `+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var steps []string
	for rows.Next() {
		var id, parent, unused int
		var step string
		rows.Scan(&id, &parent, &unused, &step)
		steps = append(steps, step)
	}
	return strings.Join(steps, "\n")
}

func TestHotQueriesDontScanTheTable(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	if Pending != 1 || Sent != 2 {
		t.Fatal("messages_unfinished and Unfinished name the states by number")
	}
	for name, q := range map[string]string{
		"Unrouted":   `SELECT id FROM messages INDEXED BY messages_recv WHERE device = 'd' AND convo = 'c' AND out = 0 AND path_hash = 0 AND recv_at >= 5 ORDER BY id DESC LIMIT 1`,
		"LastSeen":   `SELECT MAX(recv_at) FROM messages INDEXED BY messages_recv WHERE device = 'd'`,
		"Unfinished": `SELECT id FROM messages INDEXED BY messages_unfinished WHERE device = 'd' AND out = 1 AND state IN (1, 2) ORDER BY id`,
		"AddStatus":  `DELETE FROM node_status WHERE node = x'00' AND at < 5`,
		"Each":       `SELECT id FROM messages INDEXED BY messages_convo WHERE device = 'd' AND (convo, id) > ('c', 5) ORDER BY convo, id LIMIT 1000`,
	} {
		if p := plan(t, s, q); strings.Contains(p, "SCAN messages") || strings.Contains(p, "SCAN node_status") {
			t.Errorf("%s scans:\n%s", name, p)
		}
	}
	// Search walks ids from the newest down, so LIMIT stops it early instead of after a sort.
	if p := plan(t, s, `SELECT `+messageCols+` FROM messages NOT INDEXED WHERE device = ? AND text LIKE ? ORDER BY id DESC LIMIT 5`, "d", "%x%"); strings.Contains(p, "TEMP B-TREE") {
		t.Errorf("search sorts:\n%s", p)
	}
	if p := plan(t, s, `SELECT `+messageCols+` FROM messages INDEXED BY messages_convo WHERE device = ? AND convo = ? AND text LIKE ? ORDER BY id DESC LIMIT 5`, "d", "c", "%x%"); strings.Contains(p, "TEMP B-TREE") {
		t.Errorf("search in a conversation sorts:\n%s", p)
	}
}

func TestWritesSyncOnlyAtCheckpoints(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "mc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var mode int
	if s.db.QueryRow(`PRAGMA synchronous`).Scan(&mode); mode != 1 {
		t.Errorf("synchronous = %d, want NORMAL (1), which is safe under WAL", mode)
	}
}

func TestSearchOrderAndLimitHold(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range 50 {
		m := Message{Convo: ChannelConvo(i % 3), Sender: "carol", Text: "purdown " + string(rune('a'+i%26)), SentAt: t0, RecvAt: t0.Add(time.Duration(-i) * time.Minute)}
		s.AddMessage(dev, &m)
		s.AddMessage("other", &m)
	}
	all, _ := s.Search(dev, "purdown", "", 7)
	one, _ := s.Search(dev, "PURDOWN", ChannelConvo(1), 100)
	if len(all) != 7 || all[0].ID <= all[6].ID || len(one) != 17 {
		t.Fatalf("search found %d and %d", len(all), len(one))
	}
	for i := 1; i < len(one); i++ {
		if one[i].ID >= one[i-1].ID || one[i].Convo != ChannelConvo(1) {
			t.Fatalf("not newest first in one conversation: %+v", one)
		}
	}
}

func TestStatusKeysMoveToBytesAndOldHistoryGoes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mc.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	node, other := [32]byte{0xA3, 1}, [32]byte{0x6C}
	now := time.Now()
	db.Exec(`CREATE TABLE node_status (node TEXT NOT NULL, at INTEGER NOT NULL, room INTEGER NOT NULL, data BLOB NOT NULL)`)
	db.Exec(`CREATE INDEX node_status_at ON node_status (node, at)`)
	for _, r := range []struct {
		node [32]byte
		at   time.Time
	}{{node, now.Add(-time.Hour)}, {node, now.Add(-100 * 24 * time.Hour)}, {other, now.Add(-91 * 24 * time.Hour)}, {other, now.Add(-2 * time.Hour)}} {
		if _, err := db.Exec(`INSERT INTO node_status VALUES (?,?,0,?)`, radio.KeyHex(r.node[:]), r.at.UnixMilli(), []byte{1, 2}); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, n := range [][32]byte{node, other} {
		if rows, err := s.Status(n, time.Time{}); err != nil || len(rows) != 1 || rows[0].Data[1] != 2 {
			t.Fatalf("after the move, %x has %+v (%v)", n[:2], rows, err)
		}
	}
	var typ string
	s.db.QueryRow(`SELECT typeof(node) FROM node_status LIMIT 1`).Scan(&typ)
	if typ != "blob" {
		t.Errorf("keys are kept as %s", typ)
	}

	// Each poll prunes only its own node's history.
	s.db.Exec(`INSERT INTO node_status VALUES (?,?,0,x'00')`, other[:], now.Add(-95*24*time.Hour).UnixMilli())
	if err := s.AddStatus(node, StatusRow{At: now, Data: []byte{3}}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.Status(other, time.Time{}); len(rows) != 2 {
		t.Errorf("a poll of one node pruned another's: %d", len(rows))
	}
	if err := s.PruneStatus(now); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.Status(other, time.Time{}); len(rows) != 1 {
		t.Errorf("pruning left %d", len(rows))
	}
}
