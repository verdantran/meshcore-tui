package ui

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/signing"
	"github.com/verdantran/meshcore-tui/internal/sim"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
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
	t.Parallel()
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

	// A flag's tags take no cell, so tags hung on a pinned 🏴 name don't hide that it looks like the name.
	h.m.pin("🏴 Zed", zed.pub)
	h.clk.t = h.clk.t.Add(time.Minute)
	h.say(1, "🏴\U000e0078\U000e007f Zed", "trust me")
	if v := h.verdict(); v != "⚠ looks like 🏴 Zed" {
		t.Errorf("tags after the flag: %q", v)
	}

	// Where joined emoji are drawn as their parts, ♀ for ♂ or another skin tone doesn't show, so neither hides a look-alike.
	h.m.pin("🏊\u200d♂ Zed", zed.pub)
	for _, fake := range []string{"🏊\u200d♀ Zed", "🏊 Zed"} {
		h.clk.t = h.clk.t.Add(time.Minute)
		h.say(1, fake, "trust me")
		if v := h.verdict(); v != "⚠ looks like 🏊\u200d♂ Zed" {
			t.Errorf("%q: %q", fake, v)
		}
	}
	h.m.pin("👍🏽 Zed", zed.pub)
	h.clk.t = h.clk.t.Add(time.Minute)
	h.say(1, "👍🏿 Zed", "trust me")
	if v := h.verdict(); v != "⚠ looks like 👍🏽 Zed" {
		t.Errorf("another skin tone: %q", v)
	}

	// Many terminals draw a regional indicator as its letter, and a flag as two where they don't draw flags.
	h.m.pin("ZedFR", zed.pub)
	h.clk.t = h.clk.t.Add(time.Minute)
	h.say(1, "Zed\U0001F1EB\U0001F1F7", "trust me")
	if v := h.verdict(); v != "⚠ looks like ZedFR" {
		t.Errorf("a flag for its letters: %q", v)
	}

	// Where tmux and the terminal disagree on an emoji that shows as text, it's drawn as *, so one hides another.
	h.m.pin("Zed\U0001F574", zed.pub)
	h.clk.t = h.clk.t.Add(time.Minute)
	h.say(1, "Zed\U0001F590", "trust me")
	if v := h.verdict(); v != "⚠ looks like Zed\U0001F574" {
		t.Errorf("another emoji that shows as text: %q", v)
	}

	// Inside tmux an older symbol with the emoji selector can be drawn plain with a space after it.
	h.m.pin("Zed\u270c\ufe0fx", zed.pub)
	h.clk.t = h.clk.t.Add(time.Minute)
	h.say(1, "Zed\u270c x", "trust me")
	if v := h.verdict(); v != "⚠ looks like Zed\u270c\ufe0fx" {
		t.Errorf("a symbol and a space: %q", v)
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// restart quits and starts the app again at a time, on the same store.
func (h *harness) restart(at time.Time) *harness {
	h.t.Helper()
	h.s.Close()
	s := sim.New(5).Fast().Lossless().Quiet()
	h.t.Cleanup(func() { s.Close() })
	c := &clock{t: at}
	m := New(Options{Theme: theme.Neon, Motion: motion.Full, Clock: c.now, Store: h.m.opt.Store, ExportDir: h.t.TempDir(),
		Dial: func(Target) radio.Radio { return s }, Start: &Target{Kind: "SIM", Addr: "demo"},
		Env: func(string) string { return "" }})
	n := &harness{t: h.t, m: m, s: s, clk: c, results: make(chan tea.Msg, 1024)}
	n.start(m.Init())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	n.until("ready", func() bool { return m.ready.Self.Name != "" })
	n.toMain()
	return n
}

// held is a channel message the radio kept while the app was away, as the next start drains it.
func (h *harness) held(idx int, name, text string, sent time.Time) {
	h.m.Update(eventMsg{radio.Incoming{Channel: true, ChannelIdx: idx, Hops: 1, Text: name + ": " + text, SentAt: sent, At: h.clk.t, Queued: true}})
	h.settle()
}

// A copy of a signed message, sent again while the app is closed, is still a replay when the radio hands
// it over at the next start: what was signed before is kept with the history, not just in memory.
func TestReplayAcrossRestart(t *testing.T) {
	t.Parallel()
	h, zed := pinnedZed(t)
	h.clk.t = time.Now().Add(time.Hour).Truncate(time.Second) // after anything the simulator stamped with the real time
	sent := h.clk.t
	ts := h.say(1, "Zed", "meet at the hill")
	sig := zed.datagram("#bristol", ts, "meet at the hill")
	h.datagram(1, sig)
	if v := h.verdict(); v != "✓ signed" {
		t.Fatalf("first: %q", v)
	}
	h.advance(5 * time.Minute)
	closed := h.clk.t

	h = h.restart(closed.Add(3 * time.Hour))
	h.openConvo("#bristol")
	if last := h.m.chat.LastRun(); last.Before(closed.Add(-time.Minute)) || last.After(closed) {
		t.Errorf("last ran %v, closed %v", last, closed)
	}
	h.held(1, "Zed", "meet at the hill", sent)
	h.datagram(1, sig)
	if m := h.lastMessage(); m.Sig != store.SigStale || h.verdict() != "⚠ old signature" {
		t.Errorf("replayed while closed: %+v %q", m, h.verdict())
	}

	// Sent while the app was closed: it counts.
	later := closed.Add(time.Hour)
	h.held(1, "Zed", "back at six", later)
	h.datagram(1, zed.datagram("#bristol", uint32(later.Unix()), "back at six"))
	if v := h.verdict(); v != "✓ signed" {
		t.Errorf("held by the radio: %q", v)
	}

	// Sent well before the app last ran, yet never seen: it would have been heard live, so it's old.
	early := closed.Add(-2 * time.Hour)
	h.held(1, "Zed", "from this morning", early)
	h.datagram(1, zed.datagram("#bristol", uint32(early.Unix()), "from this morning"))
	if v := h.verdict(); v != "⚠ old signature" {
		t.Errorf("from before the app last ran: %q", v)
	}
}

// Leaving a channel forgets the signatures held for it, since another channel may take its slot.
func TestLeavingForgetsSignatures(t *testing.T) {
	t.Parallel()
	h, zed := pinnedZed(t)
	ts := h.say(1, "Zed", "hello")
	h.datagram(1, zed.datagram("#bristol", ts, "hello"))
	h.datagram(1, zed.datagram("#bristol", ts+60, "not yet"))
	h.datagram(2, zed.datagram("other", ts, "elsewhere"))
	if len(h.m.chats.signedMsgs) == 0 || len(h.m.chats.usedSigs) == 0 {
		t.Fatal("held")
	}
	ch, _ := h.m.chat.ChannelFor(store.ChannelConvo(1))
	h.m.removeChannel(ch)
	for _, p := range h.m.chats.sigs {
		if p.idx == 1 {
			t.Error("held signature kept")
		}
	}
	if len(h.m.chats.sigs) != 1 || len(h.m.chats.signedMsgs) != 0 || len(h.m.chats.usedSigs) != 0 {
		t.Errorf("kept: %d %d %d", len(h.m.chats.sigs), len(h.m.chats.signedMsgs), len(h.m.chats.usedSigs))
	}
}

// Signing a channel goes by its secret: a channel that takes its name and slot later doesn't sign.
func TestSigningFollowsTheSecret(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cfg, _ := config.Load(t.TempDir() + "/config.toml")
	cfg.Messages.SignChannels = []string{"#bristol", "Gone"}
	h.m.opt.Config = cfg
	h.m.moveSigning()
	if !h.m.signedChannel(store.ChannelConvo(1)) || cfg.Messages.SignChannels != nil || len(cfg.Messages.SignIDs) != 1 {
		t.Fatalf("moved: %+v", cfg.Messages)
	}
	chans := h.m.chat.Channels()
	for i := range chans {
		if chans[i].Idx == 1 {
			chans[i].Secret = [16]byte{0xEE}
		}
	}
	h.m.chat.ReplaceChannels(chans)
	if h.m.signedChannel(store.ChannelConvo(1)) {
		t.Error("a new channel under the old name signs")
	}
}

// A room post names its writer only by the 4 key bytes the room server gives, so it isn't called signed.
func TestRoomPostsAreNotSigned(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	room, alice := h.contact("Clifton Room"), h.contact("Alice")
	h.incoming(radio.Incoming{From: room.Prefix(), Hops: 1, Signed: true, Author: [4]byte(alice.Key[:4]), Text: "hello room"})
	h.openConvo("Clifton Room")
	prefix := radio.KeyHex(alice.Key[:4])
	row := ""
	for _, r := range strings.Split(h.screen(), "\n") {
		if strings.Contains(r, "Alice") && strings.Contains(r, "room post") {
			row = r
		}
	}
	if !strings.Contains(row, "room post · key "+prefix) || strings.Contains(row, "signed") || strings.Contains(row, "keys start") {
		t.Errorf("post: %q\n%s", row, h.screen())
	}

	// Another key with the same 4 bytes: the post could be either's.
	twin := alice.Key
	twin[31] ^= 1
	h.m.contacts.heard = append(h.m.contacts.heard, heardNode{radio.Contact{Key: twin, Name: "Mallory"}, h.clk.t})
	if !strings.Contains(h.screen(), "⚠ 2 keys start "+prefix) {
		t.Errorf("shared prefix:\n%s", h.screen())
	}

	// A pin on the name, to a key the bytes don't match.
	h.m.pin("Alice", newSigner("alice").pub)
	if v := h.verdict(); v != "⚠ not Alice's key" {
		t.Errorf("pinned elsewhere: %q", v)
	}
	h.m.pin("Alice", alice.Key)
	if v := h.verdict(); v != "" {
		t.Errorf("pinned: %q", v)
	}
}

// verdictFrame is a screen of 30 signed messages from heard keys, with 1000 nodes heard and a pin.
func verdictFrame(tb testing.TB) func() {
	h := newHarness(tb)
	for i := range 1000 {
		h.m.contacts.heard = append(h.m.contacts.heard, heardNode{radio.Contact{Key: sha256.Sum256([]byte{byte(i), byte(i >> 8)}), Name: fmt.Sprintf("Node %d", i)}, h.clk.t})
	}
	h.m.pin("Zed", newSigner("zed").pub)
	msgs := make([]store.Message, 30)
	for i := range msgs {
		k := h.m.contacts.heard[i].c.Key
		msgs[i] = store.Message{ID: int64(i + 1), Convo: store.ChannelConvo(1), Sender: fmt.Sprintf("Node %d", i), Text: "hi", Sig: store.SigValid, SigKey: k[:]}
	}
	return func() {
		for _, msg := range msgs {
			h.m.sigVerdict(msg)
		}
	}
}

// Drawing signed messages does no look-alike work of its own, however many nodes are heard.
func TestVerdictCostPerFrame(t *testing.T) {
	frame := verdictFrame(t)
	frame()
	if a := testing.AllocsPerRun(20, frame); a > 1 {
		t.Errorf("%.0f allocations a frame", a)
	}
}

func BenchmarkVerdictFrame(b *testing.B) {
	frame := verdictFrame(b)
	b.ReportAllocs()
	for b.Loop() {
		frame()
	}
}
