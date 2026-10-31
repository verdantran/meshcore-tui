package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// widthMode is how the terminal draws emoji that are more than one character: what the app counts them as.
type widthMode int

const (
	widthsUnknown   widthMode = iota // no answer: count a character at a time, as Bubble Tea does by default
	widthsPerChar                    // a character at a time
	widthsGraphemes                  // a grapheme at a time, two cells for any emoji
	widthsSplit                      // neither: draw such emoji as their parts, which both count the same
)

// widthProbes are emoji the two ways of counting disagree on: a variation selector, a joined emoji ending in a
// narrow symbol, two emoji joined, and a skin tone. Terminals and tmux versions draw each differently.
var widthProbes = []string{"☀️", "🏊‍♂", "👨‍💻", "👍🏽"}

// loneLetter is a regional indicator on its own, one of the letters two of which make a flag. Counted two
// cells as a grapheme and one a character at a time, it's drawn either way, as an emoji or as a small letter.
// flagProbe is two of them, counted two cells either way, which some terminals draw as one narrow glyph.
// textEmoji is an emoji from the newer emoji blocks that shows as text unless asked otherwise, such as 🕴.
// Both ways of counting give it one cell, and tmux among others draws it in two.
const (
	loneLetter = "\U0001F1F7"
	flagProbe  = "\U0001F1EB\U0001F1F7"
	textEmoji  = "\U0001F574"
)

// widths is what the probe found: how emoji are counted, and how wide a lone regional indicator and a flag
// are drawn (0 when the terminal didn't say).
type widths struct {
	mode       widthMode
	loneLetter int
	flag       int
	textEmoji  int
	probes     []int // how wide each of widthProbes was drawn, for --widths
}

// tty is the terminal as the width probe uses it: deadlines keep a silent terminal from holding up startup.
type tty interface {
	io.ReadWriter
	SetReadDeadline(time.Time) error
}

// measureWidths draws each probe and asks the terminal where the cursor landed. Many terminals, tmux among
// them, never answer the mode 2027 query Bubble Tea relies on to learn how they count.
func measureWidths(t tty, wait time.Duration) widths {
	if t.SetReadDeadline(time.Now().Add(wait)) != nil {
		return widths{}
	}
	defer t.SetReadDeadline(time.Time{})
	defer io.WriteString(t, "\r\x1b[K")
	perChar, graphemes := true, true
	var got []byte
	var w widths
	for _, p := range widthProbes {
		if _, err := io.WriteString(t, "\r"+p+"\x1b[6n"); err != nil {
			return widths{}
		}
		col, rest, ok := readColumn(t, got)
		if !ok {
			return widths{}
		}
		got = rest
		w.probes = append(w.probes, col-1)
		perChar = perChar && col-1 == ansi.WcWidth.StringWidth(p)
		graphemes = graphemes && col-1 == ansi.GraphemeWidth.StringWidth(p)
	}
	w.mode = widthsSplit
	switch {
	case graphemes:
		w.mode = widthsGraphemes
	case perChar:
		w.mode = widthsPerChar
	}
	for _, m := range []struct {
		probe string
		into  *int
	}{{loneLetter, &w.loneLetter}, {flagProbe, &w.flag}, {textEmoji, &w.textEmoji}} {
		if _, err := io.WriteString(t, "\r"+m.probe+"\x1b[6n"); err != nil {
			break
		}
		col, rest, ok := readColumn(t, got)
		if !ok {
			break
		}
		got, *m.into = rest, col-1
	}
	return w
}

// readColumn reads until a cursor position report, returning its column and whatever came after it.
func readColumn(t tty, got []byte) (int, []byte, bool) {
	buf := make([]byte, 64)
	for len(got) < 256 {
		if col, end, ok := cursorColumn(got); ok {
			return col, got[end:], true
		}
		n, err := t.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil && n == 0 {
			return 0, nil, false
		}
	}
	return 0, nil, false
}

var positionReport = regexp.MustCompile(`\x1b\[\d+;(\d+)R`)

