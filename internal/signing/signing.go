// Package signing is signed channel messages, as specified by meshcore-companion's SIGNING.md: a channel
// message is sent as usual, then a 79-byte datagram on the same channel carries an Ed25519 signature over
// it, made by the sending radio's own key. A signature proves which key sent a message, not who owns the
// key; pinning a name to a key is what catches someone posting under another's name.
package signing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
)

const (
	// DataType is the datagram's type. 0xFFFF is MeshCore's experimental range; the spec expects an
	// allocated value to replace it.
	DataType = 0xFFFF
	Size     = 79
	domain   = "MeshCore-Companion signed channel v1"
	magic    = "MC1"
)

// Datagram is the signature sent after a signed channel message.
type Datagram struct {
	KeyID [8]byte // the first 8 bytes of the signer's public key
	TS    uint32  // the message's timestamp, which pairs the datagram with it
	Sig   [64]byte
}

// ChannelHash binds a signature to its channel: the first byte of SHA-256 of the channel's name.
func ChannelHash(channel string) byte {
	h := sha256.Sum256([]byte(channel))
	return h[0]
}

// Canonical is what's signed: a fixed tag, the channel, the timestamp and the message body as typed,
// without the "Name: " the radio puts in front of it on air.
func Canonical(channel string, ts uint32, text string) []byte {
	var b bytes.Buffer
	b.WriteString(domain)
	b.WriteByte(ChannelHash(channel))
	binary.Write(&b, binary.LittleEndian, ts)
	b.WriteString(text)
	return b.Bytes()
}

func (d Datagram) Bytes() []byte {
	b := make([]byte, 0, Size)
	b = append(b, magic...)
	b = append(b, d.KeyID[:]...)
	b = binary.LittleEndian.AppendUint32(b, d.TS)
	return append(b, d.Sig[:]...)
}

// Parse reads a datagram, reporting false for anything that isn't one.
func Parse(b []byte) (Datagram, bool) {
	var d Datagram
	if len(b) != Size || string(b[:3]) != magic {
		return d, false
	}
	copy(d.KeyID[:], b[3:11])
	d.TS = binary.LittleEndian.Uint32(b[11:15])
	copy(d.Sig[:], b[15:])
	return d, true
}

// Matches reports whether a public key is the one a datagram names.
func (d Datagram) Matches(key [32]byte) bool { return bytes.Equal(key[:8], d.KeyID[:]) }

// Verify checks a datagram's signature over a message, with the full public key it names.
func (d Datagram) Verify(key [32]byte, channel, text string) bool {
	return d.Matches(key) && ed25519.Verify(key[:], Canonical(channel, d.TS, text), d.Sig[:])
}
