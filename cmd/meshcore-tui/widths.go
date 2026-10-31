package main

import (
	"io"
	"os"
	"regexp"
	"strconv"
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

// tty is the terminal as the width probe uses it: deadlines keep a silent terminal from holding up startup.
type tty interface {
	io.ReadWriter
	SetReadDeadline(time.Time) error
}

// measureWidths draws each probe and asks the terminal where the cursor landed. Many terminals, tmux among
// them, never answer the mode 2027 query Bubble Tea relies on to learn how they count.
func measureWidths(t tty, wait time.Duration) widthMode {
	if t.SetReadDeadline(time.Now().Add(wait)) != nil {
		return widthsUnknown
	}
	defer t.SetReadDeadline(time.Time{})
	defer io.WriteString(t, "\r\x1b[K")
	perChar, graphemes := true, true
	var got []byte
	for _, p := range widthProbes {
		if _, err := io.WriteString(t, "\r"+p+"\x1b[6n"); err != nil {
			return widthsUnknown
		}
		col, rest, ok := readColumn(t, got)
		if !ok {
			return widthsUnknown
		}
		got = rest
		perChar = perChar && col-1 == ansi.WcWidth.StringWidth(p)
		graphemes = graphemes && col-1 == ansi.GraphemeWidth.StringWidth(p)
	}
	switch {
	case graphemes:
		return widthsGraphemes
	case perChar:
		return widthsPerChar
	}
	return widthsSplit
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
func terminalWidths() widthMode {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return widthsUnknown
	}
	defer f.Close()
	// Fd would make reads blocking and so lose the deadline.
	conn, err := f.SyscallConn()
	if err != nil {
		return widthsUnknown
	}
	var fd uintptr
	if conn.Control(func(p uintptr) { fd = p }) != nil {
		return widthsUnknown
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return widthsUnknown
	}
	defer term.Restore(fd, state)
	return measureWidths(f, 300*time.Millisecond)
}
