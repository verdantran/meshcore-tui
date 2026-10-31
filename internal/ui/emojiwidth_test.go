package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
)

func TestSplitEmoji(t *testing.T) {
	for in, want := range map[string]string{
		"🏊‍♂ Bob":                          "🏊  Bob",
		"👨‍💻|":                             "👨💻|",
		"👍🏽|":                              "👍  |",
		"☀️|":                              "☀|",
		"🏴\U000e0067\U000e0062\U000e007f|": "🏴|",
		"\x1b[31m🏊‍♀\x1b[0m|":              "\x1b[31m🏊 \x1b[0m|",
		"plain text, é and 日本|":            "plain text, é and 日本|",
		"🐸|":                               "🐸|",
	} {
		got := splitEmoji(in)
		if got != want {
			t.Errorf("splitEmoji(%q) = %q, want %q", in, got, want)
		}
		if ansi.WcWidth.StringWidth(got) != ansi.WcWidth.StringWidth(in) {
			t.Errorf("%q: %d cells, counted as %d", in, ansi.WcWidth.StringWidth(got), ansi.WcWidth.StringWidth(in))
		}
		if ansi.WcWidth.StringWidth(got) != ansi.GraphemeWidth.StringWidth(got) {
			t.Errorf("%q: drawn as %q, which the two ways of counting still disagree on", in, got)
		}
	}
}

func TestSplitEmojiOnScreen(t *testing.T) {
	defer setRenderWidth(renderWidth)
	setRenderWidth(ansi.WcWidth) // split mode counts a character at a time
	h := newHarness(t)
	pub, _ := h.channel("Public")
	h.m.opt.SplitEmoji = true
	h.m.jumpToConvo(store.ChannelConvo(pub.Idx))
	h.m.Update(eventMsg{radio.Incoming{At: h.m.now, Channel: true, ChannelIdx: pub.Idx, SentAt: h.m.now, Text: "Zed 🐸🏊‍♂: hello"}})
	h.settle()
	if scr := h.screen(); !strings.Contains(scr, "🏊 ") || strings.Contains(scr, "‍") {
		t.Fatalf("joined emoji are drawn as their parts:\n%s", scr)
	}
	if msg := h.m.chats.msgs[len(h.m.chats.msgs)-1]; !strings.Contains(msg.Sender, "🏊‍♂") {
		t.Errorf("the sender keeps every character: %q", msg.Sender)
	}
	h.m.Update(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeSet})
	if h.m.opt.SplitEmoji || !strings.Contains(h.screen(), "🏊‍♂") {
		t.Error("a terminal that says it counts graphemes gets them whole")
	}
}

func TestPlainLetters(t *testing.T) {
	for _, c := range []struct {
		in          string
		lone, flags bool
		cells       int
		want        string
	}{
		{"Zed\U0001F1F7|", true, false, 2, "ZedR |"},
		{"Zed\U0001F1F7|", true, false, 1, "ZedR|"},
		{"Zed\U0001F1EB\U0001F1F7|", false, true, 2, "ZedFR|"},
		{"Zed\U0001F1EB\U0001F1F7 \U0001F1F7|", false, true, 2, "ZedFR \U0001F1F7|"},
		{"Zed\U0001F1EB\U0001F1F7 \U0001F1F7|", true, false, 2, "Zed\U0001F1EB\U0001F1F7 R |"},
		{"\x1b[33mAb12\U0001F1EB\U0001F1F7\x1b[0m|", false, true, 2, "\x1b[33mAb12FR\x1b[0m|"},
		{"plain|", true, true, 2, "plain|"},
	} {
		if got := plainLetters(c.in, c.lone, c.flags, c.cells); got != c.want {
			t.Errorf("plainLetters(%q, %v, %v, %d) = %q, want %q", c.in, c.lone, c.flags, c.cells, got, c.want)
		}
	}
}

func TestFlagsOnScreen(t *testing.T) {
	defer setRenderWidth(renderWidth)
	setRenderWidth(ansi.GraphemeWidth)
	h := newHarness(t)
	pub, _ := h.channel("Public")
	h.m.opt.Flag = 1 // counted two cells, drawn as one narrow glyph
	h.m.jumpToConvo(store.ChannelConvo(pub.Idx))
	h.m.Update(eventMsg{radio.Incoming{At: h.m.now, Channel: true, ChannelIdx: pub.Idx, SentAt: h.m.now, Text: "Zed\U0001F1EB\U0001F1F7: hello \U0001F1EB\U0001F1F7"}})
	h.settle()
	if scr := h.screen(); !strings.Contains(scr, "ZedFR") || strings.Contains(scr, "\U0001F1F7") {
		t.Fatalf("a flag is drawn as its letters:\n%s", scr)
	}
	if msg := h.m.chats.msgs[len(h.m.chats.msgs)-1]; msg.Sender != "Zed\U0001F1EB\U0001F1F7" {
		t.Errorf("the sender keeps it: %q", msg.Sender)
	}
	h.m.opt.Flag = 2
	if !strings.Contains(h.screen(), "Zed\U0001F1EB\U0001F1F7") {
		t.Error("drawn as wide as it's counted, it's left alone")
	}
	h.m.opt.FlagLetters = true // as [ui] flags = "letters" asks, or tmux in front of a terminal that can't say
	if !strings.Contains(h.screen(), "ZedFR") {
		t.Error("asked for letters, flags are drawn as letters whatever the probe found")
	}
}