// cursorColumn finds a cursor position report, ESC [ row ; col R, and gives its column counted from 1 and where
// the report ends. Keys typed around it can come in the same read.
func cursorColumn(b []byte) (int, int, bool) {
	m := positionReport.FindSubmatchIndex(b)
	if m == nil {
		return 0, 0, false
	}
	n, err := strconv.Atoi(string(b[m[2]:m[3]]))
	return n, m[1], err == nil
}

// terminalWidths runs the probe on the controlling terminal, in raw mode so the reports aren't echoed.
func terminalWidths() widths {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return widths{}
	}
	defer f.Close()
	// Fd would make reads blocking and so lose the deadline.
	conn, err := f.SyscallConn()
	if err != nil {
		return widths{}
	}
	var fd uintptr
	if conn.Control(func(p uintptr) { fd = p }) != nil {
		return widths{}
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return widths{}
	}
	defer term.Restore(fd, state)
	return measureWidths(f, 300*time.Millisecond)
}

// flagTerminals draw flags two cells wide, as the app counts them.
var flagTerminals = []string{"ghostty", "kitty", "wezterm", "iterm"}

// flagLetters decides whether flags and lone regional indicators are drawn as letters. Left to auto, they are
// where the probe finds a flag drawn at a width other than two; and inside tmux, which answers the probe for
// its own grid while the terminal outside may draw a flag as one narrow glyph, as Windows Terminal does,
// unless tmux says the attached terminal is one that draws flags.
func flagLetters(setting string, s session, w widths) bool {
	switch setting {
	case "letters":
		return true
	case "emoji":
		return false
	}
	if s.tmux {
		return !slices.ContainsFunc(flagTerminals, func(t string) bool { return strings.Contains(s.clientTerm, t) })
	}
	return w.flag != 0 && w.flag != 2
}

// widthReport says what the probe found and what the app does about it, for meshcore-tui --widths.
func widthReport(out io.Writer, s session, w widths, setting string) {
	fmt.Fprintln(out, "meshcore-tui "+version())
	if s.tmux {
		client := s.clientTerm
		if client == "" {
			client = "not known"
		}
		fmt.Fprintf(out, "inside tmux, which answers for its own grid; the terminal attached: %s\n", client)
	}
	if len(w.probes) < len(widthProbes) {
		fmt.Fprintln(out, "couldn't measure: the terminal didn't say where the cursor landed, or reads from it can't be timed out (as on macOS), so nothing is changed")
		return
	}
	row := func(what, s string, drawn int) {
		fmt.Fprintf(out, "  %-34s %-3s drawn %d, counted %d a character at a time, %d as a grapheme\n", what, s, drawn, ansi.WcWidth.StringWidth(s), ansi.GraphemeWidth.StringWidth(s))
	}
	for i, p := range widthProbes {
		row([]string{"emoji with a variation selector", "joined emoji ending in a symbol", "two joined emoji", "skin tone"}[i], p, w.probes[i])
	}
	for _, m := range []struct {
		what, s string
		drawn   int
	}{{"lone regional indicator", loneLetter, w.loneLetter}, {"flag", flagProbe, w.flag}, {"emoji that shows as text", textEmoji, w.textEmoji}} {
		if m.drawn != 0 {
			row(m.what, m.s, m.drawn)
		}
	}
	mode := map[widthMode]string{widthsGraphemes: "a grapheme at a time", widthsPerChar: "a character at a time", widthsSplit: "a character at a time, joined emoji drawn as their parts"}[w.mode]
	fmt.Fprintf(out, "counting: %s\n", mode)
	counted := ansi.WcWidth
	if w.mode == widthsGraphemes {
		counted = ansi.GraphemeWidth
	}
	yesNo := map[bool]string{true: "yes", false: "no"}
	fmt.Fprintf(out, "flags drawn as letters ([ui] flags = %q): %s\n", setting, yesNo[flagLetters(setting, s, w)])
	fmt.Fprintf(out, "room made after emoji that show as text: %s\n", yesNo[w.textEmoji > counted.StringWidth(textEmoji)])
}
