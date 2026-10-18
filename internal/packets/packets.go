// Package packets decodes raw on-air MeshCore packets into named fields, each tied to the bytes it came
// from, and decrypts channel traffic where the channel's secret is known.
package packets

import (
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/signing"
)

// Field is one decoded value and the bytes [Start, End) it occupies in the raw packet.
// Hidden fields name who other people's private traffic is for or from; their bytes aren't shown either.
type Field struct {
	Name, Value string
	Start, End  int
	Part        Part
	Hidden      bool
}

// Part groups fields so the hex dump can colour them.
type Part int

const (
	PartHeader Part = iota
	PartTransport
	PartPath
	PartAddress // hashes and keys that say who it's for or from
	PartMeta    // timestamps, tags, MACs, flags
	PartBody    // the content, encrypted or not
)

type Decoded struct {
	Raw  []byte
	Err  error // set when the bytes don't form a valid packet; Fields then holds what could be read
	Hash [meshcore.PacketHashSize]byte

	Route, Type  string
	Flood        bool
	HashSize     int
	Path         []byte
	HasTransport bool
	Fields       []Field
	Summary      string

	// Channel messages, when we hold the channel's secret.
	Channel   string
	Decrypted bool
	Sender    string
	Text      string

	// Channel datagrams, when we hold the channel's secret; a signed message's signature is one.
	DataType  uint16
	Data      []byte
	Signature *signing.Datagram

	// Adverts.
	Name     string
	SigOK    bool
	Lat, Lon float64
}

// Book is what we know that helps decoding: channel secrets and contact names.
type Book struct {
	Self     [32]byte
	Channels []radio.Channel
	Contacts []radio.Contact
}

// Name resolves a 1-byte path hash to a contact name, marking guesses when several share it.
func (b Book) Name(h byte) string {
	if h == b.Self[0] && b.Self != [32]byte{} {
		return "me"
	}
	var names []string
	for _, c := range b.Contacts {
		if c.Key[0] == h {
			names = append(names, c.Name)
		}
	}
	switch len(names) {
	case 0:
		return fmt.Sprintf("%02X", h)
	case 1:
		return names[0]
	}
	return fmt.Sprintf("%02X (%s?)", h, strings.Join(names, " or "))
}

func hexs(b []byte) string {
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = fmt.Sprintf("%02X", v)
	}
	return strings.Join(parts, " ")
}

func Decode(raw []byte, book Book) Decoded {
	d := Decoded{Raw: raw}
	p, err := meshcore.PacketFromBytes(raw)
	if err != nil {
		d.Err = err
		d.Summary = "malformed: " + err.Error()
		if len(raw) > 0 {
			d.Fields = []Field{{Name: "bytes", Value: fmt.Sprintf("%d", len(raw)), Start: 0, End: len(raw), Part: PartBody}}
		}
		return d
	}
	d.Hash = p.PacketHash()
	d.Route, d.Type, d.Flood = p.RouteTypeString(), p.PayloadTypeString(), p.IsRouteFlood()
	if d.Type == "" {
		d.Type = fmt.Sprintf("TYPE_%d", p.PayloadType())
	}
	d.HasTransport = p.IsTransport()
	size, count := meshcore.PathLenFields(p.PathLength)
	d.HashSize, d.Path = int(size), p.Path

	add := func(name, value string, start, end int, part Part) {
		d.Fields = append(d.Fields, Field{Name: name, Value: value, Start: start, End: end, Part: part})
	}
	add("header", fmt.Sprintf("0x%02X  route %s · %s · version %d", p.Header, d.Route, d.Type, p.PayloadVer()+1), 0, 1, PartHeader)
	i := 1
	if d.HasTransport {
		add("transport codes", fmt.Sprintf("%04X %04X  (region scope)", p.TransportCode1, p.TransportCode2), 1, 5, PartTransport)
		i = 5
	}
	hopsText := fmt.Sprintf("%d hops × %d byte", count, size)
	if size > 1 {
		hopsText += "s"
	}
	add("path length", fmt.Sprintf("0x%02X  %s", p.PathLength, hopsText), i, i+1, PartPath)
	i++
	if len(p.Path) > 0 {
		var names []string
		for _, h := range p.PathHashes() {
			if len(h) == 1 {
				names = append(names, book.Name(h[0]))
			} else {
				names = append(names, hexs(h))
			}
		}
		add("path", strings.Join(names, " → "), i, i+len(p.Path), PartPath)
		i += len(p.Path)
	}
	d.decodePayload(p, i, book)
	return d
}

func (d *Decoded) field(name, value string, start, end int, part Part) {
	d.Fields = append(d.Fields, Field{Name: name, Value: value, Start: start, End: end, Part: part})
}

