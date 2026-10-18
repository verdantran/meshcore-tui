package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestShortcodeAt(t *testing.T) {
	for before, want := range map[string]string{":jo": "jo", "hi :jo": "jo", ":j": "", "10:30": "", "http://x": "", "hi:jo": "", "a :+1": "+1", "": ""} {
		_, got, ok := shortcodeAt(before)
		if got != want || ok != (want != "") {
			t.Errorf("shortcodeAt(%q) = %q %v", before, got, ok)
		}
	}
	m := emojiMatches("jo")
	if len(m) == 0 || m[0].name != "joy" {
		t.Fatalf("joy comes first for jo: %+v", m[:min(len(m), 3)])
	}
	seen := map[string]bool{}
	for _, sc := range emojiMatches("thumbs") {
		if seen[sc.emoji] {
			t.Errorf("%s offered twice", sc.emoji)
		}
		seen[sc.emoji] = true
	}
	if len(emojiMatches("tears")) == 0 {
		t.Error("a word inside the name matches too")
	}
}

func TestEmojiInTheComposeBox(t *testing.T) {
	h := newHarness(t)
	h.openConvo("Public")
	h.keys("i")
	h.keys("ha :joy:")
	if h.m.chats.input.text != "ha 😂" {
		t.Fatalf("a finished shortcode becomes its emoji: %q", h.m.chats.input.text)
	}
	h.keys(" :jo")
	if h.m.chats.emoji == nil || !strings.Contains(h.screen(), ":joy:") || !strings.Contains(h.screen(), "tab or enter insert") {
		t.Fatalf("typing a shortcode offers emoji:\n%s", h.screen())
	}
	h.key(tea.KeyDown)
	second := h.m.chats.emoji.matches[1].emoji
	h.key(tea.KeyEnter)
	if h.m.chats.input.text != "ha 😂 "+second+" " || !h.m.chats.writing {
		t.Fatalf("enter inserts the chosen one, and doesn't send: %q", h.m.chats.input.text)
	}
	h.keys(":sm")
	h.key(tea.KeyEscape)
	if h.m.chats.emoji != nil || !h.m.chats.writing {
		t.Fatal("esc closes the list, still writing")
	}
	h.keys("i")
	if h.m.chats.emoji != nil {
		t.Error("it stays closed for that shortcode")
	}
	h.key(tea.KeyEscape)
	if h.m.chats.writing {
		t.Error("esc again stops writing")
	}
}
