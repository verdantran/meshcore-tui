package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

func TestPasteAndEmoji(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openConvo("Public")
	c := &h.m.chats

	// Pasting with nowhere to type starts a message; line breaks become spaces, so it isn't sent.
	_, cmd := h.m.Update(tea.PasteMsg{Content: "hello\nthere q"})
	h.start(cmd)
	h.settle()
	if !c.writing || c.input.text != "hello there q" || h.m.tab != 0 {
		t.Fatalf("pasted = %q, writing %v", c.input.text, c.writing)
	}

	// An emoji made of several characters, typed as one key, goes in whole; backspace takes it out whole.
	for _, emoji := range []string{"❤️", "👍🏽", "🇬🇧", "👨‍👩‍👧"} {
		h.m.Update(tea.KeyPressMsg{Text: emoji})
		if !strings.HasSuffix(c.input.text, emoji) {
			t.Errorf("%s typed: %q", emoji, c.input.text)
		}
		h.key(tea.KeyBackspace)
		if c.input.text != "hello there q" {
			t.Errorf("backspace after %s: %q", emoji, c.input.text)
		}
	}
	h.key(tea.KeyEscape)

	// Outside a text field a paste does nothing, rather than run its letters as commands.
	h.keys("6")
	_, cmd = h.m.Update(tea.PasteMsg{Content: "q"})
	h.start(cmd)
	if h.m.quitting || !strings.Contains(h.lastLog(), "nowhere to type") {
		t.Error("a paste is never taken as keys")
	}

	// It goes into other fields too: the palette, here.
	h.keys(":")
	h.m.Update(tea.PasteMsg{Content: "theme ice"})
	if h.m.pal.input.text != "theme ice" {
		t.Errorf("palette = %q", h.m.pal.input.text)
	}
	h.key(tea.KeyEscape)
	if dropLast("") != "" || dropLast("é") != "" || dropLast("ab") != "a" {
		t.Error("dropLast")
	}
}

func TestMentionsAndColours(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, typed string
		ok          bool
	}{{"🛰 Satellite", "sat", true}, {"🛰 Satellite", "🛰", true}, {"Big Bob", "bob", true}, {"Bob", "al", false}, {"🐝Bee", "be", true}} {
		if nameMatches(c.name, c.typed) != c.ok {
			t.Errorf("%q from %q", c.name, c.typed)
		}
	}
	h := newHarness(t)
	h.incoming(radio.Incoming{Channel: true, ChannelIdx: 0, Hops: 1, Text: "🛰 Satellite: overhead now"})
	h.incoming(radio.Incoming{Channel: true, ChannelIdx: 0, Hops: 1, Text: "Bob: hi"})
	h.openConvo("Public")
	h.key(tea.KeyEnter)
	h.keys("@sat")
	h.key(tea.KeyTab)
	if h.m.chats.input.text != "@[🛰 Satellite] " {
		t.Errorf("mention = %q", h.m.chats.input.text)
	}
	h.key(tea.KeyEscape)

	if h.m.nameColour("Bob") != h.m.nameColour("bob") {
		t.Error("the same person, the same colour")
	}
	seen := map[string]bool{}
	for _, n := range []string{"Alice", "Bob", "Carol", "Dan", "Eve", "Frank", "Grace"} {
		seen[strings.TrimSpace(h.m.th.Fg(h.m.nameColour(n), false).Render("x"))] = true
	}
	if len(seen) < 4 {
		t.Errorf("people get different colours: %d", len(seen))
	}
}

