// Package chat is the messaging logic: routing incoming text to conversations, and seeing outgoing
// messages through to delivered or failed. It's a state machine driven from the UI's update loop;
// radio calls happen elsewhere and report back through the methods here.
package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
)

type Kind int

const (
	KindChannel Kind = iota
	KindDirect
	KindRoom
)

// Info is one row in the conversation list.
type Info struct {
	Convo   store.Convo
	Title   string
	Kind    Kind
	Unread  int
	Mention bool // an unread message mentions you
	Last    *store.Message
}

// Send is a radio call for the UI to make; its outcome comes back through Result.
type Send struct {
	ID        int64
	Attempt   int
	Channel   bool
	Idx       int
	To        [32]byte
	Text      string
	ResetPath bool // reset the stored route first, so this attempt floods
}

// Wait asks the UI to call Timeout after Delay unless the ack arrives first.
type Wait struct {
	ID      int64
	Attempt int
	Delay   time.Duration
}

type outgoing struct {
	msg     store.Message
	to      [32]byte
	attempt int
}

type Service struct {
	store         *store.Store
	device        string
	self          string
	retries       int
	floodFallback bool

	contacts []radio.Contact
	channels []radio.Channel
	waiting  map[int64]*outgoing
	byAck    map[uint32]int64
	echoes   []echoWatch
}

// echoWatch is a channel message we sent, listened for as repeaters pass it on.
type echoWatch struct {
	id   int64
	idx  int
	text string
	at   time.Time
}

// EchoWindow is how long after sending a channel message its repeats are counted; a flood is over in seconds.
const EchoWindow = 2 * time.Minute

const (
	defaultAckWait = 10 * time.Second
	ackGrace       = 1.25
)

func New(s *store.Store, retries int, floodFallback bool) *Service {
	return &Service{store: s, retries: max(retries, 1), floodFallback: floodFallback, waiting: map[int64]*outgoing{}, byAck: map[uint32]int64{}}
}

func (s *Service) Device() string { return s.device }