func (d *Decoded) decodePayload(p *meshcore.Packet, at int, book Book) {
	pl := p.Payload
	end := at + len(pl)
	body := func(name string, from int) {
		if at+from < end {
			d.field(name, fmt.Sprintf("%d bytes", end-at-from), at+from, end, PartBody)
		}
	}
	// Private traffic between other people only shows what a repeater needs; see SPEC §10.
	addressed := func(what string) {
		if len(pl) < 4 {
			d.Summary = what + " (truncated)"
			return
		}
		to := d.address("to", pl[0], at, book, "addressed to your hash")
		from := d.address("from", pl[1], at+1, book, "sent from your hash")
		d.field("MAC", hexs(pl[2:4]), at+2, at+4, PartMeta)
		body("ciphertext", 4)
		d.Summary = fmt.Sprintf("encrypted %s · %d B%s%s", what, len(pl)-4, to, from)
	}
	switch p.PayloadType() {
	case meshcore.PayloadTypeAdvert:
		a, err := meshcore.AdvertFromBytes(pl)
		if err != nil {
			d.Summary = "advert, malformed: " + err.Error()
			body("payload", 0)
			return
		}
		key := a.PublicKey.PublicKey()
		app := a.AppData()
		d.SigOK, d.Name = a.Verify(), app.Name
		d.field("public key", radio.KeyHex(key[:]), at, at+32, PartAddress)
		d.field("timestamp", fmt.Sprintf("%d", a.Timestamp), at+32, at+36, PartMeta)
		sig := "valid"
		if !d.SigOK {
			sig = "INVALID"
		}
		d.field("signature", sig, at+36, at+100, PartMeta)
		where := ""
		if app.Lat != 0 || app.Lon != 0 {
			d.Lat, d.Lon = float64(app.Lat)/1e6, float64(app.Lon)/1e6
			where = fmt.Sprintf(" · %.4f, %.4f", d.Lat, d.Lon)
		}
		kind := strings.ToLower(app.Type)
		if kind == "" || kind == "none" {
			kind = "unknown type"
		}
		name := app.Name
		if name == "" {
			name = "unnamed"
		}
		d.field("app data", fmt.Sprintf("%s · %s%s", name, kind, where), at+100, end, PartBody)
		d.Summary = fmt.Sprintf("%s · %s%s", name, kind, where)
		if !d.SigOK {
			d.Summary += " · BAD SIGNATURE"
		}
	case meshcore.PayloadTypeGrpTxt, meshcore.PayloadTypeGrpData:
		g, err := meshcore.GroupTextFromBytes(pl)
		if err != nil {
			d.Summary = "channel message, malformed"
			return
		}
		d.field("channel hash", fmt.Sprintf("%02X", g.ChannelHash), at, at+1, PartAddress)
		d.field("MAC", hexs(g.MAC[:]), at+1, at+3, PartMeta)
		body("ciphertext", 3)
		d.Summary = fmt.Sprintf("channel %02X · %d B, no key", g.ChannelHash, len(g.EncryptedPayload))
		for _, ch := range book.Channels {
			if meshcore.DeriveChannelHash(ch.Secret) != g.ChannelHash {
				continue
			}
			d.Channel = ch.Name
			d.Summary = fmt.Sprintf("%s · %d B, can't decrypt", ch.Name, len(g.EncryptedPayload))
			if p.PayloadType() != meshcore.PayloadTypeGrpTxt {
				d.Summary = fmt.Sprintf("%s · %d B of data", ch.Name, len(g.EncryptedPayload))
				gd, err := meshcore.GroupDataFromBytes(pl)
				if err != nil {
					break
				}
				data, err := gd.DecryptStruct(ch.Secret[:])
				if err != nil {
					continue // another channel can share a 1-byte hash
				}
				d.decodeData(ch.Name, data.DataType, data.Data)
				break
			}
			msg, err := g.DecryptStruct(ch.Secret[:])
			if err != nil {
				continue // another channel can share a 1-byte hash
			}
			d.Decrypted, d.Sender, d.Text = true, msg.Sender, msg.Text
			last := &d.Fields[len(d.Fields)-1] // the ciphertext: show what it says
			last.Name, last.Value = "ciphertext, decrypted", fmt.Sprintf("%s: %s", msg.Sender, msg.Text)
			d.Summary = fmt.Sprintf("%s · %s: %s", ch.Name, msg.Sender, msg.Text)
			break
		}
	case meshcore.PayloadTypeTxtMsg:
		addressed("direct message")
	case meshcore.PayloadTypePath:
		addressed("route reply")
	case meshcore.PayloadTypeReq:
		addressed("request")
	case meshcore.PayloadTypeResponse:
		addressed("response")
	case meshcore.PayloadTypeAck:
		if len(pl) >= 4 {
			crc := binary.LittleEndian.Uint32(pl[:4])
			d.field("ack code", fmt.Sprintf("%08X", crc), at, at+4, PartMeta)
			d.Summary = fmt.Sprintf("ack %08X", crc)
		}
	case meshcore.PayloadTypeTrace:
		t, err := meshcore.TraceFromBytes(pl)
		if err != nil {
			d.Summary = "trace, malformed"
			return
		}
		d.field("tag", fmt.Sprintf("%08X", t.Tag), at, at+4, PartMeta)
		d.field("auth", fmt.Sprintf("%08X", t.AuthCode), at+4, at+8, PartMeta)
		d.field("flags", fmt.Sprintf("0x%02X", t.Flags), at+8, at+9, PartMeta)
		var via []string
		for _, h := range t.PathHashes {
			via = append(via, book.Name(h))
		}
		d.field("route", strings.Join(via, " → "), at+9, end, PartAddress)
		d.Summary = "trace via " + strings.Join(via, " → ")
	case meshcore.PayloadTypeAnonReq:
		if len(pl) < 35 {
			d.Summary = "anonymous request, malformed"
			return
		}
		to := d.address("to", pl[0], at, book, "addressed to your hash")
		var key [32]byte
		copy(key[:], pl[1:33])
		from := ""
		if key == book.Self && book.Self != ([32]byte{}) {
			d.field("sender's key", "yours", at+1, at+33, PartAddress)
			from = " · sent by you"
		} else {
			d.Fields = append(d.Fields, Field{Name: "sender's key", Value: "not shown", Start: at + 1, End: at + 33, Part: PartAddress, Hidden: true})
		}
		d.field("MAC", hexs(pl[33:35]), at+33, at+35, PartMeta)
		body("ciphertext", 35)
		d.Summary = fmt.Sprintf("encrypted anonymous request · %d B%s%s", len(pl)-35, to, from)
	case meshcore.PayloadTypeControl:
		c, err := meshcore.ControlFromBytes(pl)
		if err != nil {
			d.Summary = "control, malformed"
			return
		}
		d.field("flags", fmt.Sprintf("0x%02X", c.Flags), at, at+1, PartMeta)
		body("data", 1)
		switch c.SubType() {
		case meshcore.ControlSubTypeDiscoverReq:
			d.Summary = "neighbour discovery request"
		case meshcore.ControlSubTypeDiscoverResp:
			d.Summary = "neighbour discovery reply"
		default:
			d.Summary = fmt.Sprintf("control subtype %d", c.SubType())
		}
	default:
		body("payload", 0)
		d.Summary = fmt.Sprintf("%s · %d B", strings.ToLower(d.Type), len(pl))
	}
}

