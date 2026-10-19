package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/packets"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/signing"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

// Signed channel messages (meshcore-companion's SIGNING.md): a signed message is sent as usual, then a datagram on
// the same channel carries the radio's signature over it. They arrive separately and pair up by channel and
// timestamp. Checking signatures costs nothing, so it always runs; signing your own is chosen per channel,
// and per message with ctrl+s, since each signed message is two packets.

const (
	sigHold    = 15 * time.Minute // every signature is kept this long, for messages it may yet be for
	sigHeld    = 256              // and at most this many at once
	sigFresh   = 15 * time.Minute // a message that took longer than this to arrive is too old to vouch for
	sigQueued  = 24 * time.Hour   // the same for one the radio held while the app was away
	sigWait    = 30 * time.Second // after this, a message from someone who signs, without a signature, is flagged
	signingMin = "v1.15.0"        // the firmware that sends and receives channel datagrams
	senderMax  = 24               // cells a sender's name may take in a message's header
)

type pendingSig struct {
	idx   int
	d     signing.Datagram
	at    time.Time
	on    int64     // the message it was put on, if any
	onSig store.Sig // and what it showed there
}

// usedSig is the message a signature was valid for, so the same signature on another is known as a replay.
type usedSig struct {
	idx int
	id  int64
	at  time.Time
}

// canSign says why signing isn't possible on this radio, or nil.
func (m *Model) canSign() error {
	if _, ok := m.radio.(radio.Signer); !ok {
		return errors.New("this radio can't sign messages")
	}
	if fw := m.ready.Device.Firmware; fw != "" && admin.CompareVersions(fw, signingMin) < 0 {
		return fmt.Errorf("signing needs firmware %s or later; this radio has %s", signingMin, fw)
	}
	return nil
}

// signedChannel reports whether your messages in a conversation's channel are signed. The setting goes by the
// channel's secret, so a later channel that reuses its name or slot doesn't take it on.
func (m *Model) signedChannel(c store.Convo) bool {
	if m.opt.Config == nil || m.chat == nil {
		return false
	}
	ch, ok := m.chat.ChannelFor(c)
	return ok && m.opt.Config.Signs(ch.Secret)
}

// signThis reports whether the message being written will be signed: the channel's setting, flipped by ctrl+s.
func (m *Model) signThis() bool {
	cur, ok := m.selectedConvo()
	return ok && cur.Kind == chat.KindChannel && m.signedChannel(cur.Convo) != m.chats.signFlip
}

// moveSigning turns signing settings kept by channel name into ones kept by secret, for the channels on
// this radio. Names it doesn't have are dropped: a channel added under one later is another channel.
func (m *Model) moveSigning() {
	cfg := m.opt.Config
	if cfg == nil || m.chat == nil || len(cfg.Messages.SignChannels) == 0 {
		return
	}
	secrets := map[string][16]byte{}
	for _, ch := range m.chat.Channels() {
		secrets[ch.Name] = ch.Secret
	}
	cfg.MoveSigning(secrets)
	m.saveConfig()
}

// forgetSigs drops what's held about a channel's signatures once it's left, since its slot may be reused.
func (m *Model) forgetSigs(idx int) {
	m.chats.sigs = slices.DeleteFunc(m.chats.sigs, func(p pendingSig) bool { return p.idx == idx })
	for k := range m.chats.signedMsgs {
		if strings.HasPrefix(k, fmt.Sprintf("%d|", idx)) {
			delete(m.chats.signedMsgs, k)
		}
	}
	for sig, u := range m.chats.usedSigs {
		if u.idx == idx {
			delete(m.chats.usedSigs, sig)
		}
	}
}

