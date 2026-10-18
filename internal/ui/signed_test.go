package ui

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/signing"
	"github.com/verdantran/meshcore-tui/internal/sim"
	"github.com/verdantran/meshcore-tui/internal/store"
)

type signer struct {
	priv ed25519.PrivateKey
	pub  [32]byte
}

func newSigner(name string) signer {
	seed := sha256.Sum256([]byte(name))
	priv := ed25519.NewKeyFromSeed(seed[:])
	return signer{priv, [32]byte(priv.Public().(ed25519.PublicKey))}
}

func (s signer) datagram(channel string, ts uint32, text string) []byte {
	d := signing.Datagram{TS: ts, KeyID: [8]byte(s.pub[:8])}
	copy(d.Sig[:], ed25519.Sign(s.priv, signing.Canonical(channel, ts, text)))
	return d.Bytes()
}

// say posts a channel message as name at the harness's time, and returns its timestamp.
func (h *harness) say(idx int, name, text string) uint32 {
	h.incoming(radio.Incoming{Channel: true, ChannelIdx: idx, Hops: 1, Text: name + ": " + text})
	return uint32(h.clk.t.Unix())
}

func (h *harness) datagram(idx int, data []byte) {
	h.m.Update(eventMsg{radio.ChannelData{At: h.clk.t, Idx: idx, Type: signing.DataType, Data: data}})
	h.settle()
}

func (h *harness) lastMessage() store.Message {
	h.m.refreshChats()
	return h.m.chats.msgs[len(h.m.chats.msgs)-1]
}

func TestSignedMessages(t *testing.T) {
	h := newHarness(t)
	zed, other := newSigner("zed"), newSigner("mallory")
	h.m.contacts.heard = append(h.m.contacts.heard, heardNode{radio.Contact{Key: zed.pub, Name: "Zed"}, h.clk.t},
		heardNode{radio.Contact{Key: other.pub, Name: "Mallory"}, h.clk.t})
	h.openConvo("#bristol")
	idx := 1

	// The signature after its message.
	ts := h.say(idx, "Zed", "meet at the hill")
	h.datagram(idx, zed.datagram("#bristol", ts, "meet at the hill"))
	if m := h.lastMessage(); m.Sig != store.SigValid || [32]byte(m.SigKey) != zed.pub {
		t.Fatalf("verified = %+v", m)
	}
	if !strings.Contains(h.screen(), "signed · not pinned") {
		t.Errorf("unpinned:\n%s", h.screen())
	}

	// Pin it from the message; then it reads ✓.
	h.key(tea.KeyRight)
	if !strings.Contains(h.screen(), "K pin key") {
		t.Error("K is offered")
	}
	h.keys("K")
	if !strings.Contains(h.screen(), "✓ signed") || !strings.Contains(h.lastLog(), "PINNED  Zed") {
		t.Errorf("pinned:\n%s", h.screen())
	}
	h.keys("K")
	if _, ok := h.m.pins()["Zed"]; ok {
		t.Error("K again unpins")
	}
	h.keys("K")
	h.key(tea.KeyEscape)

	// The signature before its message waits for it.
	h.clk.t = h.clk.t.Add(time.Minute)
	ts = uint32(h.clk.t.Unix())
	h.datagram(idx, zed.datagram("#bristol", ts, "on my way"))
	if len(h.m.chats.sigs) != 1 {
		t.Fatal("held")
	}
	h.say(idx, "Zed", "on my way")
	if m := h.lastMessage(); m.Sig != store.SigValid || len(h.m.chats.sigs) != 0 {
		t.Errorf("paired on arrival: %+v", m)
	}

	// Someone else's key under Zed's name is flagged, loudly.
	h.clk.t = h.clk.t.Add(time.Minute)
	ts = h.say(idx, "Zed", "send me your password")
	h.datagram(idx, other.datagram("#bristol", ts, "send me your password"))
	if !strings.Contains(h.screen(), "⚠ not Zed's key") || !strings.Contains(plainLog(h), "signed by a different key") {
		t.Errorf("impersonation:\n%s", h.screen())
	}
	h.key(tea.KeyRight)
	h.keys("K")
	if h.m.asking == nil || !strings.Contains(h.screen(), "REPLACE IT") {
		t.Error("replacing a pin asks first")
	}
	h.key(tea.KeyEscape)
	if p := h.m.pins()["Zed"]; p.Key == other.pub {
		t.Error("any other key keeps the pin")
	}
	h.key(tea.KeyEscape)

	// Unsigned, from a name that signs: flagged once the signature's had time to arrive.
	h.clk.t = h.clk.t.Add(time.Minute)
	h.say(idx, "Zed", "trust me")
	if strings.Contains(h.screen(), "usually signs") {
		t.Error("too soon to tell")
	}
	h.clk.t = h.clk.t.Add(sigWait)
	if !strings.Contains(h.screen(), "⚠ unsigned · Zed usually signs") {
		t.Errorf("unsigned:\n%s", h.screen())
	}

	// A signature that doesn't match, and one from a key never heard.
	h.clk.t = h.clk.t.Add(time.Minute)
	ts = h.say(idx, "Zed", "the real text")
	h.datagram(idx, zed.datagram("#bristol", ts, "a different text"))
	if !strings.Contains(h.screen(), "⚠ bad signature") {
		t.Errorf("bad:\n%s", h.screen())
	}
	h.clk.t = h.clk.t.Add(time.Minute)
	ts = h.say(idx, "Yan", "hello")
	h.datagram(idx, newSigner("yan").datagram("#bristol", ts, "hello"))
	if !strings.Contains(h.screen(), "signed by an unknown key") {
		t.Errorf("unknown key:\n%s", h.screen())
	}
	h.clk.t = h.clk.t.Add(time.Minute)
	ts = h.say(idx, "Bob", "it's me, Bob")
	h.datagram(idx, other.datagram("#bristol", ts, "it's me, Bob"))
	if !strings.Contains(h.screen(), "⚠ signed with Mallory's key") {
		t.Errorf("a key that's someone else's:\n%s", h.screen())
	}
	h.key(tea.KeyRight)
	h.keys("K")
	if _, ok := h.m.pins()["Bob"]; ok || !strings.Contains(h.screen(), "LETTING THEM POST AS BOB") {
		t.Errorf("K asks before pinning another's key:\n%s", h.screen())
	}
	h.keys("y")
	if p := h.m.pins()["Bob"]; p.Key != other.pub {
		t.Error("y pins it anyway")
	}
	h.key(tea.KeyEscape)
	// Junk and other data types are ignored; a signature for a channel we don't have waits and expires.
	h.datagram(idx, []byte("not a signature"))
	h.m.Update(eventMsg{radio.ChannelData{Idx: idx, Type: 7, Data: zed.datagram("#bristol", 1, "x")}})
	h.datagram(9, zed.datagram("nowhere", 1, "x"))
	h.clk.t = h.clk.t.Add(sigHold + time.Minute)
	h.datagram(9, zed.datagram("nowhere", 2, "x"))
	if len(h.m.chats.sigs) != 1 {
		t.Errorf("held signatures expire: %d", len(h.m.chats.sigs))
	}
}

