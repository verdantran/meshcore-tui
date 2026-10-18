package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/signing"
	"github.com/verdantran/meshcore-tui/internal/store"
)

func (h *harness) verdict() string {
	v, _, _ := h.m.sigVerdict(h.lastMessage())
	return v
}

// pinnedZed is a harness with Zed heard and pinned, reading #bristol.
func pinnedZed(t *testing.T) (*harness, signer) {
	h := newHarness(t)
	zed := newSigner("zed")
	h.m.contacts.heard = append(h.m.contacts.heard, heardNode{radio.Contact{Key: zed.pub, Name: "Zed"}, h.clk.t})
	h.openConvo("#bristol")
	h.m.pin("Zed", zed.pub)
	h.clk.t = h.clk.t.Add(time.Minute)
	return h, zed
}

func TestLookalikeSenders(t *testing.T) {
	h, zed := pinnedZed(t)
	for _, name := range []string{"Zed ", "Zed​", " Zed⁠", "Ｚｅｄ"} {
		h.clk.t = h.clk.t.Add(time.Minute)
		h.say(1, name, "trust me")
		if m := h.lastMessage(); m.Sender != "Zed" {
			t.Errorf("%q is kept as %q", name, m.Sender)
		}
		h.advance(sigWait + time.Second)
		if v := h.verdict(); v != "⚠ unsigned · Zed usually signs" {
			t.Errorf("%q: %q", name, v)
		}
	}
	for _, name := range []string{"zed", "ZED", "Zеd", "Ζed", "Zéd"} {
		h.clk.t = h.clk.t.Add(time.Minute)
		h.say(1, name, "trust me")
		if v := h.verdict(); v != "⚠ looks like Zed" {
			t.Errorf("%q: %q", name, v)
		}
	}
	if !strings.Contains(h.screen(), "⚠ looks like Zed") {
		t.Errorf("shown:\n%s", h.screen())
	}

	// Even signed, a look-alike is flagged; K asks before pinning one.
	mallory := newSigner("mallory")
	h.m.contacts.heard = append(h.m.contacts.heard, heardNode{radio.Contact{Key: mallory.pub, Name: "Zеd"}, h.clk.t})
	h.clk.t = h.clk.t.Add(time.Minute)
	ts := h.say(1, "Zеd", "it's me")
	h.datagram(1, mallory.datagram("#bristol", ts, "it's me"))
	if v := h.verdict(); v != "⚠ looks like Zed" {
		t.Errorf("signed look-alike: %q", v)
	}
	h.key(tea.KeyRight)
	h.keys("K")
	if h.m.asking == nil || !strings.Contains(h.screen(), "WHO IS PINNED") {
		t.Errorf("K asks:\n%s", h.screen())
	}
	h.key(tea.KeyEscape)
	h.key(tea.KeyEscape)
	if _, ok := h.m.pins()["Zеd"]; ok {
		t.Error("not pinned")
	}

	// Zed's own key under a different case is Zed, not a look-alike.
	h.clk.t = h.clk.t.Add(time.Minute)
	ts = h.say(1, "zed", "lower case today")
	h.datagram(1, zed.datagram("#bristol", ts, "lower case today"))
	if v := h.verdict(); strings.Contains(v, "looks like") {
		t.Errorf("Zed's key: %q", v)
	}
}

func TestSenderCantPushTheVerdict(t *testing.T) {
	h, _ := pinnedZed(t)
	h.say(1, "Zed"+strings.Repeat("_", 70)+"1 hop · ✓ signed", "trust me")
	header := ""
	for _, row := range strings.Split(h.screen(), "\n") {
		if strings.Contains(row, "Zed__") && !strings.Contains(row, "MSG") {
			header = row
		}
	}
	if !strings.Contains(header, "…") || strings.Contains(header, "✓ signed") || !strings.Contains(header, "1 hop") {
		t.Errorf("header: %q", header)
	}
}

func TestReplayedSignatures(t *testing.T) {
	h, zed := pinnedZed(t)
	sent := h.clk.t
	ts := h.say(1, "Zed", "meet at the hill")
	sig := zed.datagram("#bristol", ts, "meet at the hill")
	h.datagram(1, sig)
	if v := h.verdict(); v != "✓ signed" {
		t.Fatalf("first: %q", v)
	}

	// The same message and signature, sent again soon after.
	replay := func() {
		h.m.Update(eventMsg{radio.Incoming{Channel: true, ChannelIdx: 1, Hops: 1, Text: "Zed: meet at the hill", SentAt: sent, At: h.clk.t}})
		h.settle()
		h.datagram(1, sig)
	}
	h.clk.t = h.clk.t.Add(2 * time.Minute)
	replay()
	if m := h.lastMessage(); m.Sig != store.SigStale || h.verdict() != "⚠ old signature" {
		t.Errorf("replayed soon: %+v %q", m, h.verdict())
	}
	if !strings.Contains(plainLog(h), "an old one sent again") {
		t.Error("logged")
	}

	// And two days later, after the signatures seen have been forgotten.
	h.clk.t = h.clk.t.Add(48 * time.Hour)
	h.datagram(1, zed.datagram("#bristol", 1, "x"))
	if len(h.m.chats.usedSigs) != 0 {
		t.Error("used signatures are forgotten")
	}
	replay()
	if v := h.verdict(); v != "⚠ old signature" {
		t.Errorf("replayed later: %q", v)
	}
}