// Ready loads the radio's contacts and channels. Sends left unfinished by a previous session
// can't be resumed, because the radio has forgotten their acks, so they're marked failed.
func (s *Service) Ready(r radio.Ready) error {
	s.device = radio.KeyHex(r.Self.PubKey[:])
	s.self = r.Self.Name
	s.contacts, s.channels = r.ContactList, r.ChannelList
	if err := s.store.SaveContacts(s.device, r.ContactList); err != nil {
		return err
	}
	if err := s.store.SaveChannels(s.device, r.ChannelList); err != nil {
		return err
	}
	left, err := s.store.Unfinished(s.device)
	if err != nil {
		return err
	}
	for _, m := range left {
		m.State = store.Failed
		if err := s.store.UpdateDelivery(m); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) contactByPrefix(p [6]byte) (radio.Contact, bool) {
	for _, c := range s.contacts {
		if c.Prefix() == p {
			return c, true
		}
	}
	return radio.Contact{}, false
}

// author names the writer of a room post from the first four bytes of their key.
func (s *Service) author(p [4]byte) string {
	if strings.HasPrefix(s.device, radio.KeyHex(p[:])) {
		return s.self
	}
	for _, c := range s.contacts {
		if [4]byte(c.Key[:4]) == p {
			return c.Name
		}
	}
	return radio.KeyHex(p[:])
}

func (s *Service) contactByConvo(c store.Convo) (radio.Contact, bool) {
	for _, ct := range s.contacts {
		if store.ContactConvo(ct.Key) == c {
			return ct, true
		}
	}
	return radio.Contact{}, false
}

// Contact is the contact behind a direct conversation, if it's on the radio.
func (s *Service) Contact(c store.Convo) (radio.Contact, bool) { return s.contactByConvo(c) }

func (s *Service) channel(idx int) (radio.Channel, bool) {
	for _, c := range s.channels {
		if c.Idx == idx {
			return c, true
		}
	}
	return radio.Channel{}, false
}

// AttachPath records the repeaters a received message came through, taken from its packet, on the newest
// message in the conversation since a time that matches and has no path yet. An empty sender or text matches
// any: a direct message's packet is encrypted, so only its sender is known. It reports the message's ID.
func (s *Service) AttachPath(c store.Convo, sender, text string, since time.Time, path []byte, hashSize int) (int64, bool, error) {
	m, ok, err := s.store.Unrouted(s.device, c, sender, text, since)
	if err != nil || !ok {
		return 0, false, err
	}
	return m.ID, true, s.store.SetPath(m.ID, path, hashSize)
}

// AwaitingSig lists a channel's received messages sent at a time that have no signature yet.
func (s *Service) AwaitingSig(idx int, sentAt time.Time) ([]store.Message, error) {
	return s.store.AwaitingSig(s.device, store.ChannelConvo(idx), sentAt)
}

func (s *Service) SetSig(id int64, sig store.Sig, key []byte) error {
	return s.store.SetSig(id, sig, key)
}

func (s *Service) Pins() (map[string]store.Pin, error) { return s.store.Pins() }

func (s *Service) Pin(name string, key [32]byte, at time.Time) error {
	return s.store.SetPin(name, key, at)
}

func (s *Service) Unpin(name string) error { return s.store.Unpin(name) }

// Incoming files a received message under its conversation.
func (s *Service) Incoming(in radio.Incoming) (store.Message, error) {
	m := store.Message{Text: in.Text, SentAt: in.SentAt, RecvAt: in.At, Hops: in.Hops, SNR: in.SNR, HasSNR: in.HasSNR, Signed: in.Signed, State: store.Received}
	if in.Channel {
		m.Convo = store.ChannelConvo(in.ChannelIdx)
		// Channel text arrives as "name: message"; the name is whatever the sender calls themselves.
		if name, text, ok := strings.Cut(in.Text, ": "); ok {
			m.Sender, m.Text = name, text
		}
	} else if c, ok := s.contactByPrefix(in.From); ok {
		m.Convo, m.Sender = store.ContactConvo(c.Key), c.Name
		if c.Type == radio.TypeRoom && in.Author != ([4]byte{}) {
			m.Sender = s.author(in.Author)
		}
	} else {
		m.Convo, m.Sender = store.UnknownConvo(in.From), radio.KeyHex(in.From[:])
	}
	return m, s.store.AddMessage(s.device, &m)
}

func (s *Service) ContactUpdated(c radio.Contact) error {
	for i, o := range s.contacts {
		if o.Key == c.Key {
			s.contacts[i] = c
			return s.store.UpsertContact(s.device, c)
		}
	}
	s.contacts = append(s.contacts, c)
	return s.store.UpsertContact(s.device, c)
}

// MaxBytes is how much text fits: channel messages share the firmware limit with the "name: " prefix.
func (s *Service) MaxBytes(c store.Convo) int {
	if strings.HasPrefix(string(c), "ch:") {
		return radio.MaxText - len(s.self) - 2
	}
	return radio.MaxText
}

var ErrTooLong = errors.New("message is too long")

// Compose stores a new outgoing message and returns the first radio call to make.
func (s *Service) Compose(c store.Convo, text string, now time.Time) (store.Message, Send, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return store.Message{}, Send{}, errors.New("nothing to send")
	}
	if len(text) > s.MaxBytes(c) {
		return store.Message{}, Send{}, fmt.Errorf("%w: %d of %d bytes", ErrTooLong, len(text), s.MaxBytes(c))
	}
	m := store.Message{Convo: c, Out: true, Sender: s.self, Text: text, SentAt: now, RecvAt: now, Hops: -1, State: store.Pending}
	send := Send{Text: text}
	switch {
	case strings.HasPrefix(string(c), "ch:"):
		fmt.Sscanf(string(c), "ch:%d", &send.Idx)
		if _, ok := s.channel(send.Idx); !ok {
			return store.Message{}, Send{}, errors.New("that channel isn't on the radio")
		}
		send.Channel = true
	default:
		ct, ok := s.contactByConvo(c)
		if !ok {
			return store.Message{}, Send{}, errors.New("can't reply: the sender isn't a contact on the radio")
		}
		send.To = ct.Key
		m.Flood = ct.PathLen < 0
	}
	if err := s.store.AddMessage(s.device, &m); err != nil {
		return store.Message{}, Send{}, err
	}
	send.ID = m.ID
	if send.Channel {
		s.echoes = append(s.echoes, echoWatch{m.ID, send.Idx, text, now})
	} else {
		s.waiting[m.ID] = &outgoing{msg: m, to: send.To}
	}
	return m, send, nil
}