// toggleChannelSigning turns signing your messages on an open channel on or off.
func (m *Model) toggleChannelSigning() {
	cur, ok := m.selectedConvo()
	if !ok || cur.Kind != chat.KindChannel {
		m.fail("SIGNING  only channel messages are signed; direct messages are already encrypted to one person")
		return
	}
	if err := m.canSign(); err != nil && !m.signedChannel(cur.Convo) {
		m.fail("SIGNING  " + err.Error())
		return
	}
	cfg := m.opt.Config
	if cfg == nil {
		m.fail("SIGNING  there's no config file to keep the setting in")
		return
	}
	ch, ok := m.chat.ChannelFor(cur.Convo)
	if !ok {
		m.fail("SIGNING  that channel isn't on the radio")
		return
	}
	on := !m.signedChannel(cur.Convo)
	cfg.SetSigning(ch.Secret, on)
	if on {
		m.addLog(m.now, "SIGNING ON  "+cur.Title+": your messages here are signed, which sends each as two packets", radio.ToneOK)
	} else {
		m.addLog(m.now, "SIGNING OFF  "+cur.Title, radio.ToneInfo)
	}
	m.saveConfig()
}

type signedSentMsg struct {
	send    chat.Send
	signErr error
}

// signedSendCmd sends a channel message, then its signature: the radio signs the message with its own key.
func (m *Model) signedSendCmd(s chat.Send, channel string) tea.Cmd {
	signer, _ := m.radio.(radio.Signer)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), sendLimit)
		defer cancel()
		ts, err := signer.SendChannelAt(ctx, s.Idx, s.Text)
		if err != nil {
			return sendResultMsg{send: s, err: err}
		}
		sig, err := signer.Sign(ctx, signing.Canonical(channel, ts, s.Text))
		if err == nil {
			err = signer.SendChannelData(ctx, s.Idx, signing.DataType, signing.Datagram{TS: ts, Sig: sig, KeyID: [8]byte(m.ready.Self.PubKey[:8])}.Bytes())
		}
		return signedSentMsg{s, err}
	}
}

func (m *Model) signedSent(r signedSentMsg) tea.Cmd {
	cmd := m.chatResult(sendResultMsg{send: r.send})
	if r.signErr != nil {
		m.addLog(m.now, "SENT UNSIGNED  the message went, but its signature didn't: "+radio.Explain(r.signErr), radio.ToneWarn)
		return cmd
	}
	if err := m.chat.SetSig(r.send.ID, store.SigValid, m.ready.Self.PubKey[:]); err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
	}
	m.chatsChanged(sendConvo(r.send))
	return cmd
}

// pins are the keys names are trusted to sign with, read from the store the first time they're wanted.
func (m *Model) pins() map[string]store.Pin {
	if m.chats.pins == nil && m.chat != nil {
		p, err := m.chat.Pins()
		if err != nil {
			m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
		}
		for _, n := range slices.Sorted(maps.Keys(p)) {
			k := p[n].Key
			if p[n].Clash {
				m.addLog(m.now, fmt.Sprintf("PIN CONFLICT  two pins read as %s, with different keys: kept the newer, to key %s", n, radio.KeyHex(k[:4])), radio.ToneWarn)
			}
		}
		m.chats.pins = p
		m.chats.pinsRev++
	}
	return m.chats.pins
}

func (m *Model) pin(name string, key [32]byte) {
	if err := m.chat.Pin(name, key, m.now); err != nil {
		m.fail("PIN  " + err.Error())
		return
	}
	m.chats.pins = nil
	m.addLog(m.now, fmt.Sprintf("PINNED  %s to key %s: their signed messages show ✓, and anyone else's under that name is flagged", name, radio.KeyHex(key[:4])), radio.ToneOK)
}

func (m *Model) unpin(name string) {
	if err := m.chat.Unpin(name); err != nil {
		m.fail("PIN  " + err.Error())
		return
	}
	m.chats.pins = nil
	m.addLog(m.now, "UNPINNED  "+name, radio.ToneInfo)
}

// knownKeys are full public keys we could check a signature with: us, contacts, nodes heard, and pins.
func (m *Model) knownKeys() [][32]byte {
	keys := [][32]byte{m.ready.Self.PubKey}
	if m.chat != nil {
		for _, c := range m.chat.Contacts() {
			keys = append(keys, c.Key)
		}
	}
	for _, h := range m.contacts.heard {
		keys = append(keys, h.c.Key)
	}
	for _, p := range m.pins() {
		keys = append(keys, p.Key)
	}
	return keys
}

