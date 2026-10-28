package ui

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
)

func (h *harness) channel(name string) (radio.Channel, bool) {
	for _, ch := range h.m.chat.Channels() {
		if ch.Name == name {
			return ch, true
		}
	}
	return radio.Channel{}, false
}

func (h *harness) typeFast(s string) {
	for _, r := range s {
		h.m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestAddChannels(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("n")
	if !strings.Contains(h.screen(), "ADD A CHANNEL") || !strings.Contains(h.screen(), "3 of 40 slots used") {
		t.Fatalf("add prompt:\n%s", h.screen())
	}
	h.keys("#two words")
	h.key(tea.KeyEnter)
	if !strings.Contains(h.lastLog(), "one word") || h.m.chats.chanStage != chanName {
		t.Fatal("a bad hashtag is refused and the prompt stays")
	}
	h.clear()
	h.typeLine("#bath")
	h.until("joined", func() bool { _, ok := h.channel("#bath"); return ok })
	bath, _ := h.channel("#bath")
	if bath.Secret != meshcore.DeriveHashtagPSK("#bath") || !strings.Contains(h.lastLog(), "JOINED  #bath") {
		t.Errorf("bath = %+v", bath)
	}
	if cur, _ := h.m.selectedConvo(); cur.Convo != store.ChannelConvo(bath.Idx) {
		t.Error("the new channel is opened")
	}
	if chs, _ := h.s.Channels(t0ctx); len(chs) != 4 {
		t.Error("written to the radio")
	}
	if !strings.Contains(h.screen(), "4 channels") {
		t.Error("the footer counts the new channel")
	}

	// A private channel: a name, then its secret. A name too long is refused before the secret is asked for.
	h.keys("n")
	h.typeLine("健二健二健二健二健二健二")
	if !strings.Contains(h.lastLog(), "at most 31 bytes") || strings.Contains(h.screen(), "SECRET FOR") {
		t.Fatalf("a 36-byte name goes on to the secret:\n%s", h.screen())
	}
	h.key(tea.KeyEscape)
	h.keys("n")
	h.typeLine("Family")
	if !strings.Contains(h.screen(), "SECRET FOR FAMILY") {
		t.Fatalf("secret prompt:\n%s", h.screen())
	}
	h.typeLine("nothex")
	if !strings.Contains(h.lastLog(), "32 hex characters") {
		t.Error("bad secret refused")
	}
	h.clear()
	h.typeFast("000102030405060708090a0b0c0d0e0f")
	h.key(tea.KeyEnter)
	h.until("family", func() bool { _, ok := h.channel("Family"); return ok })

	// A new private channel opens its share view straight away.
	h.keys("n")
	h.typeLine("Club")
	h.key(tea.KeyEnter)
	h.until("club", func() bool { _, ok := h.channel("Club"); return ok })
	club, _ := h.channel("Club")
	scr := h.screen()
	if h.m.chats.chanStage != chanShare || !strings.Contains(scr, "SHARE CLUB") || !strings.Contains(scr, hex.EncodeToString(club.Secret[:])) ||
		!strings.Contains(scr, chat.ChannelLinkPrefix) {
		t.Fatalf("share:\n%s", scr)
	}
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil || !strings.Contains(h.lastLog(), "COPIED  link for Club") {
		t.Error("y copies")
	}
	h.keys("z")

	// Joining by link, and refusing a duplicate.
	link := chat.ChannelLink(radio.Channel{Name: "Walkers", Secret: [16]byte{4, 4, 1, 2, 3}})
	h.keys("n")
	h.typeFast(link)
	h.key(tea.KeyEnter)
	h.until("walkers", func() bool { _, ok := h.channel("Walkers"); return ok })
	h.keys("n")
	h.typeLine("#bath")
	if !strings.Contains(h.lastLog(), "already in that channel, as #bath") {
		t.Errorf("duplicate: %s", h.lastLog())
	}
	h.key(tea.KeyEscape)
	if h.m.chats.chanStage != chanNone {
		t.Error("esc")
	}
	h.keys("n")
	h.key(tea.KeyEnter)
	if h.m.chats.chanStage != chanName || !strings.Contains(h.lastLog(), "needs a name") {
		t.Error("empty")
	}
	h.key(tea.KeyEscape)
}

func TestShareAndLeaveChannels(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pub, _ := h.channel("Public")
	h.m.jumpToConvo(store.ChannelConvo(pub.Idx))
	h.keys("e")
	if !strings.Contains(h.screen(), "its secret is no secret") {
		t.Fatalf("public share:\n%s", h.screen())
	}
	h.keys("q") // any key closes, without quitting
	if h.m.chats.chanStage != chanNone {
		t.Fatal("closed")
	}
	bristol, _ := h.channel("#bristol")
	h.m.jumpToConvo(store.ChannelConvo(bristol.Idx))
	h.keys("e")
	if !strings.Contains(h.screen(), "sharing the name #bristol is enough") {
		t.Errorf("hashtag share:\n%s", h.screen())
	}
	h.keys(" ")
	msgs, _ := h.m.opt.Store.Messages(h.m.chat.Device(), store.ChannelConvo(bristol.Idx), 10)
	if len(msgs) == 0 {
		t.Fatal("the test needs some #bristol history")
	}
	h.keys("x")
	if !strings.Contains(h.screen(), "LEAVE #BRISTOL") || !strings.Contains(h.screen(), "history here is deleted too") {
		t.Fatalf("leave:\n%s", h.screen())
	}
	h.keys("n")
	if _, ok := h.channel("#bristol"); !ok {
		t.Fatal("any other key cancels")
	}
	h.keys("x")
	h.keys("y")
	h.until("left", func() bool { _, ok := h.channel("#bristol"); return !ok })
	if msgs, _ := h.m.opt.Store.Messages(h.m.chat.Device(), store.ChannelConvo(bristol.Idx), 10); len(msgs) != 0 {
		t.Error("history deleted")
	}
	if !strings.Contains(h.lastLog(), "LEFT  #bristol") {
		t.Errorf("log: %s", h.lastLog())
	}
	// A contact conversation has no channel to share or leave.
	h.m.jumpToConvo(store.ContactConvo(h.contact("Alice").Key))
	h.keys("e")
	if h.m.chats.chanStage != chanNone {
		t.Error("not a channel")
	}
}

func TestChannelCommand(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := func(line string) {
		h.keys(":")
		h.typeLine(line)
	}
	run("channel add #hilltop2")
	h.until("joined", func() bool { _, ok := h.channel("#hilltop2"); return ok })
	run("channel add Club 000102030405060708090a0b0c0d0e0f")
	h.until("club", func() bool { _, ok := h.channel("Club"); return ok })
	run("channel new Secret Club")
	h.until("new", func() bool { _, ok := h.channel("Secret Club"); return ok })
	h.keys("z")
	run("channel add Private")
	if !strings.Contains(h.lastLog(), "needs its secret") {
		t.Errorf("add without a secret: %s", h.lastLog())
	}
	run("channel share #hilltop2")
	if h.m.chats.chanStage != chanShare || h.m.tab != 0 {
		t.Error("share")
	}
	h.keys("z")
	run("channel remove club")
	if h.m.chats.chanStage != chanRemove {
		t.Error("remove asks")
	}
	h.keys("y")
	h.until("removed", func() bool { _, ok := h.channel("Club"); return !ok })
	for _, bad := range []string{"channel", "channel frob x", "channel remove nowhere", "channel add #a b c"} {
		run(bad)
	}
	if !strings.Contains(plainLog(h), "no channel called") || !strings.Contains(plainLog(h), "try add, new, rename, remove or share") {
		t.Errorf("errors:\n%s", plainLog(h))
	}
	if got := channelCommand().complete(h.m); len(got) < 5 {
		t.Errorf("completions = %v", got)
	}
}

func TestChannelFailures(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.m.Update(channelsMsg{err: errRadio, what: "JOINED  #x"})
	if !strings.Contains(h.lastLog(), "JOINED  #x  the radio is busy") {
		t.Errorf("failure: %s", h.lastLog())
	}
	h.m.ready.Device.MaxChannels = 0
	if h.m.slots() != 8 {
		t.Error("default slots")
	}
	h.m.ready.Device.MaxChannels = 3
	h.keys("n")
	h.typeLine("#full")
	if !strings.Contains(h.lastLog(), "every channel slot") {
		t.Errorf("full: %s", h.lastLog())
	}
	h.key(tea.KeyEscape)
	h.keys("?")
	if !strings.Contains(h.screen(), "add a channel") {
		t.Errorf("help:\n%s", h.screen())
	}
}

var errRadio = radioBusy{}

type radioBusy struct{}

func (radioBusy) Error() string { return "the radio is busy" }

func TestRenameChannel(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	bristol, _ := h.channel("#bristol")
	h.m.jumpToConvo(store.ChannelConvo(bristol.Idx))
	rename := func(name string) { h.keys(":"); h.typeLine("channel rename " + name) }
	rename("#bristol")
	if !strings.Contains(plainLog(h), "can't be renamed") || h.m.chats.chanStage != chanNone {
		t.Fatal("hashtag channels keep their name")
	}
	h.keys("n")
	h.typeLine("Family")
	h.key(tea.KeyEnter)
	h.until("family", func() bool { _, ok := h.channel("Family"); return ok })
	h.keys("z")
	before, _ := h.channel("Family")
	rename("Family")
	if !strings.Contains(h.screen(), "RENAME FAMILY") || h.m.chats.chanInput.text != "Family" {
		t.Fatalf("rename:\n%s", h.screen())
	}
	h.clear()
	h.typeLine("#nope")
	if !strings.Contains(h.lastLog(), "can't be empty or start with #") {
		t.Error("# refused")
	}
	h.clear()
	h.typeLine(strings.Repeat("y", 32))
	if !strings.Contains(h.lastLog(), "at most 31 bytes") {
		t.Error("too long")
	}
	h.clear()
	h.typeLine("The Family")
	h.until("renamed", func() bool { _, ok := h.channel("The Family"); return ok })
	after, _ := h.channel("The Family")
	if after.Secret != before.Secret || after.Idx != before.Idx || !strings.Contains(h.lastLog(), "RENAMED  Family → The Family") {
		t.Errorf("renamed = %+v", after)
	}
	rename("The Family")
	h.key(tea.KeyEscape)
	if h.m.chats.chanStage != chanNone {
		t.Error("esc")
	}
}

func TestShareViewSaysWhenTheNameIsEnough(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	strong := [16]byte{0x3a, 0x91, 0x0c, 0xe7, 0x55, 0x12, 0xb8, 0x6f, 0xd4, 0x29, 0x80, 0x4e, 0xf3, 0x17, 0xaa, 0x6b}
	for _, c := range []struct {
		ch        radio.Channel
		want, not string
	}{
		{radio.Channel{Name: "#bath", Secret: strong}, "Anyone with this secret", "is enough"},
		{radio.Channel{Name: "Family", Secret: meshcore.DeriveHashtagPSK("#family")}, "Anyone can work out this secret", "Anyone with this secret"},
	} {
		h.m.chats.shareCh, h.m.chats.chanStage = c.ch, chanShare
		if scr := h.screen(); !strings.Contains(scr, c.want) || strings.Contains(scr, c.not) {
			t.Errorf("%s:\n%s", c.ch.Name, scr)
		}
	}
	gen := h.m.clipGen
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if batch, ok := cmd().(tea.BatchMsg); !ok || len(batch) != 2 || h.m.clipGen != gen+1 || !strings.Contains(h.lastLog(), "cleared in 30 s") {
		t.Errorf("a copied link is cleared like the private key: %q", h.lastLog())
	}
	if cmd := h.m.deviceMsg(clipClearMsg{gen + 1}); cmd == nil || fmt.Sprint(cmd()) != fmt.Sprint(tea.SetClipboard("")()) {
		t.Error("and the clear empties it")
	}
}

func TestCopiedMessagesKeepOnlyTheirLines(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.incoming(radio.Incoming{Channel: true, ChannelIdx: 0, Text: "Bob: line one\nline\x1b]52;c;x\x07 two\r"})
	h.openConvo("Public")
	h.key(tea.KeyRight)
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil || fmt.Sprint(cmd()) != fmt.Sprint(tea.SetClipboard("line one\nline]52;c;x two")()) {
		t.Errorf("copied %v", cmd())
	}
}

func TestWiFiSaysItIsUnencrypted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if strings.Contains(plainLog(h), "UNENCRYPTED") {
		t.Fatal("only WiFi warns")
	}
	h.m.radio = tcpSim{h.s}
	h.m.Update(eventMsg{h.m.ready})
	h.m.Update(eventMsg{h.m.ready})
	if n := strings.Count(plainLog(h), "UNENCRYPTED  WiFi links aren't encrypted"); n != 1 {
		t.Errorf("warned %d times:\n%s", n, plainLog(h))
	}

	h.keys("6")
	h.until("settings", func() bool { return h.m.device.loaded })
	h.moveTo("import")
	h.key(tea.KeyEnter)
	h.clear()
	h.typeFast(strings.Repeat("ab", 64))
	h.key(tea.KeyEnter)
	if scr := h.screen(); !strings.Contains(scr, "crosses your network unencrypted") || !strings.Contains(scr, "Type import to go ahead") {
		t.Errorf("the import warns over WiFi:\n%s", scr)
	}
	h.key(tea.KeyEscape)
}

func TestAddChannelRow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	at := h.m.lastChannel()
	h.m.chats.sel = at
	h.key(tea.KeyDown)
	if !h.m.chats.onAdd || !strings.Contains(h.screen(), "+ add a channel") || !strings.Contains(h.screen(), "enter add a channel") {
		t.Fatalf("down from the last channel picks the add row:\n%s", h.screen())
	}
	h.key(tea.KeyEnter)
	if h.m.chats.chanStage != chanName {
		t.Fatal("enter on the row opens the form")
	}
	h.key(tea.KeyEscape)
	if h.m.chats.chanStage != chanNone || !h.m.chats.onAdd {
		t.Fatal("esc goes back to the row")
	}
	h.key(tea.KeyDown)
	if h.m.chats.onAdd || h.m.chats.sel != at+1 {
		t.Fatalf("down again reaches the first direct conversation, sel %d", h.m.chats.sel)
	}
	h.key(tea.KeyUp)
	h.key(tea.KeyUp)
	if h.m.chats.onAdd || h.m.chats.sel != at {
		t.Fatal("up passes back over the row to the last channel")
	}
	h.keys("/")
	if strings.Contains(h.screen(), "+ add a channel") {
		t.Error("a filter hides the row")
	}
}

