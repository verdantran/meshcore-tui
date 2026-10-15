// Package store keeps contacts, channels and message history in SQLite, per local radio.
package store

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"meshcore-tui/internal/radio"
)

// Convo identifies a conversation: "ch:<idx>" for a channel, "dm:<key hex>" for a contact,
// or "dm?:<prefix hex>" for a sender we don't have as a contact.
type Convo string

func ChannelConvo(idx int) Convo      { return Convo(fmt.Sprintf("ch:%d", idx)) }
func ContactConvo(key [32]byte) Convo { return Convo("dm:" + radio.KeyHex(key[:])) }
func UnknownConvo(p [6]byte) Convo    { return Convo("dm?:" + radio.KeyHex(p[:])) }

type State int

const (
	Received  State = iota
	Pending         // queued to send, not yet accepted by the radio
	Sent            // the radio transmitted it; a direct message now waits for its ack
	Delivered       // acked
	Failed          // every attempt went unacked, or the radio refused it
)

type Message struct {
	ID       int64
	Convo    Convo
	Out      bool
	Sender   string
	Text     string
	SentAt   time.Time
	RecvAt   time.Time
	Hops     int // -1 for a direct route
	SNR      float32
	HasSNR   bool
	Signed   bool
	State    State
	Ack      uint32
	Attempts int
	RTT      time.Duration
	Flood    bool
	Repeats  int // copies of an outgoing channel message heard coming back from repeaters
}

