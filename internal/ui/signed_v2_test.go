package ui

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/signing"
	"github.com/verdantran/meshcore-tui/internal/store"
)

// v2 (SIGNING.md "Version 2"): the radio vouches for a separate signing key, and posts are signed with that.

func (s signer) binding(radioKey signer, ts uint32) []byte {
	b := signing.Binding{Key: s.pub, TS: ts}
	copy(b.Sig[:], ed25519.Sign(radioKey.priv, signing.BindInput(radioKey.pub, s.pub, ts)))
	return b.Bytes()
}

func (s signer) datagramV2(radioKey [32]byte, channel string, ts uint32, text string) []byte {
	d := signing.DatagramV2{TS: ts, KeyID: [8]byte(s.pub[:8])}
	copy(d.Sig[:], ed25519.Sign(s.priv, signing.CanonicalV2(radioKey, channel, ts, text)))
	return d.Bytes()
}

func (s signer) revocation(ts uint32) []byte {
	r := signing.Revocation{Key: s.pub, TS: ts}
	copy(r.Sig[:], ed25519.Sign(s.priv, signing.RevokeInput(s.pub, ts)))
	return r.Bytes()
}

func TestSignedV2(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	zedRadio, zedKey, thief := newSigner("zed radio"), newSigner("zed signing key"), newSigner("thief")
	h.m.contacts.heard = append(h.m.contacts.heard, heardNode{radio.Contact{Key: zedRadio.pub, Name: "Zed"}, h.clk.t})
	h.openConvo("#bristol")
	idx := 1
	now := func() uint32 { return uint32(h.clk.t.Unix()) }

	// Without the binding, the signing key is unknown.
	ts := h.say(idx, "Zed", "meet at the hill")
	h.datagram(idx, zedKey.datagramV2(zedRadio.pub, "#bristol", ts, "meet at the hill"))
	if m := h.lastMessage(); m.Sig != store.SigUnknown {
		t.Fatalf("no binding = %+v", m.Sig)
	}

	// The radio vouches for the key; the next post verifies, and the key recorded is the signing key.
	h.datagram(idx, zedKey.binding(zedRadio, now()))
	h.clk.t = h.clk.t.Add(time.Minute)
	ts = h.say(idx, "Zed", "on my way")
	h.datagram(idx, zedKey.datagramV2(zedRadio.pub, "#bristol", ts, "on my way"))
	if m := h.lastMessage(); m.Sig != store.SigValid || [32]byte(m.SigKey) != zedKey.pub {
		t.Fatalf("v2 = %+v", m)
	}
	h.m.pin("Zed", zedKey.pub)

	// A signature naming another radio fails: a binding can't be borrowed.
	h.clk.t = h.clk.t.Add(time.Minute)
	ts = h.say(idx, "Zed", "borrowed")
	h.datagram(idx, zedKey.datagramV2(thief.pub, "#bristol", ts, "borrowed"))
	if m := h.lastMessage(); m.Sig != store.SigBad {
		t.Fatalf("other radio = %+v", m.Sig)
	}

	// Someone with Zed's radio binds a key of their own: valid, but not the pinned key.
	h.datagram(idx, thief.binding(zedRadio, now()))
	h.clk.t = h.clk.t.Add(time.Minute)
	ts = h.say(idx, "Zed", "send money")
	h.datagram(idx, thief.datagramV2(zedRadio.pub, "#bristol", ts, "send money"))
	if m := h.lastMessage(); m.Sig != store.SigValid || [32]byte(m.SigKey) != thief.pub || !strings.Contains(h.screen(), "⚠ not Zed's key") {
		t.Fatalf("thief's key = %+v\n%s", m, h.screen())
	}

	// The radio's own (v1) signature now proves only the radio.
	h.clk.t = h.clk.t.Add(time.Minute)
	ts = h.say(idx, "Zed", "radio says hi")
	h.datagram(idx, zedRadio.datagram("#bristol", ts, "radio says hi"))
	if m := h.lastMessage(); m.Sig != store.SigRadioOnly || !strings.Contains(h.screen(), "radio-only") {
		t.Fatalf("v1 from a v2 radio = %+v\n%s", m.Sig, h.screen())
	}

	// A forged revocation is ignored; the real one sticks, even against a newer binding.
	h.datagram(idx, signing.Revocation{Key: zedKey.pub, TS: now()}.Bytes())
	if h.m.sigKeys()[zedKey.pub].Revoked {
		t.Fatal("unsigned revocation accepted")
	}
	h.datagram(idx, zedKey.revocation(now()))
	h.datagram(idx, zedKey.binding(zedRadio, now()+10))
	h.clk.t = h.clk.t.Add(time.Minute)
	ts = h.say(idx, "Zed", "after revoking")
	h.datagram(idx, zedKey.datagramV2(zedRadio.pub, "#bristol", ts, "after revoking"))
	if m := h.lastMessage(); m.Sig != store.SigRevoked || !strings.Contains(h.screen(), "revoked key") {
		t.Fatalf("revoked = %+v\n%s", m.Sig, h.screen())
	}
}

func TestSendingSignedV2(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cfg, _ := config.Load(t.TempDir() + "/config.toml")
	raw, err := os.ReadFile("../signing/testdata/v2-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Inputs     struct{ Passphrase string } `json:"inputs"`
		SigningPub string                      `json:"signing_pub"`
		Keyfile    json.RawMessage             `json:"keyfile"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	cfg.Messages.SigningKey = t.TempDir() + "/key.json"
	if err := os.WriteFile(cfg.Messages.SigningKey, v.Keyfile, 0o600); err != nil {
		t.Fatal(err)
	}
	h.m.opt.Config = cfg
	h.openConvo("#bristol")
	h.keys("S")
	h.key(tea.KeyEnter)
	h.keys("hello all")
	h.key(tea.KeyEnter)
	if !strings.Contains(h.screen(), "passphrase ▸") || h.lastMessage().Text == "hello all" {
		t.Fatalf("asks for the passphrase before sending:\n%s", h.screen())
	}

	// A wrong passphrase asks again; nothing is sent.
	h.keys("not the right words")
	h.key(tea.KeyEnter)
	h.until("refused", func() bool { return h.m.chats.unlock != nil && h.m.chats.unlock.err != "" })
	if h.lastMessage().Text == "hello all" {
		t.Fatal("sent with a wrong passphrase")
	}

	// The right one sends it, signed with the v2 key, after the radio vouches for that key.
	h.keys(v.Inputs.Passphrase)
	h.key(tea.KeyEnter)
	h.until("signed v2", func() bool {
		m := h.lastMessage()
		return m.Out && m.Text == "hello all" && m.Sig == store.SigValid
	})
	pub, _ := hex.DecodeString(v.SigningPub)
	if m := h.lastMessage(); !bytes.Equal(m.SigKey, pub) {
		t.Errorf("signed with %x, want the key file's key", m.SigKey)
	}
	if k := h.m.sigKeys()[[32]byte(pub)]; k.Radio != h.m.ready.Self.PubKey {
		t.Errorf("our binding = %+v", k)
	}

	// Esc at the prompt gives the message back to edit.
	h.keys("second")
	h.key(tea.KeyEnter)
	h.key(tea.KeyEscape)
	if h.m.chats.unlock != nil || h.m.chats.input.text != "second" {
		t.Errorf("esc: unlock=%v input=%q", h.m.chats.unlock, h.m.chats.input.text)
	}
}
