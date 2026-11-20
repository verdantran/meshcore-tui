package signing

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// v2-vectors.json is meshcore-companion's docs/signing-v2-vectors.json, unchanged: every value in it must be
// reproduced exactly, so this and the other implementations provably agree.
type vectors struct {
	Inputs struct {
		SigningSeed  string `json:"signing_seed"`
		RadioSeed    string `json:"radio_seed"`
		Text         string `json:"text"`
		Channel      string `json:"channel"`
		TS           uint32 `json:"ts"`
		Passphrase   string `json:"passphrase"`
		PRFOutput    string `json:"prf_output"`
		RecoveryCode string `json:"recovery_code"`
	} `json:"inputs"`
	SigningPub     string          `json:"signing_pub"`
	RadioPub       string          `json:"radio_pub"`
	CanonicalV2    string          `json:"canonical_v2"`
	Signature      string          `json:"signature"`
	PacketMC2      string          `json:"packet_mc2"`
	BindInput      string          `json:"bind_input"`
	PacketMCB      string          `json:"packet_mcb"`
	PacketMCR      string          `json:"packet_mcr"`
	HMACSecretSalt string          `json:"hmac_secret_salt"`
	Keyfile        json.RawMessage `json:"keyfile"`
}

func load(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile("testdata/v2-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func h(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func key32(t *testing.T, s string) [32]byte { return [32]byte(h(t, s)) }

func TestV2Vectors(t *testing.T) {
	v := load(t)
	in := v.Inputs
	priv := ed25519.NewKeyFromSeed(h(t, in.SigningSeed))
	radioPriv := ed25519.NewKeyFromSeed(h(t, in.RadioSeed))
	pub, radio := key32(t, v.SigningPub), key32(t, v.RadioPub)
	if got := hex.EncodeToString(priv.Public().(ed25519.PublicKey)); got != v.SigningPub {
		t.Fatalf("signing pub %s", got)
	}
	msg := CanonicalV2(radio, in.Channel, in.TS, in.Text)
	if hex.EncodeToString(msg) != v.CanonicalV2 {
		t.Fatal("canonical v2 differs")
	}
	sig := ed25519.Sign(priv, msg)
	if hex.EncodeToString(sig) != v.Signature {
		t.Fatal("signature differs")
	}
	d := DatagramV2{KeyID: [8]byte(pub[:8]), TS: in.TS, Sig: [64]byte(sig)}
	if hex.EncodeToString(d.Bytes()) != v.PacketMC2 {
		t.Fatal("MC2 packet differs")
	}
	p, ok := ParseV2(h(t, v.PacketMC2))
	if !ok || !p.Verify(pub, radio, in.Channel, in.Text) {
		t.Fatal("MC2 doesn't parse or verify")
	}
	if _, ok := Parse(h(t, v.PacketMC2)); ok {
		t.Fatal("a v1 parser must not read an MC2 packet")
	}
	if hex.EncodeToString(BindInput(radio, pub, in.TS)) != v.BindInput {
		t.Fatal("bind input differs")
	}
	b := Binding{Key: pub, TS: in.TS, Sig: [64]byte(ed25519.Sign(radioPriv, BindInput(radio, pub, in.TS)))}
	if hex.EncodeToString(b.Bytes()) != v.PacketMCB {
		t.Fatal("MCB packet differs")
	}
	if pb, ok := ParseBinding(h(t, v.PacketMCB)); !ok || !pb.Verify(radio) || pb.Verify(pub) {
		t.Fatal("MCB must verify against its radio only")
	}
	r := Revocation{Key: pub, TS: in.TS + 60, Sig: [64]byte(ed25519.Sign(priv, RevokeInput(pub, in.TS+60)))}
	if hex.EncodeToString(r.Bytes()) != v.PacketMCR {
		t.Fatal("MCR packet differs")
	}
	if _, ok := ParseRevocation(h(t, v.PacketMCR)); !ok {
		t.Fatal("MCR doesn't verify")
	}
}

func TestV2BindsRadioChannelText(t *testing.T) {
	v := load(t)
	in := v.Inputs
	pub, radio := key32(t, v.SigningPub), key32(t, v.RadioPub)
	d, _ := ParseV2(h(t, v.PacketMC2))
	other := [32]byte(ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey))
	for name, ok := range map[string]bool{
		"other radio":   d.Verify(pub, other, in.Channel, in.Text),
		"other channel": d.Verify(pub, radio, "#other", in.Text),
		"other text":    d.Verify(pub, radio, in.Channel, in.Text+"!"),
		"as v1":         Datagram(d).Verify(pub, in.Channel, in.Text),
	} {
		if ok {
			t.Errorf("%s verified", name)
		}
	}
}

func TestForgedRevocationRefused(t *testing.T) {
	v := load(t)
	pub := key32(t, v.SigningPub)
	thief := ed25519.NewKeyFromSeed(make([]byte, 32))
	r := Revocation{Key: pub, TS: 1, Sig: [64]byte(ed25519.Sign(thief, RevokeInput(pub, 1)))}
	if _, ok := ParseRevocation(r.Bytes()); ok {
		t.Fatal("a revocation not signed by its own key was accepted")
	}
}

func TestKeyfileSlots(t *testing.T) {
	v := load(t)
	in := v.Inputs
	k, err := ReadKeyfile(strings.NewReader(string(v.Keyfile)))
	if err != nil {
		t.Fatal(err)
	}
	want := v.SigningPub
	check := func(name string, priv ed25519.PrivateKey, err error) {
		t.Helper()
		if err != nil || hex.EncodeToString(priv.Public().(ed25519.PublicKey)) != want {
			t.Fatalf("%s: %v", name, err)
		}
	}
	priv, err := k.UnlockPassphrase(in.Passphrase)
	check("passphrase", priv, err)
	rec := h(t, in.RecoveryCode)
	priv, err = k.UnlockRecovery(recoveryText(rec))
	check("recovery", priv, err)
	for _, s := range k.Slots {
		if s.Type == "passkey" {
			priv, err = k.UnlockPRF(s, h(t, in.PRFOutput))
			check("passkey", priv, err)
			salt, _ := HMACSecretSalt(s)
			if hex.EncodeToString(salt[:]) != v.HMACSecretSalt {
				t.Fatal("hmac-secret salt differs")
			}
		}
	}
	if _, err := k.UnlockPassphrase("kettle orbit lantern fog"); err != ErrWrongSecret {
		t.Fatalf("wrong passphrase: %v", err)
	}
	k2 := k
	k2.Pub = hex.EncodeToString(make([]byte, 32)) // a slot moved into another key's file
	if _, err := k2.UnlockPassphrase(in.Passphrase); err == nil {
		t.Fatal("slot opened under another public key")
	}
}

// recoveryText writes 32 bytes the way the apps show a recovery code.
func recoveryText(b []byte) string {
	const alpha = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var bits, out strings.Builder
	for _, x := range b {
		for i := 7; i >= 0; i-- {
			bits.WriteByte('0' + x>>i&1)
		}
	}
	s := bits.String()
	for i := 0; i < len(s); i += 5 {
		var v int
		for j := i; j < i+5; j++ {
			v <<= 1
			if j < len(s) && s[j] == '1' {
				v |= 1
			}
		}
		if i > 0 && i%20 == 0 {
			out.WriteByte('-')
		}
		out.WriteByte(alpha[v])
	}
	return strings.ToLower(out.String())
}
