// Package chat is the messaging logic: routing incoming text to conversations, and seeing outgoing
// messages through to delivered or failed. It's a state machine driven from the UI's update loop;
// radio calls happen elsewhere and report back through the methods here.
package chat

import (
	"bufio"
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/untrusted"
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
	acks    []uint32 // every attempt's, since an earlier attempt's ack may still come
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
	rev      int                 // counts changes to contacts, for whatever is worked out from them
	byConvo  map[store.Convo]int // contacts' indexes by conversation and by key prefix; nil until wanted
	byPrefix map[[6]byte]int

	sums map[store.Convo]*summary // every conversation with messages, kept in step with each write; nil until read

	App string // what a JSON export says made it, such as "meshcore-tui v1.0.0"

	lastRun time.Time // when the app was last connected to this radio, before this connection
	live    bool      // connected, so the time is worth keeping
	seenAt  time.Time
}

// summary is a conversation's row in the list: its latest message, and what's unread.
type summary struct {
	last    store.Message
	unread  int
	mention bool
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
	s.rev++
	s.byConvo, s.sums = nil, nil
	last, err := s.store.LastSeen(s.device)
	if err != nil {
		return err
	}
	s.lastRun, s.live, s.seenAt = last, true, time.Time{}
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
		if err := s.deliver(m); err != nil {
			return err
		}
	}
	return nil
}

// LastRun is when the app was last connected to this radio before now; zero if it never was.
func (s *Service) LastRun() time.Time { return s.lastRun }

// Seen keeps, about once a minute, that the app is connected to the radio at a time.
func (s *Service) Seen(now time.Time) error {
	if !s.live || now.Sub(s.seenAt) < time.Minute {
		return nil
	}
	s.seenAt = now
	return s.store.Seen(s.device, now)
}

// Lost stops keeping the time once the link has gone: what the radio holds meanwhile is still to come.
func (s *Service) Lost() { s.live = false }

// Rev changes whenever the contacts do.
func (s *Service) Rev() int { return s.rev }

// index maps contacts by conversation and prefix; the first contact holding either wins, as a scan would find.
func (s *Service) index() {
	if s.byConvo != nil {
		return
	}
	s.byConvo, s.byPrefix = make(map[store.Convo]int, len(s.contacts)), make(map[[6]byte]int, len(s.contacts))
	for i := range s.contacts {
		s.indexOne(i)
	}
}

func (s *Service) indexOne(i int) {
	c := s.contacts[i]
	if cv := store.ContactConvo(c.Key); !hasKey(s.byConvo, cv) {
		s.byConvo[cv] = i
	}
	if p := c.Prefix(); !hasKey(s.byPrefix, p) {
		s.byPrefix[p] = i
	}
}

func hasKey[K comparable](m map[K]int, k K) bool {
	_, ok := m[k]
	return ok
}

func (s *Service) contactByPrefix(p [6]byte) (radio.Contact, bool) {
	s.index()
	if i, ok := s.byPrefix[p]; ok {
		return s.contacts[i], true
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
	s.index()
	if i, ok := s.byConvo[c]; ok {
		return s.contacts[i], true
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
	err = s.store.SetPath(m.ID, path, hashSize)
	s.touch(m.ID, err, func(l *store.Message) { l.Path, l.PathHash = path, hashSize })
	return m.ID, true, err
}

// AwaitingSig lists a channel's received messages sent at a time that have no signature yet.
func (s *Service) AwaitingSig(idx int, sentAt time.Time) ([]store.Message, error) {
	return s.store.AwaitingSig(s.device, store.ChannelConvo(idx), sentAt)
}

// SignedBefore reports whether another message like this one already has a valid signature by a key.
func (s *Service) SignedBefore(m store.Message, key []byte) (bool, error) {
	return s.store.SignedBefore(s.device, m.Convo, m.SentAt, m.Text, key, m.ID)
}

func (s *Service) SetSig(id int64, sig store.Sig, key []byte) error {
	err := s.store.SetSig(id, sig, key)
	s.touch(id, err, func(l *store.Message) { l.Sig, l.SigKey = sig, key })
	return err
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
			m.Sender, m.Text = untrusted.Name(name), text
		}
	} else if c, ok := s.contactByPrefix(in.From); ok {
		m.Convo, m.Sender = store.ContactConvo(c.Key), c.Name
		if c.Type == radio.TypeRoom && in.Author != ([4]byte{}) {
			m.Sender, m.Author = s.author(in.Author), in.Author[:]
		}
	} else {
		m.Convo, m.Sender = store.UnknownConvo(in.From), radio.KeyHex(in.From[:])
	}
	err := s.store.AddMessage(s.device, &m)
	s.added(m, err)
	return m, err
}

