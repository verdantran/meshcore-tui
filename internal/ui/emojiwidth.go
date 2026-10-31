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

// loneProbe and flagProbe are a regional indicator on its own and a flag, as the width probe draws them.
const (
	loneProbe = "\U0001F1F7"
	flagProbe = "\U0001F1EB\U0001F1F7"
)

// plainLetters draws regional indicators as the capital letters they stand for, for a terminal that draws them
// at another width from the app's count: lone ones padded to the cells each was counted as, and flags as their
// two letters, which take a flag's two cells. untrusted.Skeleton reads them as those letters, so a name looks
// alike either way.
func plainLetters(frame string, lone, flags bool, loneCells int) string {
	if !strings.ContainsFunc(frame, regionalIndicator) {
		return frame
	}
	var b strings.Builder
	g := uniseg.NewGraphemes(frame)
	for g.Next() {
		rs := g.Runes()
		switch {
		case len(rs) == 1 && lone && regionalIndicator(rs[0]):
			b.WriteRune('A' + rs[0] - 0x1F1E6)
			b.WriteString(strings.Repeat(" ", max(loneCells-1, 0)))
		case len(rs) == 2 && flags && regionalIndicator(rs[0]) && regionalIndicator(rs[1]):
			b.WriteRune('A' + rs[0] - 0x1F1E6)
			b.WriteRune('A' + rs[1] - 0x1F1E6)
		default:
			b.WriteString(g.Str())
		}
	}
	return b.String()
}

func regionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

// textEmojiProbe is an emoji that shows as text by default, as the width probe draws it.
const textEmojiProbe = "\U0001F574"

// standInTextEmoji draws each emoji from the newer emoji blocks that shows as text by default, such as 🕴 and 🖐,
// as *, for a terminal that draws them two cells wide where the app counted one. Inside tmux the gap can't be
// closed any other way: Bubble Tea updates rows at the cells its own count gives, so padding the first draw
// still leaves later updates a cell out, and with the emoji selector Windows Terminal draws over the cell
// before. A * is one cell everywhere, so nothing moves. untrusted.Skeleton reads them as * too, so names
// that differ only in such an emoji look alike there, and are matched as alike.
func standInTextEmoji(frame string) string {
	if !strings.ContainsFunc(frame, textDefaultEmoji) {
		return frame
	}
	var b strings.Builder
	g := uniseg.NewGraphemes(frame)
	for g.Next() {
		if rs := g.Runes(); len(rs) == 1 && textDefaultEmoji(rs[0]) {
			b.WriteByte('*')
			continue
		}
		b.WriteString(g.Str())
	}
	return b.String()
}

// textDefaultEmoji is an emoji from the newer blocks that both ways of counting give one cell.
func textDefaultEmoji(r rune) bool {
	return r >= 0x1F000 && !regionalIndicator(r) && ansi.WcWidth.StringWidth(string(r)) == 1
}