func TestPickedRowAndCopy(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.hear(channelPacket(t, h, "Public", "Bob", "four hops", []byte{0x77, 0x5F, 0xA3, 0x44}), 3)
	h.incoming(radio.Incoming{Channel: true, ChannelIdx: 0, Hops: 4, Text: "Bob: four hops"})
	h.openConvo("Public")
	if !strings.Contains(h.screen(), "4 hops via 77…44") {
		t.Errorf("a long path is shortened:\n%s", h.screen())
	}
	h.key(tea.KeyRight)
	r := h.m.th.Raised
	bg := fmt.Sprintf("48;2;%d;%d;%d", r.R, r.G, r.B)
	rows := 0
	for _, l := range strings.Split(h.m.View().Content, "\n") {
		if strings.Contains(l, bg) && strings.Contains(l, "▶") {
			rows++
		}
	}
	if rows < 2 {
		t.Errorf("the picked message's rows all get the background: %d", rows)
	}
	if !strings.Contains(h.screen(), "y copy") {
		t.Error("y is listed")
	}
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil || !strings.Contains(h.lastLog(), "COPIED  Bob's message") {
		t.Error("y copies the message")
	}
	h.keys("4")
	_, cmd = h.m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil || !strings.Contains(h.lastLog(), "packet's bytes") {
		t.Error("y copies a packet")
	}
	if pathText([]byte{0x77, 0x5F}, 1) != "77·5F" || pathText(nil, 1) != "" {
		t.Error("short paths are shown whole")
	}
}

func TestLineEditCursor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openConvo("Public")
	c := &h.m.chats
	h.key(tea.KeyEnter)
	h.keys("helo")
	h.key(tea.KeyLeft)
	h.keys("l")
	if c.input.text != "hello" {
		t.Fatalf("insert at the cursor: %q", c.input.text)
	}
	h.key(tea.KeyHome)
	h.keys(">")
	h.key(tea.KeyEnd)
	h.keys("!")
	if c.input.text != ">hello!" {
		t.Fatalf("home and end: %q", c.input.text)
	}
	h.key(tea.KeyLeft)
	h.key(tea.KeyBackspace)
	h.key(tea.KeyDelete)
	if c.input.text != ">hell" {
		t.Errorf("backspace and delete: %q", c.input.text)
	}

	// A completion mid-line replaces only the @name before the cursor.
	c.input = newLine("hi @Car there")
	c.input.pos = len("hi @Car")
	c.mention = nil
	h.key(tea.KeyTab)
	if c.input.text != "hi @[Carol] there" || c.input.before() != "hi @[Carol]" {
		t.Errorf("mid-line mention: %q", c.input.text)
	}

	e := newLine("a👨‍👩‍👧b")
	e.key("left")
	e.key("left")
	if e.before() != "a" {
		t.Errorf("an emoji is one step: %q", e.before())
	}
	e.key("delete")
	if e.text != "ab" {
		t.Errorf("and one delete: %q", e.text)
	}
	e = newLine("one two  three")
	e.key("alt+left")
	if e.before() != "one two  " {
		t.Errorf("word left: %q", e.before())
	}
	e.key("alt+backspace")
	if e.text != "one three" {
		t.Errorf("word delete: %q", e.text)
	}
	e.key("ctrl+u")
	e.key("ctrl+e")
	e.key("alt+left")
	e.key("ctrl+k")
	if e.text != "" {
		t.Errorf("kill to the start, then the end: %q", e.text)
	}

	// A long line scrolls to keep the cursor in view, at either end.
	long := newLine(strings.Repeat("abcdefghij", 10))
	if got := ansi.Strip(long.render(h.m.th, 20, false)); visibleWidth(got) > 20 || !strings.HasPrefix(got, "…") {
		t.Errorf("end of a long line: %q", got)
	}
	long.key("home")
	if got := ansi.Strip(long.render(h.m.th, 20, false)); visibleWidth(got) > 20 || !strings.HasPrefix(got, "abc") {
		t.Errorf("start of a long line: %q", got)
	}
	if got := ansi.Strip(newLine("secret").render(h.m.th, 20, true)); strings.Contains(got, "secret") {
		t.Errorf("masked: %q", got)
	}
}

// Scrolled text fills the field, give or take a wide character that won't fit, whatever the mix of widths.
func TestScrolledFieldFillsItsWidth(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("東a", 20)
	for pos := 0; pos < len(text); pos++ {
		e := lineEdit{text: text, pos: pos}
		// A wide character that won't fit can leave one cell spare, but no more.
		if got := visibleWidth(e.render(theme.Neon, 12, false)); got < 11 {
			t.Errorf("cursor at byte %d: %d cells, want 11 or 12", pos, got)
		}
	}
}
