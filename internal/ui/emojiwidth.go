package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// splitEmoji draws each joined or skin-toned emoji in a frame as its parts, for a terminal that draws them
// neither way the app can count. The parts are what a terminal that doesn't join emoji shows, which
// untrusted.Skeleton already treats as the same name: the joiner, variation selectors, skin tones and a
// flag's tags go, and so does a narrow symbol such as ♂ after an emoji, since terminals draw those at
// different widths. Each is padded to the cells it was counted as, a character at a time, so rows line up.
// Only what's drawn changes: names, channels and messages keep every character.
func splitEmoji(frame string) string {
	if !strings.ContainsFunc(frame, joinsEmoji) {
		return frame
	}
	var b strings.Builder
	g := uniseg.NewGraphemes(frame)
	for g.Next() {
		c := g.Str()
		if !strings.ContainsFunc(c, joinsEmoji) {
			b.WriteString(c)
			continue
		}
		var parts strings.Builder
		wide := false
		for _, r := range c {
			w := ansi.WcWidth.StringWidth(string(r))
			if joinsEmoji(r) || wide && w < 2 && emojiPart(r) {
				continue
			}
			parts.WriteRune(r)
			wide = w == 2
		}
		b.WriteString(parts.String())
		b.WriteString(strings.Repeat(" ", max(ansi.WcWidth.StringWidth(c)-ansi.WcWidth.StringWidth(parts.String()), 0)))
	}
	return b.String()
}

// joinsEmoji is a character that makes one emoji of several, or changes how one looks.
func joinsEmoji(r rune) bool {
	return r == '‍' || r == '️' || r >= 0x1f3fb && r <= 0x1f3ff || r >= 0xe0020 && r <= 0xe007f
}

// emojiPart is a symbol that can be part of an emoji: ♂, ♀, ⚕, ❤ and the like.
func emojiPart(r rune) bool { return r >= 0x2190 && r <= 0x2bff }
