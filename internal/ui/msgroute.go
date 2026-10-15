package ui

import (
	"fmt"
	"time"

	"meshcore-tui/internal/packets"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/store"
)

// A received message tells us only how many hops it took; the packet that carried it names them. The two
// arrive separately, usually the packet first, so whichever comes second is matched to the first and the
// path is saved with the message. The first copy heard is the way the message came: later copies are
// repeats the radio drops.

const (
	heardKeep     = 100
	channelWindow = 5 * time.Minute // a channel message and its packet match by channel, sender and text
	directWindow  = time.Minute     // a direct message's packet is encrypted, so only its sender's key byte and timing match
)

// heardMessage is a message's packet as heard, kept a little while for the message to arrive.
type heardMessage struct {
	convo        store.Convo // a channel's; for a direct message, empty and from is set
	sender, text string
	from         int // a direct message's sender's first key byte, or -1
	path         []byte
	hashSize     int
	snr          float32
	at           time.Time
}

// messagePacket notes the packet of a channel message we can read, or of a direct message to us, and gives its
// path to the message if that has already arrived. Nothing here is shown on Packets or the map.
func (m *Model) messagePacket(dec packets.Decoded, raw []byte, snr float32) {
	if m.chat == nil || dec.Err != nil || !dec.Flood || dec.HashSize == 0 {
		return
	}
	h := heardMessage{from: -1, path: dec.Path, hashSize: dec.HashSize, snr: snr, at: m.now}
	switch {
	case dec.Type == "GRP_TXT" && dec.Decrypted:
		for _, ch := range m.chat.Channels() {
			if ch.Name == dec.Channel {
				h.convo = store.ChannelConvo(ch.Idx)
			}
		}
		if h.convo == "" || dec.Sender == m.ready.Self.Name {
			return
		}
		h.sender, h.text = dec.Sender, dec.Text
	case dec.Type == "TXT_MSG":
		to, from := -1, -1
		for _, f := range dec.Fields {
			switch f.Name {
			case "to":
				to = int(raw[f.Start])
			case "from":
				from = int(raw[f.Start])
			}
		}
		if to != int(m.ready.Self.PubKey[0]) || from < 0 {
			return
		}
		h.from = from
	default:
		return
	}
	for _, c := range m.chats.heard {
		if c.convo == h.convo && c.from == h.from && c.sender == h.sender && c.text == h.text && m.now.Sub(c.at) < directWindow {
			return // a repeat of one already heard: the first copy is the way it came
		}
	}
	m.chats.heard = append(m.chats.heard, h)
	if len(m.chats.heard) > heardKeep {
		m.chats.heard = m.chats.heard[len(m.chats.heard)-heardKeep:]
	}
	attached := false
	for _, c := range m.convosFor(h) {
		window := channelWindow
		if h.from >= 0 {
			window = directWindow
		}
		id, ok, err := m.chat.AttachPath(c, h.sender, h.text, m.now.Add(-window), h.path, h.hashSize)
		if err != nil {
			m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
		}
		if ok {
			attached = true
			m.chats.routed[id] = m.now
			break
		}
	}
	if attached {
		m.refreshChats()
	}
}

// convosFor is where a heard message's packet could belong: its channel, or the conversation with each
// contact whose key starts with the sender's byte.
func (m *Model) convosFor(h heardMessage) []store.Convo {
	if h.from < 0 {
		return []store.Convo{h.convo}
	}
	var out []store.Convo
	for _, c := range m.chat.Contacts() {
		if int(c.Key[0]) == h.from {
			out = append(out, store.ContactConvo(c.Key))
		}
	}
	return out
}

// routeArrived gives a message that's just arrived the path of its packet, if that was heard first.
func (m *Model) routeArrived(msg store.Message) {
	if msg.Hops < 0 {
		return // it came by a direct route, which the packet doesn't record
	}
	ct, isContact := m.chat.Contact(msg.Convo)
	for i := len(m.chats.heard) - 1; i >= 0; i-- {
		h := m.chats.heard[i]
		match := h.convo == msg.Convo && h.sender == msg.Sender && h.text == msg.Text && msg.RecvAt.Sub(h.at) < channelWindow
		if h.from >= 0 {
			match = isContact && int(ct.Key[0]) == h.from && msg.RecvAt.Sub(h.at) < directWindow
		}
		if match {
			if _, _, err := m.chat.AttachPath(msg.Convo, msg.Sender, msg.Text, msg.RecvAt.Add(-time.Second), h.path, h.hashSize); err != nil {
				m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
			}
			return
		}
	}
}

// pathText is a message's path as compact hex, such as 5F·77, for the line above it.
func pathText(path []byte, size int) string {
	out := ""
	for i := 0; size > 0 && i+size <= len(path); i += size {
		if i > 0 {
			out += "·"
		}
		out += fmt.Sprintf("%X", path[i:i+size])
	}
	return out
}