// Retry sends a failed message again as a new one, listed from now, and drops the failed copy.
func (s *Service) Retry(m store.Message, now time.Time) (store.Message, Send, error) {
	if !m.Out || m.State != store.Failed {
		return store.Message{}, Send{}, errors.New("only a message that failed can be sent again")
	}
	nm, send, err := s.Compose(m.Convo, m.Text, now)
	if err != nil {
		return store.Message{}, Send{}, err
	}
	return nm, send, s.store.DeleteMessage(m.ID)
}

// Result records the radio's answer to a Send. For a direct message it returns how long to wait for the ack.
func (s *Service) Result(sd Send, sent radio.Sent, err error) (store.Message, *Wait, error) {
	if sd.Channel {
		m := store.Message{ID: sd.ID, State: store.Sent, Flood: true}
		if err != nil {
			m.State = store.Failed
		}
		return m, nil, s.store.UpdateDelivery(m)
	}
	o, ok := s.waiting[sd.ID]
	if !ok || o.attempt != sd.Attempt {
		return store.Message{}, nil, nil // superseded by a later attempt, or already settled
	}
	o.msg.Attempts = sd.Attempt + 1
	if err != nil {
		o.msg.State = store.Failed
		delete(s.waiting, sd.ID)
		return o.msg, nil, s.store.UpdateDelivery(o.msg)
	}
	o.msg.State, o.msg.Ack, o.msg.Flood = store.Sent, sent.Ack, sent.Flood
	s.byAck[sent.Ack] = sd.ID
	wait := sent.Timeout
	if wait <= 0 {
		wait = defaultAckWait
	}
	return o.msg, &Wait{ID: sd.ID, Attempt: sd.Attempt, Delay: time.Duration(float64(wait) * ackGrace)}, s.store.UpdateDelivery(o.msg)
}

// Acked settles whichever message this ack belongs to, including acks for an earlier attempt.
func (s *Service) Acked(a radio.Acked) (store.Message, bool, error) {
	id, ok := s.byAck[a.Ack]
	if !ok {
		return store.Message{}, false, nil
	}
	delete(s.byAck, a.Ack)
	o, ok := s.waiting[id]
	if !ok {
		return store.Message{}, false, nil
	}
	delete(s.waiting, id)
	o.msg.State, o.msg.RTT = store.Delivered, a.RTT
	return o.msg, true, s.store.UpdateDelivery(o.msg)
}

// Timeout runs when an ack didn't arrive in time: retry, flooding on the last attempt if allowed, or give up.
func (s *Service) Timeout(w Wait) (store.Message, *Send, error) {
	o, ok := s.waiting[w.ID]
	if !ok || o.attempt != w.Attempt || o.msg.State != store.Sent {
		return store.Message{}, nil, nil
	}
	next := w.Attempt + 1
	if next >= s.retries {
		o.msg.State = store.Failed
		delete(s.waiting, w.ID)
		return o.msg, nil, s.store.UpdateDelivery(o.msg)
	}
	o.attempt = next
	send := &Send{ID: w.ID, Attempt: next, To: o.to, Text: o.msg.Text}
	if next == s.retries-1 && s.floodFallback && !o.msg.Flood {
		send.ResetPath = true
	}
	o.msg.State = store.Pending
	return o.msg, send, s.store.UpdateDelivery(o.msg)
}

func (s *Service) Messages(c store.Convo, limit int) ([]store.Message, error) {
	return s.store.Messages(s.device, c, limit)
}

func (s *Service) MarkRead(c store.Convo) error { return s.store.MarkRead(s.device, c) }

