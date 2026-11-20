package signing

// Version 2 (meshcore-companion's SIGNING.md, "Version 2"): signatures made by a separate signing key that is
// never on the radio and only exists decrypted for one signature, after a passphrase (or passkey) unlock. The
// radio vouches for the key once with a binding datagram; each signature names the radio its key is bound to.
//
//	MC2 (79 bytes)  "MC2" + keyid(8) + ts(4 LE) + sig(64)          a signature, keyid = the signing key's first 8 bytes
//	MCB (103 bytes) "MCB" + signing key(32) + ts(4 LE) + sig(64)   a binding, signed by the RADIO key
//	MCR (103 bytes) "MCR" + signing key(32) + ts(4 LE) + sig(64)   a revocation, signed by the signing key itself

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
)

const (
	domainV2     = "MeshCore-Companion signed channel v2"
	domainBind   = "MeshCore-Companion key binding v2"
	domainRevoke = "MeshCore-Companion key revocation v2"
	magicV2      = "MC2"
	magicBind    = "MCB"
	magicRevoke  = "MCR"
	// KeySize is the size of a binding or revocation datagram.
	KeySize = 3 + 32 + 4 + 64
)

// CanonicalV2 is what a v2 signature covers. It names the radio the signing key is bound to, so a binding of
// someone else's signing key to another radio is worth nothing.
func CanonicalV2(radio [32]byte, channel string, ts uint32, text string) []byte {
	var b bytes.Buffer
	b.WriteString(domainV2)
	b.Write(radio[:8])
	b.WriteByte(ChannelHash(channel))
	binary.Write(&b, binary.LittleEndian, ts)
	b.WriteString(text)
	return b.Bytes()
}

// DatagramV2 is a v2 signature. It has the same shape as a v1 Datagram, with its own magic.
type DatagramV2 Datagram

func (d DatagramV2) Bytes() []byte {
	b := Datagram(d).Bytes()
	copy(b, magicV2)
	return b
}

// ParseV2 reads a v2 signature, reporting false for anything that isn't one.
func ParseV2(b []byte) (DatagramV2, bool) {
	if len(b) != Size || string(b[:3]) != magicV2 {
		return DatagramV2{}, false
	}
	c := bytes.Clone(b)
	copy(c, magic)
	d, ok := Parse(c)
	return DatagramV2(d), ok
}

// Matches reports whether a signing key is the one the signature names.
func (d DatagramV2) Matches(key [32]byte) bool { return bytes.Equal(key[:8], d.KeyID[:]) }

// Verify checks a v2 signature with the signing key it names and the radio that key is bound to.
func (d DatagramV2) Verify(key, radio [32]byte, channel, text string) bool {
	return d.Matches(key) && ed25519.Verify(key[:], CanonicalV2(radio, channel, d.TS, text), d.Sig[:])
}

// Binding is a radio vouching for a signing key. The radio isn't named in it: the signature verifies only
// against the radio that made it, so a listener tries the radio keys it knows.
type Binding struct {
	Key [32]byte // the signing key
	TS  uint32
	Sig [64]byte // by the radio's key
}

// BindInput is what the radio signs to vouch for a signing key.
func BindInput(radio, key [32]byte, ts uint32) []byte {
	b := append([]byte(domainBind), radio[:]...)
	b = append(b, key[:]...)
	return binary.LittleEndian.AppendUint32(b, ts)
}

func (k Binding) Bytes() []byte { return keyBytes(magicBind, k.Key, k.TS, k.Sig) }

// ParseBinding reads a binding, reporting false for anything that isn't one.
func ParseBinding(b []byte) (Binding, bool) {
	key, ts, sig, ok := parseKey(magicBind, b)
	return Binding{key, ts, sig}, ok
}

// Verify reports whether this radio made the binding.
func (k Binding) Verify(radio [32]byte) bool {
	return ed25519.Verify(radio[:], BindInput(radio, k.Key, k.TS), k.Sig[:])
}

// Revocation says a signing key must no longer be trusted. Only the key's holder can make one.
type Revocation struct {
	Key [32]byte
	TS  uint32
	Sig [64]byte // by the revoked key itself
}

// RevokeInput is what a signing key signs to revoke itself.
func RevokeInput(key [32]byte, ts uint32) []byte {
	return binary.LittleEndian.AppendUint32(append([]byte(domainRevoke), key[:]...), ts)
}

func (r Revocation) Bytes() []byte { return keyBytes(magicRevoke, r.Key, r.TS, r.Sig) }

// ParseRevocation reads a revocation, reporting false for anything that isn't one or isn't signed by its key.
func ParseRevocation(b []byte) (Revocation, bool) {
	key, ts, sig, ok := parseKey(magicRevoke, b)
	if !ok || !ed25519.Verify(key[:], RevokeInput(key, ts), sig[:]) {
		return Revocation{}, false
	}
	return Revocation{key, ts, sig}, true
}

func keyBytes(magic string, key [32]byte, ts uint32, sig [64]byte) []byte {
	b := make([]byte, 0, KeySize)
	b = append(b, magic...)
	b = append(b, key[:]...)
	b = binary.LittleEndian.AppendUint32(b, ts)
	return append(b, sig[:]...)
}

func parseKey(magic string, b []byte) (key [32]byte, ts uint32, sig [64]byte, ok bool) {
	if len(b) != KeySize || string(b[:3]) != magic {
		return key, 0, sig, false
	}
	copy(key[:], b[3:35])
	ts = binary.LittleEndian.Uint32(b[35:39])
	copy(sig[:], b[39:])
	return key, ts, sig, true
}