// check verifies a signature over a message with every known key it could be. A valid one is stale when the
// message is old or the signature has already vouched for another.
func (m *Model) check(d signing.Datagram, channel string, msg store.Message) (store.Sig, []byte) {
	tried := false
	for _, k := range m.knownKeys() {
		if !d.Matches(k) {
			continue
		}
		tried = true
		if d.Verify(k, channel, msg.Text) {
			fresh := sigFresh
			if _, ok := m.chats.queued[msg.ID]; ok {
				fresh = sigQueued
				if last := m.chat.LastRun(); !last.IsZero() {
					fresh = min(fresh, max(msg.RecvAt.Sub(last), 0)+sigFresh) // so it was sent since the app last had the radio
				}
			}
			if u, ok := m.chats.usedSigs[d.Sig]; ok && u.id != msg.ID || msg.RecvAt.Sub(msg.SentAt).Abs() > fresh {
				return store.SigStale, k[:]
			}
			again, err := m.chat.SignedBefore(msg, k[:])
			if err != nil {
				m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
			}
			if again || err != nil {
				return store.SigStale, k[:]
			}
			return store.SigValid, k[:]
		}
	}
	if !tried {
		return store.SigUnknown, d.KeyID[:]
	}
	return store.SigBad, d.KeyID[:]
}

func (m *Model) channelName(idx int) (string, bool) {
	for _, ch := range m.chat.Channels() {
		if ch.Idx == idx {
			return ch.Name, true
		}
	}
	return "", false
}

// onChannelData pairs a signature with the message it signs, and holds it for any message at its timestamp
// still to come: a forgery that arrives first mustn't stop the real signature counting.
func (m *Model) onChannelData(e radio.ChannelData) {
	if e.Type != signing.DataType || m.chat == nil {
		return
	}
	d, ok := signing.Parse(e.Data)
	if !ok {
		return
	}
	live := m.chats.sigs[:0]
	for _, p := range m.chats.sigs {
		if m.now.Sub(p.at) < sigHold {
			live = append(live, p)
		}
	}
	if len(live) >= sigHeld {
		live = append(live[:0], live[len(live)-sigHeld+1:]...)
	}
	m.chats.sigs = append(live, pendingSig{idx: e.Idx, d: d, at: m.now})
	for sig, u := range m.chats.usedSigs {
		if m.now.Sub(u.at) > sigHold+sigFresh {
			delete(m.chats.usedSigs, sig)
		}
	}
	m.applySig(&m.chats.sigs[len(m.chats.sigs)-1])
}

func sigRank(s store.Sig) int {
	return slices.Index([]store.Sig{store.Unsigned, store.SigBad, store.SigUnknown, store.SigStale, store.SigValid}, s)
}

