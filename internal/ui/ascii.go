package ui

import (
	"math/rand"
	"strings"

	"github.com/clipperhouse/uax29/v2/graphemes"

	"github.com/verdantran/meshcore-tui/internal/motion"
)

// asciiFor stands in for each symbol the screens draw, one cell for one cell.
var asciiFor = map[string]string{
	"·": ".", "…": "~", "•": "*", "—": "-", "–": "-", "°": "o", "≥": ">", "×": "x",
	"█": "#", "▌": "|", "▐": "|", "▀": "\"", "▄": "_", "░": ".", "▒": ":", "▓": "#",
	"▁": "_", "▂": "_", "▃": "-", "▅": "=", "▆": "=", "▇": "#", "▚": "%", "▞": "%", "▙": "%", "▟": "%",
	"→": ">", "←": "<", "↑": "^", "↓": "v", "▸": ">", "▶": ">", "›": ">", "‹": "<", "⟳": "@",
	"─": "-", "━": "-", "│": "|", "┃": "|", "╱": "/", "╲": "\\", "╳": "X", "┼": "+",
	"┌": "+", "┐": "+", "└": "+", "┘": "+", "┏": "+", "┓": "+", "┗": "+", "┛": "+",
	"✓": "v", "✕": "x", "★": "*", "✎": "%", "✍": "%", "⚠": "!", "⌂": "H",
	"◉": "@", "●": "o", "○": "o", "■": "#", "▲": "^",
}

// toASCII rewrites a frame in plain ASCII for terminals without Unicode symbols, keeping its colours.
// Braille becomes a character shaped like its dots, and anything else without a stand-in becomes ?,
// one for each cell it took, so columns still line up.
func toASCII(frame string) string {
	var b strings.Builder
	b.Grow(len(frame))
	g := graphemes.FromString(frame)
	g.AnsiEscapeSequences = true
	for g.Next() {
		s := g.Value()
		switch r := []rune(s); {
		case s[0] < 0x80:
			b.WriteString(s)
		case len(r) == 1 && r[0] >= 0x2800 && r[0] <= 0x28FF:
			b.WriteByte(brailleASCII(r[0] - 0x2800))
		default:
			if a, ok := asciiFor[s]; ok {
				b.WriteString(a)
			} else {
				b.WriteString(strings.Repeat("?", max(renderWidth.StringWidth(s), 1)))
			}
		}
	}
	return b.String()
}

// brailleASCII picks a character for a braille cell's dots by where they sit: high, low, or across.
func brailleASCII(dots rune) byte {
	n := 0
	for d := dots; d != 0; d &= d - 1 {
		n++
	}
	high, low := dots&0x1B != 0, dots&0xE4 != 0 // dots 1, 2, 4, 5 above the middle; 3, 6, 7, 8 below
	switch {
	case n == 0:
		return ' '
	case n >= 6:
		return '#'
	case high && low:
		return ':'
	case high:
		return '\''
	}
	return '.'
}

// scrambleRow swaps some of a styled row's characters for glitch glyphs, leaving its escapes and widths alone.
func scrambleRow(row string, rng *rand.Rand) string {
	glyphs := []rune(motion.Scramble)
	var b strings.Builder
	g := graphemes.FromString(row)
	g.AnsiEscapeSequences = true
	for g.Next() {
		s := g.Value()
		if s[0] != 0x1b && s != " " && renderWidth.StringWidth(s) == 1 && rng.Float64() < 0.18 {
			b.WriteRune(glyphs[rng.Intn(len(glyphs))])
			continue
		}
		b.WriteString(s)
	}
	return b.String()
}