func (s *Service) ContactUpdated(c radio.Contact) error {
	for i, o := range s.contacts {
		if o.Key == c.Key {
			s.contacts[i] = c
			s.rev++
			return s.store.UpsertContact(s.device, c)
		}
	}
	s.contacts = append(s.contacts, c)
	s.rev++
	if s.byConvo != nil {
		s.indexOne(len(s.contacts) - 1)
	}
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
		s.added(m, err)
		return store.Message{}, Send{}, err
	}
	s.added(m, nil)
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
	err = s.store.DeleteMessage(m.ID)
	s.touch(m.ID, err, func(*store.Message) { s.sums = nil })
	return nm, send, err
}

// Result records the radio's answer to a Send. For a direct message it returns how long to wait for the ack.
func (s *Service) Result(sd Send, sent radio.Sent, err error) (store.Message, *Wait, error) {
	if sd.Channel {
		m := store.Message{ID: sd.ID, State: store.Sent, Flood: true}
		if err != nil {
			m.State = store.Failed
		}
		return m, nil, s.deliver(m)
	}
	o, ok := s.waiting[sd.ID]
	if !ok || o.attempt != sd.Attempt {
		return store.Message{}, nil, nil // superseded by a later attempt, or already settled
	}
	o.msg.Attempts = sd.Attempt + 1
	if err != nil {
		o.msg.State = store.Failed
		s.settle(sd.ID)
		return o.msg, nil, s.deliver(o.msg)
	}
	o.msg.State, o.msg.Ack, o.msg.Flood = store.Sent, sent.Ack, sent.Flood
	s.byAck[sent.Ack] = sd.ID
	o.acks = append(o.acks, sent.Ack)
	wait := sent.Timeout
	if wait <= 0 {
		wait = defaultAckWait
	}
	return o.msg, &Wait{ID: sd.ID, Attempt: sd.Attempt, Delay: time.Duration(float64(wait) * ackGrace)}, s.deliver(o.msg)
}

// Acked settles whichever message this ack belongs to, including acks for an earlier attempt.
func (s *Service) Acked(a radio.Acked) (store.Message, bool, error) {
	id, ok := s.byAck[a.Ack]
	if !ok {
		return store.Message{}, false, nil
	}
	o, ok := s.waiting[id]
	if !ok {
		delete(s.byAck, a.Ack)
		return store.Message{}, false, nil
	}
	s.settle(id)
	o.msg.State, o.msg.RTT = store.Delivered, a.RTT
	return o.msg, true, s.deliver(o.msg)
}

// settle stops waiting on a message, and on the acks of all its attempts.
func (s *Service) settle(id int64) {
	if o, ok := s.waiting[id]; ok {
		for _, a := range o.acks {
			if s.byAck[a] == id {
				delete(s.byAck, a)
			}
		}
	}
	delete(s.waiting, id)
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
		s.settle(w.ID)
		return o.msg, nil, s.deliver(o.msg)
	}
	o.attempt = next
	send := &Send{ID: w.ID, Attempt: next, To: o.to, Text: o.msg.Text}
	if next == s.retries-1 && s.floodFallback && !o.msg.Flood {
		send.ResetPath = true
	}
	o.msg.State = store.Pending
	return o.msg, send, s.deliver(o.msg)
}

func (s *Service) Messages(c store.Convo, limit int) ([]store.Message, error) {
	return s.store.Messages(s.device, c, limit)
}

func (s *Service) MarkRead(c store.Convo) error {
	if err := s.store.MarkRead(s.device, c); err != nil {
		s.sums = nil
		return err
	}
	if x := s.sums[c]; x != nil {
		x.unread, x.mention = 0, false
	}
	return nil
}

// deliver records progress on an outgoing message.
func (s *Service) deliver(m store.Message) error {
	err := s.store.UpdateDelivery(m)
	s.touch(m.ID, err, func(l *store.Message) {
		l.State, l.Ack, l.Attempts, l.RTT, l.Flood = m.State, m.Ack, m.Attempts, m.RTT.Truncate(time.Millisecond), m.Flood
	})
	return err
}

// added counts a message just written into its conversation's summary.
func (s *Service) added(m store.Message, err error) {
	if err != nil {
		s.sums = nil
	}
	if s.sums == nil {
		return
	}
	x := s.sums[m.Convo]
	if x == nil {
		x = &summary{}
		s.sums[m.Convo] = x
	}
	x.last = stored(m)
	if !m.Out {
		x.unread++
		x.mention = x.mention || m.Convo.IsChannel() && s.Mentions(x.last.Text)
	}
}