// applySig checks a signature against the channel's messages sent at its timestamp, recording the outcome
// where it's better than what the message shows.
func (m *Model) applySig(p *pendingSig) {
	name, ok := m.channelName(p.idx)
	if !ok {
		return
	}
	msgs, err := m.chat.AwaitingSig(p.idx, time.Unix(int64(p.d.TS), 0))
	if err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
	}
	// Two messages can share a second; the signature's text decides which it's for. One that fits none
	// marks only the first message without a signature.
	var best store.Message
	sig, key := store.Unsigned, []byte(nil)
	for _, msg := range msgs {
		if msg.Sig == store.SigStale {
			continue // a replay stays one, even once its signature's been forgotten
		}
		s, k := m.check(p.d, name, msg)
		failed := s == store.SigBad || s == store.SigUnknown
		if failed && (p.on != 0 || msg.Sig != store.Unsigned) || sigRank(s) <= sigRank(msg.Sig) || sigRank(s) <= sigRank(sig) {
			continue
		}
		best, sig, key = msg, s, k
	}
	if sig == store.Unsigned {
		return
	}
	if p.on != 0 && p.on != best.ID {
		for _, msg := range msgs {
			if msg.ID == p.on && msg.Sig == p.onSig {
				m.setSig(msg, p.idx, p.d.TS, store.Unsigned, nil)
			}
		}
	}
	p.on, p.onSig = best.ID, sig
	if sig == store.SigValid {
		m.chats.usedSigs[p.d.Sig] = usedSig{p.idx, best.ID, m.now}
	}
	m.setSig(best, p.idx, p.d.TS, sig, key)
	switch {
	case sig == store.SigBad:
		m.addLog(m.now, fmt.Sprintf("SIGNATURE  %s's message in %s has a signature that doesn't match it", best.Sender, name), radio.ToneFail)
	case sig == store.SigStale:
		m.addLog(m.now, fmt.Sprintf("SIGNATURE  %s's message in %s is an old one sent again", best.Sender, name), radio.ToneFail)
	case sig == store.SigValid && m.pinnedOther(best.Sender, key):
		m.addLog(m.now, fmt.Sprintf("SIGNATURE  a message as %s in %s was signed by a different key from theirs", best.Sender, name), radio.ToneFail)
	}
	m.chatsChanged(best.Convo)
}

func (m *Model) setSig(msg store.Message, idx int, ts uint32, sig store.Sig, key []byte) {
	if err := m.chat.SetSig(msg.ID, sig, key); err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
	}
	msg.Sig, msg.SigKey = sig, key
	m.chats.signedMsgs[sigKey(idx, ts)] = msg
	m.trimSigned()
}

// sigArrived tries the signatures held for a channel message's timestamp on it.
func (m *Model) sigArrived(msg store.Message) {
	var idx int
	if _, err := fmt.Sscanf(string(msg.Convo), "ch:%d", &idx); err != nil {
		return
	}
	for i, p := range m.chats.sigs {
		if p.idx == idx && int64(p.d.TS) == msg.SentAt.Unix() {
			m.applySig(&m.chats.sigs[i])
		}
	}
}

func (m *Model) pinOf(name string) (store.Pin, bool) {
	p, ok := m.pins()[m.names().canonical(name)]
	return p, ok
}

func (m *Model) pinnedOther(name string, key []byte) bool {
	p, ok := m.pinOf(name)
	return ok && !bytes.Equal(p.Key[:], key)
}

// lookalike is the pinned name a name could be taken for on screen, other than its own.
func (m *Model) lookalike(name string) (string, store.Pin, bool) {
	pins := m.pins()
	if len(pins) == 0 {
		return "", store.Pin{}, false
	}
	ix := m.names()
	canon, like := ix.canonical(name), ""
	for _, n := range ix.pinned[ix.skeleton(name)] {
		if n != canon && (like == "" || n < like) {
			like = n
		}
	}
	return like, pins[like], like != ""
}

// namesake is the name of a contact or heard node that reads like name but has another key, contacts first.
func (m *Model) namesake(name string, key []byte) string {
	ix := m.names()
	for _, c := range ix.named[ix.skeleton(name)] {
		if !bytes.Equal(c.Key[:], key) {
			return c.Name
		}
	}
	return ""
}

// nameIndex is what the look-alike checks compare with, worked out when contacts, heard nodes or pins
// change rather than for every message drawn.
type nameIndex struct {
	rev    [4]int
	named  map[string][]radio.Contact // contacts, then heard nodes, by skeleton
	owner  map[[32]byte]string        // whose each key is, contacts first
	pinned map[string][]string        // pinned names by skeleton
	shared map[[4]byte]int            // how many known keys start with each 4 bytes

	skel  map[string]string // the skeletons and canonical forms of names asked about
	canon map[string]string
}

const nameMemo = 8192

func (ix *nameIndex) skeleton(name string) string {
	if sk, ok := ix.skel[name]; ok {
		return sk
	}
	if len(ix.skel) >= nameMemo {
		clear(ix.skel)
	}
	ix.skel[name] = untrusted.Skeleton(name)
	return ix.skel[name]
}