func TestStandInTextEmoji(t *testing.T) {
	for in, want := range map[string]string{
		"Zed\U0001F574   |":              "Zed*   |",
		"\U0001F590|":                    "*|",
		"\x1b[33mDon\U0001F574\x1b[0m |": "\x1b[33mDon*\x1b[0m |",
		"\U0001F574\ufe0f|":              "\U0001F574\ufe0f|", // asked to be an emoji, it's counted two already
		"\U0001F344|":                    "\U0001F344|",       // an emoji counted two
		"\u263a|":                        "\u263a|",           // an older symbol tmux draws in one cell
		"\U0001F1EB\U0001F1F7|":          "\U0001F1EB\U0001F1F7|",
	} {
		got := standInTextEmoji(in)
		if got != want {
			t.Errorf("standInTextEmoji(%q) = %q, want %q", in, got, want)
		}
		for _, m := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
			if m.StringWidth(got) != m.StringWidth(in) {
				t.Errorf("%q: the row's width changed, which Bubble Tea's count of it wouldn't follow", in)
			}
		}
	}
}

// Inside tmux, counting by grapheme, rows holding such an emoji come to the screen's width by every count.
func TestTextEmojiOnScreen(t *testing.T) {
	defer setRenderWidth(renderWidth)
	setRenderWidth(ansi.GraphemeWidth)
	h := newHarness(t)
	pub, _ := h.channel("Public")
	h.m.opt.TextEmoji = 2
	h.m.jumpToConvo(store.ChannelConvo(pub.Idx))
	h.m.Update(eventMsg{radio.Incoming{At: h.m.now, Channel: true, ChannelIdx: pub.Idx, SentAt: h.m.now, Text: "Zed\U0001F574: hello"}})
	h.settle()
	scr := strings.Join(screen(t, h.m), "\n")
	if !strings.Contains(scr, "Zed*") || strings.Contains(scr, "\U0001F574") {
		t.Fatalf("drawn as *:\n%s", ansi.Strip(scr))
	}
	if msg := h.m.chats.msgs[len(h.m.chats.msgs)-1]; msg.Sender != "Zed\U0001F574" {
		t.Errorf("the sender keeps it: %q", msg.Sender)
	}
	h.m.opt.TextEmoji = 1
	if !strings.Contains(h.screen(), "Zed\U0001F574") {
		t.Error("drawn as wide as it's counted, it's left alone")
	}
}

func TestPlainSymbols(t *testing.T) {
	for in, want := range map[string]string{
		"@Zed\u270c\ufe0f|":            "@Zed\u270c |",
		"\u2600\ufe0f sun|":            "\u2600  sun|",
		"\x1b[33m\u270c\ufe0f\x1b[0m|": "\x1b[33m\u270c \x1b[0m|",
		"\u270c|":                      "\u270c|",           // already plain
		"\U0001F574\ufe0f|":            "\U0001F574\ufe0f|", // a newer emoji
		"1\ufe0f\u20e3|":               "1\ufe0f\u20e3|",    // a keycap
		"\U0001F1EB\U0001F1F7|":        "\U0001F1EB\U0001F1F7|",
	} {
		got := plainSymbols(in)
		if got != want {
			t.Errorf("plainSymbols(%q) = %q, want %q", in, got, want)
		}
		if ansi.GraphemeWidth.StringWidth(got) != ansi.GraphemeWidth.StringWidth(in) {
			t.Errorf("%q: the row's width changed", in)
		}
	}
}

func TestPlainSymbolsOnScreen(t *testing.T) {
	defer setRenderWidth(renderWidth)
	setRenderWidth(ansi.GraphemeWidth)
	h := newHarness(t)
	pub, _ := h.channel("Public")
	h.m.opt.PlainSymbols = true
	h.m.jumpToConvo(store.ChannelConvo(pub.Idx))
	h.m.Update(eventMsg{radio.Incoming{At: h.m.now, Channel: true, ChannelIdx: pub.Idx, SentAt: h.m.now, Text: "Zed\u270c\ufe0f: hello"}})
	h.settle()
	if scr := strings.Join(screen(t, h.m), "\n"); !strings.Contains(scr, "Zed\u270c ") || strings.Contains(scr, "\ufe0f") {
		t.Fatalf("drawn plain, then a space:\n%s", ansi.Strip(scr))
	}
	if msg := h.m.chats.msgs[len(h.m.chats.msgs)-1]; msg.Sender != "Zed\u270c\ufe0f" {
		t.Errorf("the sender keeps it: %q", msg.Sender)
	}
}