// touch applies a change to a message to its conversation's summary, if it's the latest there.
func (s *Service) touch(id int64, err error, f func(*store.Message)) {
	if err != nil {
		s.sums = nil
	}
	for _, x := range s.sums {
		if x.last.ID == id {
			f(&x.last)
			return
		}
	}
}

// stored is a message as reading it back from the store would give it.
func stored(m store.Message) store.Message {
	m.Sender, m.Text = untrusted.Line(m.Sender), untrusted.Text(m.Text)
	m.SentAt, m.RecvAt, m.RTT = time.UnixMilli(m.SentAt.UnixMilli()), time.UnixMilli(m.RecvAt.UnixMilli()), m.RTT.Truncate(time.Millisecond)
	return m
}

// loadSums reads the conversation list from the store, when it isn't already held.
func (s *Service) loadSums() error {
	if s.sums != nil {
		return nil
	}
	list, err := s.store.Conversations(s.device)
	if err != nil {
		return err
	}
	sums := make(map[store.Convo]*summary, len(list))
	for _, sm := range list {
		x := &summary{last: sm.Last, unread: sm.Unread}
		if sm.Unread > 0 && sm.Convo.IsChannel() {
			texts, err := s.store.UnreadMentioning(s.device, sm.Convo)
			if err != nil {
				return err
			}
			x.mention = slices.ContainsFunc(texts, s.Mentions)
		}
		sums[sm.Convo] = x
	}
	s.sums = sums
	return nil
}

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

// Conversations lists channels, then the direct and room conversations that have messages, most recent first.
// open adds a contact's or room's conversation with no messages yet, for one being started from Contacts.
func (s *Service) Conversations(open store.Convo) ([]Info, error) {
	if err := s.loadSums(); err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(s.channels)+len(s.sums))
	for _, ch := range s.channels {
		info := Info{Convo: store.ChannelConvo(ch.Idx), Title: ch.Name, Kind: KindChannel}
		if x := s.sums[info.Convo]; x != nil {
			last := x.last
			info.Unread, info.Mention, info.Last = x.unread, x.mention, &last
		}
		out = append(out, info)
	}
	direct := len(out)
	for c, x := range s.sums {
		if c.IsChannel() {
			continue
		}
		last := x.last
		info := Info{Convo: c, Title: s.Title(c), Kind: KindDirect, Unread: x.unread, Last: &last}
		if ct, ok := s.contactByConvo(c); ok && ct.Type == radio.TypeRoom {
			info.Kind = KindRoom
		}
		out = append(out, info)
	}
	slices.SortFunc(out[direct:], func(a, b Info) int { return cmp.Compare(b.Last.ID, a.Last.ID) })
	if ct, ok := s.contactByConvo(open); ok && s.sums[open] == nil && (ct.Type == radio.TypeChat || ct.Type == radio.TypeRoom) {
		kind := KindDirect
		if ct.Type == radio.TypeRoom {
			kind = KindRoom
		}
		out = append(out, Info{Convo: open, Title: ct.Name, Kind: kind})
	}
	return out, nil
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

// SearchLater is Search to run off the UI goroutine, given up when its context is done.
func (s *Service) SearchLater(q string, c store.Convo, limit int) func(context.Context) ([]store.Message, error) {
	st, device := s.store, s.device
	return func(ctx context.Context) ([]store.Message, error) { return st.SearchContext(ctx, device, q, c, limit) }
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
	Signature    string    `json:"signature,omitempty"`  // a channel message's: none, valid, bad, unknown or stale
	SignerKey    string    `json:"signer_key,omitempty"` // hex; only a key's first 8 bytes when it isn't known
	Verdict      string    `json:"verdict,omitempty"`    // what the signature means against who's pinned
}

// sigText is what a channel message's signature means, in words.
func sigText(m store.Message, verdict func(store.Message) string) string {
	if verdict != nil {
		if v := verdict(m); v != "" {
			return v
		}
	}
	switch {
	case m.Sig != store.Unsigned:
		return "signature " + sigNames[m.Sig]
	case !m.Out:
		return "unsigned"
	}
	return ""
}

var sigNames = map[store.Sig]string{store.Unsigned: "none", store.SigValid: "valid", store.SigBad: "bad", store.SigUnknown: "unknown", store.SigStale: "stale"}

var stateNames = map[store.State]string{store.Received: "", store.Pending: "pending", store.Sent: "sent", store.Delivered: "delivered", store.Failed: "failed"}