func (ix *nameIndex) canonical(name string) string {
	if n, ok := ix.canon[name]; ok {
		return n
	}
	if len(ix.canon) >= nameMemo {
		clear(ix.canon)
	}
	ix.canon[name] = untrusted.Name(name)
	return ix.canon[name]
}

// names is the look-alike index as things stand, rebuilt if they've changed.
func (m *Model) names() *nameIndex {
	ix := &m.chats.names
	pins := m.pins()
	rev := [4]int{-1, m.contacts.heardRev, len(m.contacts.heard), m.chats.pinsRev}
	if m.chat != nil {
		rev[0] = m.chat.Rev()
	}
	if ix.skel == nil {
		ix.skel, ix.canon = map[string]string{}, map[string]string{}
	} else if ix.named != nil && ix.rev == rev {
		return ix
	}
	ix.rev = rev
	ix.named, ix.owner, ix.pinned, ix.shared = map[string][]radio.Contact{}, map[[32]byte]string{}, map[string][]string{}, map[[4]byte]int{}
	var nodes []radio.Contact
	if m.chat != nil {
		nodes = m.chat.Contacts()
	}
	for _, h := range m.contacts.heard {
		nodes = append(nodes, h.c)
	}
	keys := map[[32]byte]bool{m.ready.Self.PubKey: true}
	for _, c := range nodes {
		sk := ix.skeleton(c.Name)
		ix.named[sk] = append(ix.named[sk], c)
		if _, ok := ix.owner[c.Key]; !ok {
			ix.owner[c.Key] = c.Name
		}
		keys[c.Key] = true
	}
	for n, p := range pins {
		sk := ix.skeleton(n)
		ix.pinned[sk] = append(ix.pinned[sk], n)
		keys[p.Key] = true
	}
	for k := range keys {
		ix.shared[[4]byte(k[:4])]++
	}
	return ix
}

// sigBadge describes a message's signature for the line above it, against who's pinned now.
func (m *Model) sigBadge(msg store.Message) string {
	text, col, bold := m.sigVerdict(msg)
	if text == "" {
		return ""
	}
	return m.th.Fg(col, bold).Render(text)
}

// sigVerdict is what a message's signature shows, and how loudly.
func (m *Model) sigVerdict(msg store.Message) (string, theme.RGB, bool) {
	th := m.th
	if msg.Out {
		if msg.Sig == store.SigValid {
			return "✍ signed", th.Green, false
		}
		return "", th.Text, false
	}
	p, pinned := m.pinOf(msg.Sender)
	if like, lp, ok := m.lookalike(msg.Sender); !pinned && ok && !(msg.Sig == store.SigValid && bytes.Equal(lp.Key[:], msg.SigKey)) {
		return "⚠ looks like " + like, th.Red, true
	}
	if len(msg.Author) == 4 && pinned && !bytes.HasPrefix(p.Key[:], msg.Author) {
		return "⚠ not " + msg.Sender + "'s key", th.Red, true
	}
	switch msg.Sig {
	case store.SigValid:
		switch {
		case !pinned:
			if owner := m.keyOwner(msg.SigKey); owner != "" && m.names().canonical(owner) != m.names().canonical(msg.Sender) {
				return "⚠ signed with " + owner + "'s key", th.Red, true
			}
			if other := m.namesake(msg.Sender, msg.SigKey); other != "" {
				return "⚠ another " + other + " has a different key", th.Red, true
			}
			return "signed · not pinned", th.Yellow, false
		case bytes.Equal(p.Key[:], msg.SigKey):
			return "✓ signed", th.Green, true
		}
		return "⚠ not " + msg.Sender + "'s key", th.Red, true
	case store.SigBad:
		return "⚠ bad signature", th.Red, true
	case store.SigUnknown:
		if pinned {
			return "⚠ not " + msg.Sender + "'s key", th.Red, true
		}
		return "signed by an unknown key", th.Yellow, false
	case store.SigStale:
		return "⚠ old signature", th.Red, true
	}
	if pinned && msg.Convo.IsChannel() && msg.RecvAt.After(p.At) && m.now.Sub(msg.RecvAt) > sigWait {
		return "⚠ unsigned · " + msg.Sender + " usually signs", th.Red, true
	}
	return "", th.Text, false
}