type Summary struct {
	Convo  Convo
	Last   Message
	Unread int
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS contacts (
	device TEXT NOT NULL, key TEXT NOT NULL, name TEXT NOT NULL, type INTEGER NOT NULL, flags INTEGER NOT NULL,
	path_len INTEGER NOT NULL, path BLOB, last_advert INTEGER NOT NULL, lat REAL NOT NULL, lon REAL NOT NULL,
	PRIMARY KEY (device, key));
CREATE TABLE IF NOT EXISTS channels (
	device TEXT NOT NULL, idx INTEGER NOT NULL, name TEXT NOT NULL, secret BLOB,
	PRIMARY KEY (device, idx));
CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT, device TEXT NOT NULL, convo TEXT NOT NULL, out INTEGER NOT NULL,
	sender TEXT NOT NULL, text TEXT NOT NULL, sent_at INTEGER NOT NULL, recv_at INTEGER NOT NULL,
	hops INTEGER NOT NULL, snr REAL NOT NULL, has_snr INTEGER NOT NULL, signed INTEGER NOT NULL,
	state INTEGER NOT NULL, ack INTEGER NOT NULL, attempts INTEGER NOT NULL, rtt_ms INTEGER NOT NULL, flood INTEGER NOT NULL,
	repeats INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS messages_convo ON messages (device, convo, id);
CREATE TABLE IF NOT EXISTS reads (
	device TEXT NOT NULL, convo TEXT NOT NULL, last_read INTEGER NOT NULL,
	PRIMARY KEY (device, convo));
CREATE TABLE IF NOT EXISTS node_status (
	node TEXT NOT NULL, at INTEGER NOT NULL, room INTEGER NOT NULL, data BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS node_status_at ON node_status (node, at);
`

// Open opens (or creates) the database file; ":memory:" gives a throwaway store for tests.
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; an in-memory database also needs a single connection
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate adds columns that databases made by earlier versions don't have.
func migrate(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'repeats'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		_, err := db.Exec(`ALTER TABLE messages ADD COLUMN repeats INTEGER NOT NULL DEFAULT 0`)
		return err
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SaveContacts replaces the device's contact list with what the radio holds now.
func (s *Store) SaveContacts(device string, cs []radio.Contact) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM contacts WHERE device = ?`, device); err != nil {
		return err
	}
	for _, c := range cs {
		if err := upsertContact(tx, device, c); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

func upsertContact(e execer, device string, c radio.Contact) error {
	_, err := e.Exec(`INSERT OR REPLACE INTO contacts VALUES (?,?,?,?,?,?,?,?,?,?)`,
		device, radio.KeyHex(c.Key[:]), c.Name, int(c.Type), int(c.Flags), c.PathLen, c.Path, c.LastAdvert.Unix(), c.Lat, c.Lon)
	return err
}

func (s *Store) UpsertContact(device string, c radio.Contact) error {
	return upsertContact(s.db, device, c)
}

func (s *Store) Contacts(device string) ([]radio.Contact, error) {
	rows, err := s.db.Query(`SELECT key, name, type, flags, path_len, path, last_advert, lat, lon FROM contacts WHERE device = ? ORDER BY name COLLATE NOCASE`, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []radio.Contact
	for rows.Next() {
		var c radio.Contact
		var keyHex string
		var typ, flags int
		var advert int64
		if err := rows.Scan(&keyHex, &c.Name, &typ, &flags, &c.PathLen, &c.Path, &advert, &c.Lat, &c.Lon); err != nil {
			return nil, err
		}
		hex.Decode(c.Key[:], []byte(keyHex))
		c.Type, c.Flags, c.LastAdvert = radio.ContactType(typ), byte(flags), time.Unix(advert, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SaveChannels(device string, cs []radio.Channel) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM channels WHERE device = ?`, device); err != nil {
		return err
	}
	for _, c := range cs {
		if _, err := tx.Exec(`INSERT INTO channels VALUES (?,?,?,?)`, device, c.Idx, c.Name, c.Secret[:]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Channels(device string) ([]radio.Channel, error) {
	rows, err := s.db.Query(`SELECT idx, name, secret FROM channels WHERE device = ? ORDER BY idx`, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []radio.Channel
	for rows.Next() {
		var c radio.Channel
		var secret []byte
		if err := rows.Scan(&c.Idx, &c.Name, &secret); err != nil {
			return nil, err
		}
		copy(c.Secret[:], secret)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) AddMessage(device string, m *Message) error {
	r, err := s.db.Exec(`INSERT INTO messages (device, convo, out, sender, text, sent_at, recv_at, hops, snr, has_snr, signed, state, ack, attempts, rtt_ms, flood)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		device, string(m.Convo), b2i(m.Out), m.Sender, m.Text, m.SentAt.UnixMilli(), m.RecvAt.UnixMilli(), m.Hops, m.SNR,
		b2i(m.HasSNR), b2i(m.Signed), int(m.State), m.Ack, m.Attempts, m.RTT.Milliseconds(), b2i(m.Flood))
	if err != nil {
		return err
	}
	m.ID, err = r.LastInsertId()
	return err
}

// UpdateDelivery records progress on an outgoing message.
func (s *Store) UpdateDelivery(m Message) error {
	_, err := s.db.Exec(`UPDATE messages SET state = ?, ack = ?, attempts = ?, rtt_ms = ?, flood = ? WHERE id = ?`,
		int(m.State), m.Ack, m.Attempts, m.RTT.Milliseconds(), b2i(m.Flood), m.ID)
	return err
}

// AddRepeat counts one more copy of an outgoing message heard from a repeater, and returns the total.
func (s *Store) AddRepeat(id int64) (int, error) {
	var n int
	err := s.db.QueryRow(`UPDATE messages SET repeats = repeats + 1 WHERE id = ? RETURNING repeats`, id).Scan(&n)
	return n, err
}

const messageCols = `id, convo, out, sender, text, sent_at, recv_at, hops, snr, has_snr, signed, state, ack, attempts, rtt_ms, flood, repeats`

// scanMessage reads messageCols, plus any extra columns that follow them.
func scanMessage(sc interface{ Scan(...any) error }, extra ...any) (Message, error) {
	var m Message
	var convo string
	var out, hasSNR, signed, state, flood int
	var sent, recv, rtt int64
	dest := []any{&m.ID, &convo, &out, &m.Sender, &m.Text, &sent, &recv, &m.Hops, &m.SNR, &hasSNR, &signed, &state, &m.Ack, &m.Attempts, &rtt, &flood, &m.Repeats}
	err := sc.Scan(append(dest, extra...)...)
	m.Convo, m.Out, m.HasSNR, m.Signed, m.State, m.Flood = Convo(convo), out == 1, hasSNR == 1, signed == 1, State(state), flood == 1
	m.SentAt, m.RecvAt, m.RTT = time.UnixMilli(sent), time.UnixMilli(recv), time.Duration(rtt)*time.Millisecond
	return m, err
}

// Messages returns up to limit of the latest messages in a conversation, oldest first.
func (s *Store) Messages(device string, c Convo, limit int) ([]Message, error) {
	return s.queryMessages(`SELECT `+messageCols+` FROM (SELECT * FROM messages WHERE device = ? AND convo = ? ORDER BY id DESC LIMIT ?) ORDER BY id`, device, string(c), limit)
}

// Unfinished lists outgoing messages still waiting on the radio or an ack, e.g. after a restart.
func (s *Store) Unfinished(device string) ([]Message, error) {
	return s.queryMessages(`SELECT `+messageCols+` FROM messages WHERE device = ? AND out = 1 AND state IN (?, ?) ORDER BY id`, device, int(Pending), int(Sent))
}

// Conversations summarises every conversation with messages: the latest one and the unread count.
func (s *Store) Conversations(device string) ([]Summary, error) {
	rows, err := s.db.Query(`
		SELECT m.`+strings.ReplaceAll(messageCols, ", ", ", m.")+`, (SELECT COUNT(*) FROM messages u WHERE u.device = m.device AND u.convo = m.convo AND u.out = 0
			AND u.id > COALESCE((SELECT last_read FROM reads r WHERE r.device = m.device AND r.convo = m.convo), 0))
		FROM messages m WHERE m.device = ? AND m.id = (SELECT MAX(id) FROM messages x WHERE x.device = m.device AND x.convo = m.convo)
		ORDER BY m.id DESC`, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var sum Summary
		m, err := scanMessage(rows, &sum.Unread)
		if err != nil {
			return nil, err
		}
		sum.Convo, sum.Last = m.Convo, m
		out = append(out, sum)
	}
	return out, rows.Err()
}

// DeleteConvo removes a conversation's history, for a channel whose slot is being freed.
func (s *Store) DeleteConvo(device string, c Convo) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM messages WHERE device = ? AND convo = ?`, `DELETE FROM reads WHERE device = ? AND convo = ?`} {
		if _, err := tx.Exec(q, device, string(c)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) MarkRead(device string, c Convo) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO reads VALUES (?, ?, COALESCE((SELECT MAX(id) FROM messages WHERE device = ? AND convo = ?), 0))`,
		device, string(c), device, string(c))
	return err
}

func (s *Store) queryMessages(query string, args ...any) ([]Message, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// Search finds messages whose text or sender contains q, newest first. An empty convo searches everything.
func (s *Store) Search(device, q string, c Convo, limit int) ([]Message, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	if c == "" {
		return s.queryMessages(`SELECT `+messageCols+` FROM messages WHERE device = ? AND (text LIKE ? ESCAPE '\' OR sender LIKE ? ESCAPE '\') ORDER BY id DESC LIMIT ?`,
			device, likePattern(q), likePattern(q), limit)
	}
	return s.queryMessages(`SELECT `+messageCols+` FROM messages WHERE device = ? AND convo = ? AND (text LIKE ? ESCAPE '\' OR sender LIKE ? ESCAPE '\') ORDER BY id DESC LIMIT ?`,
		device, string(c), likePattern(q), likePattern(q), limit)
}

// All returns every message in a conversation, or in all of them when convo is empty, oldest first.
func (s *Store) All(device string, c Convo) ([]Message, error) {
	if c == "" {
		return s.queryMessages(`SELECT `+messageCols+` FROM messages WHERE device = ? ORDER BY convo, id`, device)
	}
	return s.queryMessages(`SELECT `+messageCols+` FROM messages WHERE device = ? AND convo = ? ORDER BY id`, device, string(c))
}

// Unread returns the incoming messages in a conversation that haven't been read yet.
func (s *Store) Unread(device string, c Convo) ([]Message, error) {
	return s.queryMessages(`SELECT `+messageCols+` FROM messages WHERE device = ? AND convo = ? AND out = 0
		AND id > COALESCE((SELECT last_read FROM reads WHERE device = ? AND convo = ?), 0) ORDER BY id`,
		device, string(c), device, string(c))
}

// StatusRow is one status reply from a watched repeater or room server, as the node sent it.
type StatusRow struct {
	At   time.Time
	Room bool
	Data []byte
}

// statusKeep is how long watched nodes' status history is kept.
const statusKeep = 90 * 24 * time.Hour

// AddStatus keeps a status reply. Status history belongs to the node, not to the radio that asked.
func (s *Store) AddStatus(node [32]byte, r StatusRow) error {
	if _, err := s.db.Exec(`INSERT INTO node_status VALUES (?,?,?,?)`, radio.KeyHex(node[:]), r.At.UnixMilli(), b2i(r.Room), r.Data); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM node_status WHERE at < ?`, r.At.Add(-statusKeep).UnixMilli())
	return err
}

// Status returns a node's status history since a time, oldest first.
func (s *Store) Status(node [32]byte, since time.Time) ([]StatusRow, error) {
	rows, err := s.db.Query(`SELECT at, room, data FROM node_status WHERE node = ? AND at >= ? ORDER BY at`, radio.KeyHex(node[:]), since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatusRow
	for rows.Next() {
		var r StatusRow
		var at int64
		var room int
		if err := rows.Scan(&at, &room, &r.Data); err != nil {
			return nil, err
		}
		r.At, r.Room = time.UnixMilli(at), room == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClearStatus deletes a node's status history.
func (s *Store) ClearStatus(node [32]byte) error {
	_, err := s.db.Exec(`DELETE FROM node_status WHERE node = ?`, radio.KeyHex(node[:]))
	return err
}
