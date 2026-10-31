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
		in      string
		counted int
		want    string
	}{
		{"FR14-HV3\U0001F1F7|", 2, "FR14-HV3R |"},
		{"FR14-HV3\U0001F1F7|", 1, "FR14-HV3R|"},
		{"\U0001F1EB\U0001F1F7 flag|", 2, "\U0001F1EB\U0001F1F7 flag|"},
		{"\x1b[33mIN98UV\U0001F1EA\x1b[0m|", 2, "\x1b[33mIN98UVE \x1b[0m|"},
		{"plain|", 2, "plain|"},
	} {
		if got := plainLetters(c.in, c.counted); got != c.want {
			t.Errorf("plainLetters(%q, %d) = %q, want %q", c.in, c.counted, got, c.want)
		}
	}
}

func TestLoneLettersOnScreen(t *testing.T) {
	defer setRenderWidth(renderWidth)
	setRenderWidth(ansi.GraphemeWidth) // counted two cells, drawn one, as tmux 3.8 does
	h := newHarness(t)
	pub, _ := h.channel("Public")
	h.m.opt.LoneLetter = 1
	h.m.jumpToConvo(store.ChannelConvo(pub.Idx))
	h.m.Update(eventMsg{radio.Incoming{At: h.m.now, Channel: true, ChannelIdx: pub.Idx, SentAt: h.m.now, Text: "FR14-HV3\U0001F1F7: 73 de Falaise"}})
	h.settle()
	if scr := h.screen(); !strings.Contains(scr, "FR14-HV3R ") || strings.Contains(scr, "\U0001F1F7") {
		t.Fatalf("a lone regional indicator is drawn as its letter:\n%s", scr)
	}
	if msg := h.m.chats.msgs[len(h.m.chats.msgs)-1]; msg.Sender != "FR14-HV3\U0001F1F7" {
		t.Errorf("the sender keeps it: %q", msg.Sender)
	}
	h.m.opt.LoneLetter = 2
	if !strings.Contains(h.screen(), "FR14-HV3\U0001F1F7") {
		t.Error("drawn as wide as it's counted, it's left alone")
	}
}
