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
	h.m.Update(eventMsg{radio.Incoming{At: h.m.now, Channel: true, ChannelIdx: pub.Idx, SentAt: h.m.now, Text: "LVNC 🐸🏊‍♂: hello"}})
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

func TestWidenTextEmoji(t *testing.T) {
	for in, want := range map[string]string{
		"The Don\U0001F574   |":            "The Don\U0001F574  |",
		"\U0001F590     |":                 "\U0001F590    |",
		"\x1b[33mDon\U0001F574\x1b[0m   |": "\x1b[33mDon\U0001F574\x1b[0m  |",
		"\U0001F574 Bob  |":                "\U0001F574 Bob  |", // one space after it, as in a name: left alone
		"\U0001F574Bob  |":                 "\U0001F574Bob  |",
		"\U0001F574\ufe0f   |":             "\U0001F574\ufe0f   |", // counted two already
		"\U0001F344   |":                   "\U0001F344   |",       // an emoji counted two
		"\u263a   |":                       "\u263a   |",           // an old symbol tmux draws in one cell
	} {
		if got := widenTextEmoji(in); got != want {
			t.Errorf("widenTextEmoji(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTextEmojiOnScreen(t *testing.T) {
	h := newHarness(t)
	pub, _ := h.channel("Public")
	h.m.opt.TextEmoji = 2
	h.m.jumpToConvo(store.ChannelConvo(pub.Idx))
	h.m.Update(eventMsg{radio.Incoming{At: h.m.now, Channel: true, ChannelIdx: pub.Idx, SentAt: h.m.now, Text: "Zed\U0001F574: \U0001F590"}})
	h.settle()
	found := false
	for _, l := range strings.Split(h.m.View().Content, "\n") {
		if strings.Contains(l, "\U0001F574") || strings.Contains(l, "\U0001F590") {
			found = true
			if got := ansi.WcWidth.StringWidth(l) + strings.Count(l, "\U0001F574") + strings.Count(l, "\U0001F590"); got != h.m.w {
				t.Errorf("drawn two cells wide, the row is %d cells, not %d: %q", got, h.m.w, ansi.Strip(l))
			}
		}
	}
	if !found {
		t.Error("the message isn't on screen")
	}
}
