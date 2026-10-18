package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/packets"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/signing"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

// Signed channel messages (SIGNING.md, and SPEC §7): a signed message is sent as usual, then a datagram on
// the same channel carries the radio's signature over it. They arrive separately and pair up by channel and
// timestamp. Checking signatures costs nothing, so it always runs; signing your own is chosen per channel,
// and per message with ctrl+s, since each signed message is two packets.

const (
	sigHold    = 15 * time.Minute // a signature that arrives before its message waits this long for it
	sigWait    = 30 * time.Second // after this, a message from someone who signs, without a signature, is flagged
	signingMin = "v1.15.0"        // the firmware that sends and receives channel datagrams
)

type pendingSig struct {
	idx int
	d   signing.Datagram
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

func (m *Model) signedChannel(name string) bool {
	return m.opt.Config != nil && slices.Contains(m.opt.Config.Messages.SignChannels, name)
}

// signThis reports whether the message being written will be signed: the channel's setting, flipped by ctrl+s.
func (m *Model) signThis() bool {
	cur, ok := m.selectedConvo()
	return ok && cur.Kind == chat.KindChannel && m.signedChannel(cur.Title) != m.chats.signFlip
}

// toggleChannelSigning turns signing your messages on an open channel on or off.
func (m *Model) toggleChannelSigning() {
	cur, ok := m.selectedConvo()
	if !ok || cur.Kind != chat.KindChannel {
		m.fail("SIGNING  only channel messages are signed; direct messages are already encrypted to one person")
		return
	}
	if err := m.canSign(); err != nil && !m.signedChannel(cur.Title) {
		m.fail("SIGNING  " + err.Error())
		return
	}
	cfg := m.opt.Config
	if cfg == nil {
		m.fail("SIGNING  there's no config file to keep the setting in")
		return
	}
	on := !m.signedChannel(cur.Title)
	cfg.Messages.SignChannels = slices.DeleteFunc(cfg.Messages.SignChannels, func(n string) bool { return n == cur.Title })
	if on {
		cfg.Messages.SignChannels = append(cfg.Messages.SignChannels, cur.Title)
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
	m.refreshChats()
	return cmd
}

// pins are the keys names are trusted to sign with, read from the store the first time they're wanted.
func (m *Model) pins() map[string]store.Pin {
	if m.chats.pins == nil && m.chat != nil {
		p, err := m.chat.Pins()
		if err != nil {
			m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
		}
		m.chats.pins = p
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

// check verifies a signature over a message with every known key it could be.
func (m *Model) check(d signing.Datagram, channel, text string) (store.Sig, []byte) {
	tried := false
	for _, k := range m.knownKeys() {
		if !d.Matches(k) {
			continue
		}
		tried = true
		if d.Verify(k, channel, text) {
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

// onChannelData pairs a signature with the message it signs, or holds it until the message arrives.
func (m *Model) onChannelData(e radio.ChannelData) {
	if e.Type != signing.DataType || m.chat == nil {
		return
	}
	d, ok := signing.Parse(e.Data)
	if !ok {
		return
	}
	if m.applySig(e.Idx, d) {
		return
	}
	live := m.chats.sigs[:0]
	for _, p := range m.chats.sigs {
		if m.now.Sub(p.at) < sigHold {
			live = append(live, p)
		}
	}
	m.chats.sigs = append(live, pendingSig{e.Idx, d, m.now})
}

// applySig checks a signature against the channel's messages sent at its timestamp, recording the outcome.
func (m *Model) applySig(idx int, d signing.Datagram) bool {
	name, ok := m.channelName(idx)
	if !ok {
		return false
	}
	msgs, err := m.chat.AwaitingSig(idx, time.Unix(int64(d.TS), 0))
	if err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
	}
	if len(msgs) == 0 {
		return false
	}
	// Two messages can share a second; the signature's text decides which it's for.
	best, sig, key := msgs[0], store.SigBad, d.KeyID[:]
	for _, msg := range msgs {
		if s, k := m.check(d, name, msg.Text); s == store.SigValid || s == store.SigUnknown && sig == store.SigBad {
			best, sig, key = msg, s, k
			if s == store.SigValid {
				break
			}
		}
	}
	if err := m.chat.SetSig(best.ID, sig, key); err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
	}
	best.Sig, best.SigKey = sig, key
	m.chats.signedMsgs[sigKey(name, d.TS)] = best
	switch {
	case sig == store.SigBad:
		m.addLog(m.now, fmt.Sprintf("SIGNATURE  %s's message in %s has a signature that doesn't match it", best.Sender, name), radio.ToneFail)
	case sig == store.SigValid && m.pinnedOther(best.Sender, key):
		m.addLog(m.now, fmt.Sprintf("SIGNATURE  a message as %s in %s was signed by a different key from theirs", best.Sender, name), radio.ToneFail)
	}
	m.refreshChats()
	return true
}

// sigArrived looks for a signature that came before its channel message.
func (m *Model) sigArrived(msg store.Message) {
	var idx int
	if _, err := fmt.Sscanf(string(msg.Convo), "ch:%d", &idx); err != nil {
		return
	}
	for i, p := range m.chats.sigs {
		if p.idx == idx && int64(p.d.TS) == msg.SentAt.Unix() && m.applySig(idx, p.d) {
			m.chats.sigs = append(m.chats.sigs[:i], m.chats.sigs[i+1:]...)
			return
		}
	}
}

func (m *Model) pinnedOther(name string, key []byte) bool {
	p, ok := m.pins()[name]
	return ok && !bytes.Equal(p.Key[:], key)
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
	p, pinned := m.pins()[msg.Sender]
	switch msg.Sig {
	case store.SigValid:
		switch {
		case !pinned:
			if owner := m.keyOwner(msg.SigKey); owner != "" && owner != msg.Sender {
				return "⚠ signed with " + owner + "'s key", th.Red, true
			}
			return "signed · not pinned", th.Yellow, false
		case bytes.Equal(p.Key[:], msg.SigKey):
			return "✓ signed", th.Green, true
		}
		return "⚠ not " + msg.Sender + "'s key", th.Red, true
	case store.SigBad:
		return "⚠ bad signature", th.Red, true
	case store.SigUnknown:
		return "signed by an unknown key", th.Yellow, false
	}
	if pinned && msg.Convo.IsChannel() && msg.RecvAt.After(p.At) && m.now.Sub(msg.RecvAt) > sigWait {
		return "⚠ unsigned · " + msg.Sender + " usually signs", th.Red, true
	}
	return "", th.Text, false
}

func sigKey(channel string, ts uint32) string { return fmt.Sprintf("%s|%d", channel, ts) }

// signatureSummary is the Packets screen's line for a signature: the message it signs and the verdict, as
// they stand now.
func (m *Model) signatureSummary(dec packets.Decoded) string {
	d := dec.Signature
	msg, ok := m.chats.signedMsgs[sigKey(dec.Channel, d.TS)]
	if !ok {
		return fmt.Sprintf("%s · signature by key %s, for a message not heard", dec.Channel, radio.KeyHex(d.KeyID[:4]))
	}
	verdict, _, _ := m.sigVerdict(msg)
	return fmt.Sprintf("%s · signature for %s: %s · %s", dec.Channel, msg.Sender, msg.Text, verdict)
}

// keyOwner is the name of the contact or heard node with a key, if any.
func (m *Model) keyOwner(key []byte) string {
	if m.chat != nil {
		for _, c := range m.chat.Contacts() {
			if bytes.Equal(c.Key[:], key) {
				return c.Name
			}
		}
	}
	for _, h := range m.contacts.heard {
		if bytes.Equal(h.c.Key[:], key) {
			return h.c.Name
		}
	}
	return ""
}

// pinPicked pins the key a picked message was signed with to its sender's name. Replacing a pin is a
// separate key, since that's what an impersonator would want you to do.
func (m *Model) pinPicked(msg store.Message, replace bool) {
	if msg.Sig != store.SigValid || len(msg.SigKey) != 32 || msg.Out {
		m.fail("PIN  only a message with a valid signature can pin its key")
		return
	}
	p, pinned := m.pins()[msg.Sender]
	switch {
	case pinned && bytes.Equal(p.Key[:], msg.SigKey):
		m.addLog(m.now, "PIN  "+msg.Sender+" is already pinned to this key", radio.ToneInfo)
	case pinned && !replace:
		m.fail("PIN  " + msg.Sender + " is pinned to a different key; P replaces it, if you're sure this one is really theirs")
	case !replace && m.keyOwner(msg.SigKey) != "" && m.keyOwner(msg.SigKey) != msg.Sender:
		m.fail("PIN  that key is " + m.keyOwner(msg.SigKey) + "'s; pinning it to " + msg.Sender + " would let them post as " + msg.Sender + ". P pins it anyway")
	default:
		m.pin(msg.Sender, [32]byte(msg.SigKey))
	}
}
