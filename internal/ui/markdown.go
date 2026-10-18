package ui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

// emph is how a byte of message text is drawn, from the markers people type around words.
type emph uint8

const (
	emBold emph = 1 << iota
	emItalic
	emStrike
	emCode
)

var emphMarkers = []struct {
	ch byte
	em emph
}{{'`', emCode}, {'*', emBold}, {'_', emItalic}, {'~', emStrike}}

// wordByte is part of a word, so a marker beside it is punctuation rather than emphasis: snake_case,
// 2*3*4. Anything outside ASCII counts, so letters in every script do.
func wordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b >= 0x80
}

// emphasis finds *bold*, _italic_, ~struck~ and `code` as the chat apps write them, returning the text with
// the markers taken out and how each byte of what's left is drawn. Mentions are left alone, and nothing is
// emphasised inside code.
func emphasis(text string) (string, []emph) {
	n := len(text)
	cut, marks, locked := make([]bool, n), make([]emph, n), make([]bool, n)
	for _, mn := range chat.FindMentions(text) {
		for i := mn.Start; i < mn.End; i++ {
			locked[i] = true
		}
	}
	found := false
	for _, mk := range emphMarkers {
		opens := func(i int) bool {
			return text[i] == mk.ch && !locked[i] && (i == 0 || !wordByte(text[i-1])) && i+1 < n && text[i+1] != ' ' && text[i+1] != mk.ch
		}
		closes := func(j int) bool {
			return text[j] == mk.ch && !locked[j] && text[j-1] != ' ' && (j+1 == n || !wordByte(text[j+1]))
		}
		for i := 0; i < n; i++ {
			if !opens(i) {
				continue
			}
			j := i + 2
			for j < n && text[j] != '\n' && !locked[j] && !closes(j) {
				j++
			}
			if j >= n || !closes(j) {
				continue
			}
			cut[i], cut[j], found = true, true, true
			if i > 0 && j+1 < n && text[i-1] == mk.ch && text[j+1] == mk.ch {
				cut[i-1], cut[j+1] = true, true // **bold**, as Markdown writes it
			}
			for k := i + 1; k < j; k++ {
				marks[k] |= mk.em
				locked[k] = locked[k] || mk.em == emCode
			}
			i = j
		}
	}
	if !found {
		return text, nil
	}
	var b strings.Builder
	var out []emph
	for i := range n {
		if !cut[i] {
			b.WriteByte(text[i])
			out = append(out, marks[i])
		}
	}
	return b.String(), out
}

// marksIn is marks[from:to], or nil when there are none.
func marksIn(marks []emph, from, to int) []emph {
	if marks == nil {
		return nil
	}
	return marks[from:to]
}

// emphStyle is plain text's style with a byte's emphasis laid on it.
func emphStyle(th theme.Theme, base theme.RGB, e emph) lipgloss.Style {
	st := th.Fg(base, false)
	if e&emCode != 0 {
		return th.Block(th.Text, th.Raised)
	}
	if e&emBold != 0 {
		st = th.Fg(base, true)
	}
	return st.Italic(e&emItalic != 0).Strikethrough(e&emStrike != 0)
}

// emphRender draws a run of text with its emphasis, a style change at a time.
func emphRender(th theme.Theme, base theme.RGB, s string, marks []emph) string {
	if len(marks) == 0 {
		return th.Fg(base, false).Render(s)
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		j := i + 1
		for j < len(s) && marks[j] == marks[i] {
			j++
		}
		b.WriteString(emphStyle(th, base, marks[i]).Render(s[i:j]))
		i = j
	}
	return b.String()
}

// wrapEmph wraps text already rid of its markers, keeping each line's share of the emphasis.
func wrapEmph(text string, marks []emph, w int, wrap func(string, int) string) ([]string, [][]emph) {
	lines := strings.Split(wrap(text, w), "\n")
	if marks == nil {
		return lines, make([][]emph, len(lines))
	}
	out := make([][]emph, len(lines))
	pos := 0
	for i, l := range lines {
		at := strings.Index(text[pos:], l)
		if at < 0 {
			continue // a line the wrap changed; it's drawn plain
		}
		at += pos
		out[i] = marks[at : at+len(l)]
		pos = at + len(l)
	}
	return lines, out
}