func (s *Service) Title(c store.Convo) string {
	if strings.HasPrefix(string(c), "ch:") {
		var idx int
		fmt.Sscanf(string(c), "ch:%d", &idx)
		if ch, ok := s.channel(idx); ok {
			return ch.Name
		}
		return fmt.Sprintf("channel %d", idx)
	}
	if ct, ok := s.contactByConvo(c); ok {
		return ct.Name
	}
	return strings.TrimPrefix(string(c), "dm?:")
}

// Conversations lists every channel, then direct conversations: those with messages, most recent
// first, then the remaining chat and room contacts by name, so anyone can be messaged.
// Conversations lists channels, then the direct and room conversations that have messages.
// open adds a contact's or room's conversation with no messages yet, for one being started from Contacts.
func (s *Service) Conversations(open store.Convo) ([]Info, error) {
	sums, err := s.store.Conversations(s.device)
	if err != nil {
		return nil, err
	}
	byConvo := map[store.Convo]store.Summary{}
	for _, sm := range sums {
		byConvo[sm.Convo] = sm
	}
	var out []Info
	for _, ch := range s.channels {
		info := Info{Convo: store.ChannelConvo(ch.Idx), Title: ch.Name, Kind: KindChannel}
		if sm, ok := byConvo[info.Convo]; ok {
			info.Unread, info.Last = sm.Unread, &sm.Last
			if sm.Unread > 0 {
				unread, _ := s.store.Unread(s.device, info.Convo)
				for _, m := range unread {
					info.Mention = info.Mention || s.Mentions(m.Text)
				}
			}
		}
		out = append(out, info)
	}
	seen := map[store.Convo]bool{}
	var direct []Info
	for _, sm := range sums {
		if strings.HasPrefix(string(sm.Convo), "ch:") {
			continue
		}
		sm := sm
		info := Info{Convo: sm.Convo, Title: s.Title(sm.Convo), Kind: KindDirect, Unread: sm.Unread, Last: &sm.Last}
		if ct, ok := s.contactByConvo(sm.Convo); ok && ct.Type == radio.TypeRoom {
			info.Kind = KindRoom
		}
		direct = append(direct, info)
		seen[sm.Convo] = true
	}
	var rest []Info
	for _, ct := range s.contacts {
		c := store.ContactConvo(ct.Key)
		if seen[c] || c != open || ct.Type != radio.TypeChat && ct.Type != radio.TypeRoom {
			continue
		}
		kind := KindDirect
		if ct.Type == radio.TypeRoom {
			kind = KindRoom
		}
		rest = append(rest, Info{Convo: c, Title: ct.Name, Kind: kind})
	}
	return append(append(out, direct...), rest...), nil
}

// Mentions reports whether text @-mentions this radio's name, ignoring case.
func (s *Service) Mentions(text string) bool {
	if s.self == "" {
		return false
	}
	for _, m := range FindMentions(text) {
		if strings.EqualFold(m.Name, s.self) {
			return true
		}
	}
	return false
}

// Mention is an @name in a message, and the bytes [Start, End) it takes in the text.
type Mention struct {
	Start, End int
	Name       string
}

// The MeshCore apps write @[name], so names can hold spaces; a bare @name is accepted too.
var mentionRe = regexp.MustCompile(`@\[([^\]\n]+)\]|@([A-Za-z0-9_\-]+)`)

func FindMentions(text string) []Mention {
	var out []Mention
	for _, loc := range mentionRe.FindAllStringSubmatchIndex(text, -1) {
		name := ""
		if loc[2] >= 0 {
			name = text[loc[2]:loc[3]]
		} else {
			name = text[loc[4]:loc[5]]
		}
		out = append(out, Mention{loc[0], loc[1], strings.TrimSpace(name)})
	}
	return out
}

func (s *Service) Self() string { return s.self }

func (s *Service) Search(q string, c store.Convo, limit int) ([]store.Message, error) {
	return s.store.Search(s.device, q, c, limit)
}

