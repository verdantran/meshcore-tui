package ui

import (
	"encoding/hex"
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

	// A private channel: a name, then its secret.
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
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	if cmd == nil || !strings.Contains(h.lastLog(), "COPIED  link for Club") {
		t.Error("c copies")
	}
	h.keys("z")

	// Joining by link, and refusing a duplicate.
	link := chat.ChannelLink(radio.Channel{Name: "Walkers", Secret: [16]byte{4, 4}})
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
	h := newHarness(t)
	pub, _ := h.channel("Public")
	h.m.jumpToConvo(store.ChannelConvo(pub.Idx))
	h.keys("s")
	if !strings.Contains(h.screen(), "its secret is no secret") {
		t.Fatalf("public share:\n%s", h.screen())
	}
	h.keys("q") // any key closes, without quitting
	if h.m.chats.chanStage != chanNone {
		t.Fatal("closed")
	}
	bristol, _ := h.channel("#bristol")
	h.m.jumpToConvo(store.ChannelConvo(bristol.Idx))
	h.keys("s")
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
	h.keys("s")
	if h.m.chats.chanStage != chanNone {
		t.Error("not a channel")
	}
}

func TestChannelCommand(t *testing.T) {
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
	if !strings.Contains(plainLog(h), "no channel called") || !strings.Contains(plainLog(h), "try add, new, remove or share") {
		t.Errorf("errors:\n%s", plainLog(h))
	}
	if got := channelCommand().complete(h.m); len(got) < 5 {
		t.Errorf("completions = %v", got)
	}
}

func TestChannelFailures(t *testing.T) {
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
	h := newHarness(t)
	bristol, _ := h.channel("#bristol")
	h.m.jumpToConvo(store.ChannelConvo(bristol.Idx))
	h.keys("r")
	if !strings.Contains(plainLog(h), "can't be renamed") || h.m.chats.chanStage != chanNone {
		t.Fatal("hashtag channels keep their name")
	}
	h.keys("n")
	h.typeLine("Family")
	h.key(tea.KeyEnter)
	h.until("family", func() bool { _, ok := h.channel("Family"); return ok })
	h.keys("z")
	before, _ := h.channel("Family")
	h.keys("r")
	if !strings.Contains(h.screen(), "RENAME FAMILY") || h.m.chats.chanInput != "Family" {
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
	h.keys("r")
	h.key(tea.KeyEscape)
	if h.m.chats.chanStage != chanNone {
		t.Error("esc")
	}
}