func TestJoinHashtagFromAMessage(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pub, _ := h.channel("Public")
	h.m.jumpToConvo(store.ChannelConvo(pub.Idx))
	h.m.Update(eventMsg{radio.Incoming{At: h.m.now, Channel: true, ChannelIdx: pub.Idx, SentAt: h.m.now,
		Text: "Carol: is #hilltop still a thing? try #bath, issue #12 or a&#x"}})
	h.settle()
	h.key(tea.KeyRight)
	if !strings.Contains(h.screen(), "J join #bath") {
		t.Fatalf("a channel we're in, a number and an entity are passed over for #bath:\n%s", h.screen())
	}
	h.keys("J")
	h.until("joined", func() bool { _, ok := h.channel("#bath"); return ok })
	if !strings.Contains(h.lastLog(), "JOINED  #bath") {
		t.Error(h.lastLog())
	}
	h.m.chats.reading = true
	h.m.jumpToConvo(store.ChannelConvo(pub.Idx))
	h.key(tea.KeyRight)
	if strings.Contains(h.screen(), "J join") {
		t.Error("nothing left to join, so no hint")
	}
}

func TestFinderJoinsAndAddsChannels(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys(":")
	h.typeFast("#cardiff")
	if !strings.Contains(h.screen(), "join #cardiff") {
		t.Fatalf("a hashtag not joined is offered:\n%s", h.screen())
	}
	h.key(tea.KeyEnter)
	h.until("joined", func() bool { _, ok := h.channel("#cardiff"); return ok })

	h.keys(":")
	h.typeFast("#bristol")
	if strings.Contains(h.screen(), "join #bristol") {
		t.Error("a channel we're in isn't offered to join")
	}
	h.key(tea.KeyEscape)

	h.keys(":")
	h.typeFast("add a ch")
	h.key(tea.KeyEnter)
	if h.m.tab != 0 || h.m.chats.chanStage != chanName {
		t.Error("add a channel opens the form")
	}
}