type exported struct {
	Conversation string    `json:"conversation"`
	From         string    `json:"from"`
	Outgoing     bool      `json:"outgoing"`
	Text         string    `json:"text"`
	Sent         time.Time `json:"sent"`
	Received     time.Time `json:"received"`
	Hops         *int      `json:"hops,omitempty"`
	SNR          *float32  `json:"snr,omitempty"`
	Status       string    `json:"status,omitempty"`
	RoundTripMS  int64     `json:"round_trip_ms,omitempty"`
}

var stateNames = map[store.State]string{store.Received: "", store.Pending: "pending", store.Sent: "sent", store.Delivered: "delivered", store.Failed: "failed"}

// Export writes a conversation, or every conversation when c is empty, as text or JSON. It returns the count.
func (s *Service) Export(w io.Writer, c store.Convo, asJSON bool) (int, error) {
	ms, err := s.store.All(s.device, c)
	if err != nil {
		return 0, err
	}
	if asJSON {
		out := make([]exported, 0, len(ms))
		for _, m := range ms {
			e := exported{Conversation: s.Title(m.Convo), From: m.Sender, Outgoing: m.Out, Text: m.Text,
				Sent: m.SentAt, Received: m.RecvAt, Status: stateNames[m.State], RoundTripMS: m.RTT.Milliseconds()}
			if !m.Out {
				hops := m.Hops
				e.Hops = &hops
				if m.HasSNR {
					snr := m.SNR
					e.SNR = &snr
				}
			}
			out = append(out, e)
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return len(ms), enc.Encode(out)
	}
	var last store.Convo
	for _, m := range ms {
		if m.Convo != last {
			if last != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "# %s\n\n", s.Title(m.Convo))
			last = m.Convo
		}
		detail := ""
		switch {
		case m.Out && m.State == store.Delivered:
			detail = fmt.Sprintf("  (delivered, %.1fs)", m.RTT.Seconds())
		case m.Out && m.State == store.Failed:
			detail = "  (not delivered)"
		case !m.Out && m.Hops < 0:
			detail = "  (direct)"
		case !m.Out:
			detail = fmt.Sprintf("  (%d hops)", m.Hops)
		}
		fmt.Fprintf(w, "[%s] %s: %s%s\n", m.RecvAt.Local().Format("2006-01-02 15:04:05"), m.Sender, m.Text, detail)
	}
	return len(ms), nil
}

// Contacts is every contact on the radio, as last synced.
func (s *Service) Contacts() []radio.Contact { return append([]radio.Contact(nil), s.contacts...) }

// ReplaceContacts takes a fresh copy of the radio's table, such as after an import or a removal.
func (s *Service) ReplaceContacts(cs []radio.Contact) error {
	s.contacts = cs
	return s.store.SaveContacts(s.device, cs)
}

// RemoveContact forgets a contact the radio dropped; its message history stays.
func (s *Service) RemoveContact(key [32]byte) error {
	for i, c := range s.contacts {
		if c.Key == key {
			s.contacts = append(s.contacts[:i], s.contacts[i+1:]...)
			break
		}
	}
	return s.store.SaveContacts(s.device, s.contacts)
}

// ContactByPrefix finds a contact from the 6-byte key prefix the radio uses in pushes.
func (s *Service) ContactByPrefix(p [6]byte) (radio.Contact, bool) { return s.contactByPrefix(p) }

// Channels is the radio's channel list, secrets included, as last synced.
func (s *Service) Channels() []radio.Channel { return append([]radio.Channel(nil), s.channels...) }

// Echo counts a copy of a channel message heard on air, if it's one we sent recently: a repeater has
// passed it on. It returns which message and how many repeats it now has.
func (s *Service) Echo(idx int, sender, text string, now time.Time) (id int64, repeats int, ok bool, err error) {
	if sender != s.self {
		return 0, 0, false, nil
	}
	live := s.echoes[:0]
	for _, w := range s.echoes {
		if now.Sub(w.at) <= EchoWindow {
			live = append(live, w)
		}
	}
	s.echoes = live
	for i := len(s.echoes) - 1; i >= 0; i-- {
		if w := s.echoes[i]; w.idx == idx && w.text == text {
			n, err := s.store.AddRepeat(w.id)
			return w.id, n, err == nil, err
		}
	}
	return 0, 0, false, nil
}