func sigKey(idx int, ts uint32) string { return fmt.Sprintf("%d|%d", idx, ts) }

// signatureSummary is the Packets screen's line for a signature: the message it signs and the verdict, as
// they stand now.
func (m *Model) signatureSummary(dec packets.Decoded) string {
	d := dec.Signature
	ch, found := m.packetChannel(dec)
	msg, ok := m.chats.signedMsgs[sigKey(ch.Idx, d.TS)]
	if !found || !ok {
		return fmt.Sprintf("%s · signature by key %s, for a message not heard", dec.Channel, radio.KeyHex(d.KeyID[:4]))
	}
	verdict, _, _ := m.sigVerdict(msg)
	return fmt.Sprintf("%s · signature for %s: %s · %s", dec.Channel, msg.Sender, msg.Text, verdict)
}

// roomAuthor describes who the room server says wrote a post: the 4 key bytes it names them by, which don't
// prove anything, and a warning when more than one known key starts with them.
func (m *Model) roomAuthor(msg store.Message) string {
	th := m.th
	out := th.Fg(th.Dim, false).Render(" · room post")
	if len(msg.Author) != 4 {
		return out
	}
	hex := radio.KeyHex(msg.Author)
	out += th.Fg(th.Dim, false).Render(" · key " + hex)
	if n := m.names().shared[[4]byte(msg.Author)]; n > 1 {
		out += th.Fg(th.Dim, false).Render(" · ") + th.Fg(th.Red, true).Render(fmt.Sprintf("⚠ %d keys start %s", n, hex))
	}
	return out
}

// keyOwner is the name of the contact or heard node with a key, if any.
func (m *Model) keyOwner(key []byte) string {
	if len(key) != 32 {
		return ""
	}
	return m.names().owner[[32]byte(key)]
}

// pinPicked pins the key a picked message was signed with to its sender's name. Replacing a pin is a
// separate key, since that's what an impersonator would want you to do.
func (m *Model) pinPicked(msg store.Message) {
	if msg.Sig != store.SigValid || len(msg.SigKey) != 32 || msg.Out {
		m.fail("PIN  only a message with a valid signature can pin its key")
		return
	}
	key := [32]byte(msg.SigKey)
	p, pinned := m.pinOf(msg.Sender)
	like, _, looks := m.lookalike(msg.Sender)
	switch {
	case pinned && bytes.Equal(p.Key[:], msg.SigKey):
		m.unpin(msg.Sender)
	case pinned:
		m.ask(msg.Sender+" is pinned to a different key: replace it, if you're sure this one is really theirs?", "replace", func() tea.Cmd {
			m.pin(msg.Sender, key)
			return nil
		})
	case m.keyOwner(msg.SigKey) != "" && m.names().canonical(m.keyOwner(msg.SigKey)) != m.names().canonical(msg.Sender):
		owner := m.keyOwner(msg.SigKey)
		m.ask("that key is "+owner+"'s: pin it to "+msg.Sender+" anyway, letting them post as "+msg.Sender+"?", "pin", func() tea.Cmd {
			m.pin(msg.Sender, key)
			return nil
		})
	case looks:
		m.ask(msg.Sender+" looks like "+like+", who is pinned: pin this key to "+msg.Sender+" as well?", "pin", func() tea.Cmd {
			m.pin(msg.Sender, key)
			return nil
		})
	case m.namesake(msg.Sender, msg.SigKey) != "":
		m.ask("another "+m.namesake(msg.Sender, msg.SigKey)+" has a different key: pin this one, if you're sure it's theirs?", "pin", func() tea.Cmd {
			m.pin(msg.Sender, key)
			return nil
		})
	default:
		m.pin(msg.Sender, key)
	}
}
