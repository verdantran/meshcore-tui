// Package store keeps contacts, channels and message history in SQLite, per local radio.
package store

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

// Convo identifies a conversation: "ch:<idx>" for a channel, "dm:<key hex>" for a contact,
// or "dm?:<prefix hex>" for a sender we don't have as a contact.
type Convo string

func ChannelConvo(idx int) Convo { return Convo(fmt.Sprintf("ch:%d", idx)) }

func (c Convo) IsChannel() bool       { return strings.HasPrefix(string(c), "ch:") }
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
	Repeats  int    // copies of an outgoing channel message heard coming back from repeaters
	Path     []byte // the repeaters a received message came through, from its packet; empty if not known
	PathHash int    // bytes per repeater in Path
	Sig      Sig    // a channel message's signature, when one came with it
	SigKey   []byte // the signer's public key; for a key we don't know, just the 8 bytes the signature named
}

// Sig is what a channel message's signature showed. Whether a valid one is the sender's pinned key is
// worked out when it's shown, so pinning a key later changes how earlier messages read.
type Sig int

const (
	Unsigned   Sig = iota
	SigValid       // verified with the key in SigKey
	SigBad         // a signature came, but it doesn't match the message
	SigUnknown     // signed by a key we've never heard an advert from, so it can't be checked
	SigStale       // a valid signature, but on a message sent long before it arrived, or already used: a replay
)

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
	repeats INTEGER NOT NULL DEFAULT 0, path BLOB, path_hash INTEGER NOT NULL DEFAULT 0,
	sig INTEGER NOT NULL DEFAULT 0, sig_key BLOB);
CREATE INDEX IF NOT EXISTS messages_convo ON messages (device, convo, id);
CREATE TABLE IF NOT EXISTS reads (
	device TEXT NOT NULL, convo TEXT NOT NULL, last_read INTEGER NOT NULL,
	PRIMARY KEY (device, convo));