// decodeData describes a channel datagram we could decrypt. What's in it is inside the ciphertext, so it's
// described in that field's value.
func (d *Decoded) decodeData(channel string, dataType uint16, data []byte) {
	d.Decrypted, d.DataType, d.Data = true, dataType, data
	ct := &d.Fields[len(d.Fields)-1]
	ct.Name, ct.Value = "ciphertext, decrypted", fmt.Sprintf("data type 0x%04X · %d bytes", dataType, len(data))
	d.Summary = fmt.Sprintf("%s · data type 0x%04X · %d B", channel, dataType, len(data))
	sig, ok := signing.Parse(data)
	if dataType != signing.DataType || !ok {
		return
	}
	d.Signature = &sig
	ct.Value = fmt.Sprintf("a signature by key %s for the message sent %s: %s", hexs(sig.KeyID[:]),
		time.Unix(int64(sig.TS), 0).UTC().Format("2006-01-02 15:04:05 UTC"), hexs(sig.Sig[:8])+" …")
	d.Summary = fmt.Sprintf("%s · signature by key %s", channel, hexs(sig.KeyID[:4]))
}

// address adds a to/from hash. Only one matching our own key is shown; it's a one-byte hash,
// so a match means the packet may be ours, not that it is.
func (d *Decoded) address(name string, h byte, at int, book Book, mine string) string {
	if book.Self != ([32]byte{}) && h == book.Self[0] {
		d.field(name, fmt.Sprintf("%02X, your hash", h), at, at+1, PartAddress)
		return " · " + mine
	}
	d.Fields = append(d.Fields, Field{Name: name, Value: "not shown", Start: at, End: at + 1, Part: PartAddress, Hidden: true})
	return ""
}

// Hidden reports whether byte i belongs to a hidden field.
func (d Decoded) Hidden(i int) bool {
	for _, f := range d.Fields {
		if f.Hidden && i >= f.Start && i < f.End {
			return true
		}
	}
	return false
}

// MaskedHex is the packet as hex, with hidden bytes written as "··".
func (d Decoded) MaskedHex() string {
	var b strings.Builder
	for i, c := range d.Raw {
		if d.Hidden(i) {
			b.WriteString("··")
		} else {
			fmt.Fprintf(&b, "%02x", c)
		}
	}
	return b.String()
}