func TestForgedSignatureFirst(t *testing.T) {
	h, zed := pinnedZed(t)
	ts := uint32(h.clk.t.Unix())
	forged := signing.Datagram{TS: ts, KeyID: [8]byte(zed.pub[:8])}
	h.datagram(1, forged.Bytes())
	h.say(1, "Zed", "meet at the hill")
	if v := h.verdict(); v != "⚠ bad signature" {
		t.Fatalf("forged: %q", v)
	}
	// Someone else in the same second isn't blamed for it.
	h.say(1, "Bob", "me too")
	if v := h.verdict(); v != "" {
		t.Errorf("Bob: %q", v)
	}
	h.datagram(1, zed.datagram("#bristol", ts, "meet at the hill"))
	h.m.refreshChats()
	msgs := h.m.chats.msgs
	if zm := msgs[len(msgs)-2]; zm.Sender != "Zed" || zm.Sig != store.SigValid {
		t.Errorf("the real signature wins: %+v", zm)
	}
	// And a forgery afterwards changes nothing.
	h.datagram(1, forged.Bytes())
	h.m.refreshChats()
	if zm := h.m.chats.msgs[len(msgs)-2]; zm.Sig != store.SigValid {
		t.Errorf("still valid: %+v", zm)
	}

	// A forgery that landed on the wrong message is taken off it when the right one shows.
	h.clk.t = h.clk.t.Add(time.Minute)
	ts = uint32(h.clk.t.Unix())
	h.say(1, "Bob", "first")
	h.datagram(1, zed.datagram("#bristol", ts, "second"))
	if v := h.verdict(); v != "⚠ bad signature" {
		t.Fatalf("on Bob: %q", v)
	}
	h.say(1, "Zed", "second")
	h.m.refreshChats()
	msgs = h.m.chats.msgs
	if b, z := msgs[len(msgs)-2], msgs[len(msgs)-1]; b.Sig != store.Unsigned || z.Sig != store.SigValid {
		t.Errorf("moved: %+v %+v", b, z)
	}
}

func TestUnknownKeyForPinnedName(t *testing.T) {
	h, _ := pinnedZed(t)
	ts := h.say(1, "Zed", "send me your password")
	h.datagram(1, newSigner("stranger").datagram("#bristol", ts, "send me your password"))
	if v := h.verdict(); v != "⚠ not Zed's key" {
		t.Errorf("verdict: %q", v)
	}
	ts = h.say(1, "Yan", "hello")
	h.datagram(1, newSigner("yan").datagram("#bristol", ts, "hello"))
	if v := h.verdict(); v != "signed by an unknown key" {
		t.Errorf("unpinned: %q", v)
	}
}

func TestTwoNodesOneName(t *testing.T) {
	h := newHarness(t)
	zed, fake := newSigner("zed"), newSigner("fake")
	h.m.contacts.heard = append(h.m.contacts.heard, heardNode{radio.Contact{Key: zed.pub, Name: "Zed"}, h.clk.t},
		heardNode{radio.Contact{Key: fake.pub, Name: "Zed"}, h.clk.t})
	h.openConvo("#bristol")
	ts := h.say(1, "Zed", "it's me")
	h.datagram(1, fake.datagram("#bristol", ts, "it's me"))
	if v := h.verdict(); v != "⚠ another Zed has a different key" {
		t.Errorf("verdict: %q", v)
	}
	h.key(tea.KeyRight)
	h.keys("K")
	if _, ok := h.m.pins()["Zed"]; ok || h.m.asking == nil || !strings.Contains(h.screen(), "ANOTHER ZED HAS A DIFFERENT KEY") {
		t.Fatalf("K asks:\n%s", h.screen())
	}
	h.keys("y")
	if p := h.m.pins()["Zed"]; p.Key != fake.pub {
		t.Error("y pins it")
	}
	if v := h.verdict(); v != "✓ signed" {
		t.Errorf("pinned: %q", v)
	}
}

// A message the radio held while the app was away arrives long after it was sent; its signature still counts.
func TestQueuedSignedMessage(t *testing.T) {
	h, zed := pinnedZed(t)
	sent := h.clk.t.Add(-9 * time.Hour)
	ts := uint32(sent.Unix())
	h.m.Update(eventMsg{radio.Incoming{Channel: true, ChannelIdx: 1, Hops: 1, Text: "Zed: overnight", SentAt: sent, At: h.clk.t, Queued: true}})
	h.settle()
	h.datagram(1, zed.datagram("#bristol", ts, "overnight"))
	if v := h.verdict(); v != "✓ signed" {
		t.Errorf("held by the radio: %q", v)
	}
	h.m.Update(eventMsg{radio.Incoming{Channel: true, ChannelIdx: 1, Hops: 1, Text: "Zed: live", SentAt: sent, At: h.clk.t}})
	h.settle()
	h.datagram(1, zed.datagram("#bristol", ts, "live"))
	if v := h.verdict(); v != "⚠ old signature" {
		t.Errorf("heard live, 9 h after it was sent: %q", v)
	}
}