func TestSendingSigned(t *testing.T) {
	h := newHarness(t)
	cfg, _ := config.Load(t.TempDir() + "/config.toml")
	h.m.opt.Config = cfg
	h.openConvo("#bristol")
	h.keys("S")
	if !h.m.signedChannel("#bristol") || !strings.Contains(h.screen(), "✍ signing your messages") {
		t.Fatalf("signing on:\n%s", h.screen())
	}
	if back, _ := config.Load(cfg.Path()); len(back.Messages.SignChannels) != 1 {
		t.Error("saved")
	}
	h.key(tea.KeyEnter)
	h.keys("hello all")
	if !strings.Contains(h.screen(), "✍ signed") {
		t.Error("the compose line says it'll be signed")
	}
	h.m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if h.m.signThis() || !strings.Contains(h.screen(), "ctrl+s signs") {
		t.Error("ctrl+s flips it for this message")
	}
	h.m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	h.key(tea.KeyEnter)
	h.until("signed", func() bool {
		m := h.lastMessage()
		return m.Out && m.Sig == store.SigValid
	})
	if !strings.Contains(h.screen(), "✍ signed") {
		t.Errorf("our message is marked signed:\n%s", h.screen())
	}

	// Signing off; the channel's messages go unsigned again.
	h.key(tea.KeyEscape)
	h.keys("S")
	if h.m.signedChannel("#bristol") {
		t.Error("off")
	}
	// Signing isn't for direct messages, and old firmware can't.
	h.openConvo("Alice")
	h.keys("S")
	if !strings.Contains(h.lastLog(), "only channel messages") {
		t.Error("direct")
	}
	h.m.ready.Device.Firmware = "v1.14.2"
	h.openConvo("#bristol")
	h.keys("S")
	if !strings.Contains(h.lastLog(), "needs firmware v1.15.0") {
		t.Errorf("old firmware: %s", h.lastLog())
	}
	if r := h.m.signingRow(); r.value != "not available" {
		t.Error("device row")
	}
	cfg.Messages.SignChannels = []string{"#bristol"}
	h.key(tea.KeyEnter)
	h.keys("plain")
	h.key(tea.KeyEnter)
	if !strings.Contains(plainLog(h), "SENDING UNSIGNED") {
		t.Error("sent unsigned when the radio can't sign")
	}
}