CREATE TABLE IF NOT EXISTS pins (
	name TEXT PRIMARY KEY, key BLOB NOT NULL, at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS node_status (
	node TEXT NOT NULL, at INTEGER NOT NULL, room INTEGER NOT NULL, data BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS node_status_at ON node_status (node, at);
`

// private makes the database's folder and files readable by their owner only: it holds message history.
// SQLite gives its journal files the database's permissions, and installs from before this get theirs
// tightened too.
func private(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Open opens (or creates) the database file; ":memory:" gives a throwaway store for tests.
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := private(path); err != nil {
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
	for _, col := range []struct{ name, def string }{
		{"repeats", "INTEGER NOT NULL DEFAULT 0"},
		{"path", "BLOB"},
		{"path_hash", "INTEGER NOT NULL DEFAULT 0"},
		{"sig", "INTEGER NOT NULL DEFAULT 0"},
		{"sig_key", "BLOB"},
	} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = ?`, col.name).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN ` + col.name + ` ` + col.def); err != nil {
				return err
			}
		}
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
		c.Name = untrusted.Text(c.Name) // kept before names were cleaned as they arrived
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
		c.Name = untrusted.Text(c.Name)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) AddMessage(device string, m *Message) error {
	r, err := s.db.Exec(`INSERT INTO messages (device, convo, out, sender, text, sent_at, recv_at, hops, snr, has_snr, signed, state, ack, attempts, rtt_ms, flood, path, path_hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		device, string(m.Convo), b2i(m.Out), m.Sender, m.Text, m.SentAt.UnixMilli(), m.RecvAt.UnixMilli(), m.Hops, m.SNR,
		b2i(m.HasSNR), b2i(m.Signed), int(m.State), m.Ack, m.Attempts, m.RTT.Milliseconds(), b2i(m.Flood), m.Path, m.PathHash)
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

func (s *Store) DeleteMessage(id int64) error {
	_, err := s.db.Exec(`DELETE FROM messages WHERE id = ?`, id)
	return err
}

// SetSig records what a message's signature showed.
func (s *Store) SetSig(id int64, sig Sig, key []byte) error {
	_, err := s.db.Exec(`UPDATE messages SET sig = ?, sig_key = ? WHERE id = ?`, int(sig), key, id)
	return err
}

// AwaitingSig lists a channel conversation's received messages sent at a time that have no valid signature
// yet, for pairing with a signature that's arrived: the two carry the same timestamp.
func (s *Store) AwaitingSig(device string, c Convo, sentAt time.Time) ([]Message, error) {
	return s.queryMessages(`SELECT `+messageCols+` FROM messages WHERE device = ? AND convo = ? AND out = 0 AND sig != ? AND sent_at = ? ORDER BY id`,
		device, string(c), int(SigValid), sentAt.UnixMilli())
}

// Pin is the key a display name is trusted to sign with.
type Pin struct {
	Key [32]byte
	At  time.Time
}

func (s *Store) Pins() (map[string]Pin, error) {
	rows, err := s.db.Query(`SELECT name, key, at FROM pins`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Pin{}
	for rows.Next() {
		var name string
		var key []byte
		var at int64
		if err := rows.Scan(&name, &key, &at); err != nil {
			return nil, err
		}
		var p Pin
		copy(p.Key[:], key)
		p.At = time.UnixMilli(at)
		out[untrusted.Name(name)] = p
	}
	return out, rows.Err()
}

// SetPin pins a name in its canonical form (untrusted.Name), replacing any pin that reads the same.
func (s *Store) SetPin(name string, key [32]byte, at time.Time) error {
	if err := s.Unpin(name); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO pins VALUES (?, ?, ?)`, untrusted.Name(name), key[:], at.UnixMilli())
	return err
}

// Unpin removes every pin whose name reads the same as this one, including any kept before names were
// made canonical.
func (s *Store) Unpin(name string) error {
	rows, err := s.db.Query(`SELECT name FROM pins`)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		if untrusted.Name(n) == untrusted.Name(name) {
			gone = append(gone, n)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, n := range gone {
		if _, err := s.db.Exec(`DELETE FROM pins WHERE name = ?`, n); err != nil {
			return err
		}
	}
	return nil
}

// SetPath records the repeaters a received message came through.
func (s *Store) SetPath(id int64, path []byte, hashSize int) error {
	_, err := s.db.Exec(`UPDATE messages SET path = ?, path_hash = ? WHERE id = ?`, path, hashSize, id)
	return err
}

// Unrouted finds the newest received message in a conversation since a time whose path isn't known yet,
// from a sender and with a text when those are given.
func (s *Store) Unrouted(device string, c Convo, sender, text string, since time.Time) (Message, bool, error) {
	q := `SELECT ` + messageCols + ` FROM messages WHERE device = ? AND convo = ? AND out = 0 AND path_hash = 0 AND recv_at >= ?`
	args := []any{device, string(c), since.UnixMilli()}
	if sender != "" {
		q, args = q+` AND sender = ?`, append(args, sender)
	}
	if text != "" {
		q, args = q+` AND text = ?`, append(args, text)
	}
	msgs, err := s.queryMessages(q+` ORDER BY id DESC LIMIT 1`, args...)
	if err != nil || len(msgs) == 0 {
		return Message{}, false, err
	}
	return msgs[0], true, nil
}

// AddRepeat counts one more copy of an outgoing message heard from a repeater, and returns the total.
func (s *Store) AddRepeat(id int64) (int, error) {
	var n int
	err := s.db.QueryRow(`UPDATE messages SET repeats = repeats + 1 WHERE id = ? RETURNING repeats`, id).Scan(&n)
	return n, err
}

const messageCols = `id, convo, out, sender, text, sent_at, recv_at, hops, snr, has_snr, signed, state, ack, attempts, rtt_ms, flood, repeats, path, path_hash, sig, sig_key`

// scanMessage reads messageCols, plus any extra columns that follow them.
func scanMessage(sc interface{ Scan(...any) error }, extra ...any) (Message, error) {
	var m Message
	var convo string
	var out, hasSNR, signed, state, flood int
	var sent, recv, rtt int64
	dest := []any{&m.ID, &convo, &out, &m.Sender, &m.Text, &sent, &recv, &m.Hops, &m.SNR, &hasSNR, &signed, &state, &m.Ack, &m.Attempts, &rtt, &flood, &m.Repeats, &m.Path, &m.PathHash, &m.Sig, &m.SigKey}
	err := sc.Scan(append(dest, extra...)...)
	m.Sender, m.Text = untrusted.Text(m.Sender), untrusted.Text(m.Text)
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
