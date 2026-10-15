package signing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestSignAndVerify(t *testing.T) {
	seed := sha256.Sum256([]byte("alice"))
	priv := ed25519.NewKeyFromSeed(seed[:])
	var pub [32]byte
	copy(pub[:], priv.Public().(ed25519.PublicKey))

	ts := uint32(1760000000)
	d := Datagram{TS: ts}
	copy(d.KeyID[:], pub[:8])
	copy(d.Sig[:], ed25519.Sign(priv, Canonical("#bristol", ts, "meet at the hill")))

	b := d.Bytes()
	if len(b) != Size || string(b[:3]) != "MC1" {
		t.Fatalf("datagram = %x", b)
	}
	back, ok := Parse(b)
	if !ok || back != d {
		t.Fatal("round trip")
	}
	if !back.Verify(pub, "#bristol", "meet at the hill") {
		t.Fatal("verifies")
	}
	for name, bad := range map[string]func() bool{
		"other text":    func() bool { return back.Verify(pub, "#bristol", "meet at the hill!") },
		"other channel": func() bool { return back.Verify(pub, "Public", "meet at the hill") },
		"other key":     func() bool { other := pub; other[0] ^= 1; return back.Verify(other, "#bristol", "meet at the hill") },
		"other time":    func() bool { e := back; e.TS++; return e.Verify(pub, "#bristol", "meet at the hill") },
	} {
		if bad() {
			t.Errorf("%s still verifies", name)
		}
	}
	for _, junk := range [][]byte{nil, b[:78], append([]byte("MC2"), b[3:]...)} {
		if _, ok := Parse(junk); ok {
			t.Errorf("%x parsed", junk)
		}
	}
}

// The signed bytes are exactly the spec's: a 36-byte tag, one channel byte, a little-endian timestamp, the text.
func TestCanonical(t *testing.T) {
	c := Canonical("Public", 0x01020304, "hi")
	if len(domain) != 36 || len(c) != 36+1+4+2 || string(c[:36]) != domain || c[37] != 4 || c[40] != 1 || string(c[41:]) != "hi" {
		t.Errorf("canonical = %s", hex.EncodeToString(c))
	}
	if h := sha256.Sum256([]byte("Public")); ChannelHash("Public") != h[0] {
		t.Error("channel hash")
	}
}
