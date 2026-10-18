// Package untrusted makes text from outside the app (the mesh, a repeater's console, downloaded map tiles)
// safe to draw: a terminal acts on control characters, so none may reach it from someone else.
package untrusted

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxMarks is how many combining marks may stack on one character; more only smear over the rows nearby.
const maxMarks = 4

const zwj = '\u200d'

// Text drops control characters (C0 but line breaks, DEL and C1, so escape sequences can't form), the
// bidirectional controls that can make text read in a different order to how it's stored, and invisible
// characters, except the joiner inside an emoji sequence. Tabs become spaces, invalid UTF-8 becomes U+FFFD,
// and a stack of combining marks is cut to maxMarks.
func Text(s string) string { return clean(s, false) }

// Line is Text for a field drawn on one row, such as a name: line breaks become spaces too, so it can't
// split its row or start a forged one.
func Line(s string) string { return clean(s, true) }

func clean(s string, line bool) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return special(r, line) }) {
		return s
	}
	s = strings.ToValidUTF8(s, "�")
	var b strings.Builder
	b.Grow(len(s))
	var prev rune
	marks := 0
	for i, r := range s {
		switch {
		case r == '\t', line && lineBreak(r):
			r = ' '
		case r == zwj:
			if next, _ := utf8.DecodeRuneInString(s[i+utf8.RuneLen(zwj):]); !pictographic(prev) || !pictographic(next) {
				continue
			}
		case drop(r):
			continue
		}
		if unicode.Is(unicode.M, r) {
			if marks++; marks > maxMarks {
				continue
			}
		} else {
			marks = 0
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

func special(r rune, line bool) bool {
	return drop(r) || r == '\t' || r == utf8.RuneError || r == zwj || line && lineBreak(r) || unicode.Is(unicode.M, r)
}

func lineBreak(r rune) bool { return r == '\n' || r == '\u2028' || r == '\u2029' }

func drop(r rune) bool {
	switch {
	case r == '\n':
		return false
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f, r == 0x061c:
		return true
	case r == 0x200b, r == 0x200c, r == 0x2060, r == 0xfeff:
		return true
	}
	return false
}

// pictographic is roughly Extended_Pictographic, plus the emoji presentation selector that may sit
// between an emoji and its joiner.
func pictographic(r rune) bool {
	switch {
	case r >= 0x1f000 && r <= 0x1faff, r >= 0x2600 && r <= 0x27bf, r >= 0x2300 && r <= 0x23ff, r >= 0x2b00 && r <= 0x2bff:
		return true
	case r >= 0x2190 && r <= 0x21ff, r >= 0x25a0 && r <= 0x25ff, r == 0x00a9, r == 0x00ae, r == 0x203c, r == 0x2049, r == 0x2122, r == 0x2139:
		return true
	case r == 0xfe0f:
		return true
	}
	return false
}
