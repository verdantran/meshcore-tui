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