func TestPinsFromContactsAndDevice(t *testing.T) {
	h := newHarness(t)
	alice := h.contact("Alice")
	h.m.pin("Alice", newSigner("other").pub)
	h.keys("2")
	h.pick("Alice")
	if !strings.Contains(h.screen(), "the name is pinned to a different key") {
		t.Errorf("contact detail:\n%s", h.screen())
	}
	h.keys("K")
	if p := h.m.pins()["Alice"]; p.Key != alice.Key || !strings.Contains(h.screen(), "✓ pinned") {
		t.Error("K pins the contact's key")
	}
	h.keys("K")
	if _, ok := h.m.pins()["Alice"]; ok {
		t.Error("K again unpins")
	}
	r := h.m.signingRow()
	if r.value != "supported" {
		t.Errorf("device row = %+v", r)
	}
	r.press(h.m)
	if !strings.Contains(h.lastLog(), "no channels sign") {
		t.Error(h.lastLog())
	}
}

// failingSigner sends, but its radio won't sign.
type failingSigner struct{ *sim.Sim }

func (failingSigner) Sign(context.Context, []byte) ([64]byte, error) {
	return [64]byte{}, errors.New("the radio refused")
}

func TestSignatureFailsAfterSending(t *testing.T) {
	h := newHarness(t)
	cfg, _ := config.Load(t.TempDir() + "/config.toml")
	cfg.Messages.SignChannels = []string{"#bristol"}
	h.m.opt.Config = cfg
	h.m.radio = failingSigner{h.s}
	h.openConvo("#bristol")
	h.key(tea.KeyEnter)
	h.keys("hello")
	h.key(tea.KeyEnter)
	h.until("sent", func() bool { return strings.Contains(plainLog(h), "SENT UNSIGNED") })
	if m := h.lastMessage(); !m.Out || m.Sig != store.Unsigned {
		t.Errorf("the message went, unsigned: %+v", m)
	}
	h.m.opt.Store.Close() // and with the store gone, pinning says so
	h.m.chats.pins = map[string]store.Pin{}
	h.m.pin("Zed", [32]byte{1})
	h.m.unpin("Zed")
	if !strings.Contains(plainLog(h), "PIN  ") {
		t.Error("store errors are reported")
	}
}

func channelDataPacket(t *testing.T, h *harness, channel string, dataType uint16, data []byte) []byte {
	t.Helper()
	for _, ch := range h.m.chat.Channels() {
		if ch.Name == channel {
			plain, _ := meshcore.BuildGroupDataPayload(meshcore.GroupDataPayload{DataType: dataType, Data: data})
			gd, err := meshcore.NewGroupData(meshcore.DeriveChannelHash(ch.Secret), ch.Secret[:], plain)
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := gd.ToBytes()
			return rawPacket(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpData, []byte{0xA3}, payload)
		}
	}
	t.Fatalf("no channel %s", channel)
	return nil
}

func TestSignaturesOnPackets(t *testing.T) {
	h := newHarness(t)
	zed := newSigner("zed")
	h.m.contacts.heard = append(h.m.contacts.heard, heardNode{radio.Contact{Key: zed.pub, Name: "Zed"}, h.clk.t})
	ts := h.say(1, "Zed", "meet at the hill")
	sig := zed.datagram("#bristol", ts, "meet at the hill")
	h.datagram(1, sig)                                                      // the radio hands over the signature
	h.hear(channelDataPacket(t, h, "#bristol", signing.DataType, sig), 3)   // and its packet is heard
	h.hear(channelDataPacket(t, h, "#bristol", 0x1234, []byte("other")), 3) // another kind of datagram
	unmatched := zed.datagram("#bristol", ts+60, "never arrived")
	h.hear(channelDataPacket(t, h, "#bristol", signing.DataType, unmatched), 3)
	h.keys("4")
	scr := h.screen()
	if !strings.Contains(scr, "#bristol · signature for Zed: meet at the hill · signed · not") {
		t.Fatalf("signature on Packets:\n%s", scr)
	}
	if !strings.Contains(scr, "for a message not heard") || strings.Contains(scr, "0x1234") {
		t.Errorf("unmatched shown, other data not:\n%s", scr)
	}
	h.m.pin("Zed", zed.pub)
	if !strings.Contains(h.screen(), "signature for Zed: meet at the hill · ✓ signed") {
		t.Error("the verdict follows the pins")
	}
	if r, ok := h.m.selectedPacket(); !ok || r.dec.Signature == nil {
		t.Fatal("the newest is a signature")
	}
	if !strings.Contains(h.screen(), "a signature by key") {
		t.Errorf("detail:\n%s", h.screen())
	}
}