// Export writes a conversation, or every conversation when c is empty, as text or JSON. It returns the count.
// verdict, if given, says what a channel message's signature means; text is one line per message.
func (s *Service) Export(w io.Writer, c store.Convo, asJSON bool, verdict func(store.Message) string) (int, error) {
	bw := bufio.NewWriter(w)
	titles := map[store.Convo]string{}
	title := func(c store.Convo) string {
		t, ok := titles[c]
		if !ok {
			t = s.Title(c)
			titles[c] = t
		}
		return t
	}
	n := 0
	var last store.Convo
	if asJSON {
		bw.WriteString("{\n")
		if s.App != "" {
			app, _ := json.Marshal(s.App)
			fmt.Fprintf(bw, "  \"app\": %s,\n", app)
		}
		bw.WriteString(`  "messages": [`)
	}
	err := s.store.Each(context.Background(), s.device, c, func(m store.Message) error {
		n++
		if asJSON {
			return exportJSON(bw, m, title(m.Convo), verdict, n == 1)
		}
		if m.Convo != last {
			if last != "" {
				fmt.Fprintln(bw)
			}
			fmt.Fprintf(bw, "# %s\n\n", untrusted.Line(title(m.Convo)))
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
		if v := sigText(m, verdict); v != "" && m.Convo.IsChannel() {
			detail += "  [" + v + "]"
		}
		text := untrusted.Line(strings.ReplaceAll(untrusted.Text(m.Text), "\n", " ⏎ "))
		_, err := fmt.Fprintf(bw, "[%s] %s: %s%s\n", m.RecvAt.Local().Format("2006-01-02 15:04:05"), untrusted.Line(m.Sender), text, detail)
		return err
	})
	if err != nil {
		return n, err
	}
	switch {
	case asJSON && n == 0:
		bw.WriteString("]\n}\n")
	case asJSON:
		bw.WriteString("\n  ]\n}\n")
	}
	return n, bw.Flush()
}

// exportJSON writes one message of a JSON export, laid out as encoding/json indents the whole.
func exportJSON(w *bufio.Writer, m store.Message, title string, verdict func(store.Message) string, first bool) error {
	e := exported{Conversation: title, From: m.Sender, Outgoing: m.Out, Text: m.Text,
		Sent: m.SentAt, Received: m.RecvAt, Status: stateNames[m.State], RoundTripMS: m.RTT.Milliseconds()}
	if m.Convo.IsChannel() {
		e.Signature, e.SignerKey, e.Verdict = sigNames[m.Sig], hex.EncodeToString(m.SigKey), sigText(m, verdict)
	}
	if !m.Out {
		hops := m.Hops
		e.Hops = &hops
		if m.HasSNR {
			snr := m.SNR
			e.SNR = &snr
		}
	}
	b, err := json.MarshalIndent(e, "    ", "  ")
	if err != nil {
		return err
	}
	if !first {
		w.WriteString(",")
	}
	w.WriteString("\n    ")
	_, err = w.Write(b)
	return err
}

// ExportLater is Export to run off the UI goroutine: it works from a copy of the names conversations are
// titled by, so the radio can change them meanwhile.
func (s *Service) ExportLater(c store.Convo, asJSON bool, verdict func(store.Message) string) func(io.Writer) (int, error) {
	snap := &Service{store: s.store, device: s.device, self: s.self, contacts: slices.Clone(s.contacts), channels: slices.Clone(s.channels), App: s.App}
	return func(w io.Writer) (int, error) { return snap.Export(w, c, asJSON, verdict) }
}

// Contacts is every contact on the radio, as last synced.
func (s *Service) Contacts() []radio.Contact { return append([]radio.Contact(nil), s.contacts...) }

func (s *Service) ContactCount() int { return len(s.contacts) }

// ReplaceContacts takes a fresh copy of the radio's table, such as after an import or a removal.
func (s *Service) ReplaceContacts(cs []radio.Contact) error {
	s.contacts = cs
	s.rev++
	s.byConvo = nil
	return s.store.SaveContacts(s.device, cs)
}

// RemoveContact forgets a contact the radio dropped; its message history stays.
func (s *Service) RemoveContact(key [32]byte) error {
	for i, c := range s.contacts {
		if c.Key == key {
			s.contacts = append(s.contacts[:i], s.contacts[i+1:]...)
			s.rev++
			s.byConvo = nil
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
			s.touch(w.id, err, func(l *store.Message) { l.Repeats = n })
			return w.id, n, err == nil, err
		}
	}
	return 0, 0, false, nil
}
