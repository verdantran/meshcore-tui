package main

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/charmbracelet/x/term"
)

// wideProbe is an emoji drawn one cell wide by wcwidth and two by grapheme: the sun and VS16.
const wideProbe = "☀️"

// tty is the terminal as the width probe uses it: deadlines keep a silent terminal from holding up startup.
type tty interface {
	io.ReadWriter
	SetReadDeadline(time.Time) error
}

// drawsGraphemes asks the terminal where the cursor lands after the probe. tmux lays text out a grapheme
// at a time but never answers the mode 2027 query Bubble Tea relies on to know that.
func drawsGraphemes(t tty, wait time.Duration) bool {
	if t.SetReadDeadline(time.Now().Add(wait)) != nil {
		return false
	}
	defer t.SetReadDeadline(time.Time{})
	if _, err := io.WriteString(t, "\r"+wideProbe+"\x1b[6n"); err != nil {
		return false
	}
	defer io.WriteString(t, "\r\x1b[K")
	var got []byte
	buf := make([]byte, 64)
	for len(got) < 256 {
		n, err := t.Read(buf)
		got = append(got, buf[:n]...)
		if col, ok := cursorColumn(got); ok {
			return col == 3
		}
		if err != nil {
			return false
		}
	}
	return false
}

var positionReport = regexp.MustCompile(`\x1b\[\d+;(\d+)R`)

// cursorColumn finds a cursor position report, ESC [ row ; col R, and gives its column counted from 1.
// Keys typed around it can come in the same read.
func cursorColumn(b []byte) (int, bool) {
	m := positionReport.FindSubmatch(b)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(string(m[1]))
	return n, err == nil
}

// terminalDrawsGraphemes runs the probe on the controlling terminal, in raw mode so the report isn't echoed.
func terminalDrawsGraphemes() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	// Fd would make reads blocking and so lose the deadline.
	conn, err := f.SyscallConn()
	if err != nil {
		return false
	}
	var fd uintptr
	if conn.Control(func(p uintptr) { fd = p }) != nil {
		return false
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return false
	}
	defer term.Restore(fd, state)
	return drawsGraphemes(f, 300*time.Millisecond)
}
