package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeTTY answers the probe with reply, or with nothing if reply is empty.
type fakeTTY struct {
	written    bytes.Buffer
	reply      string
	noDeadline bool
}

func (f *fakeTTY) Write(b []byte) (int, error) { return f.written.Write(b) }

func (f *fakeTTY) Read(b []byte) (int, error) {
	if f.reply == "" {
		return 0, os.ErrDeadlineExceeded
	}
	n := copy(b, f.reply)
	f.reply = f.reply[n:]
	return n, nil
}

func (f *fakeTTY) SetReadDeadline(time.Time) error {
	if f.noDeadline {
		return errors.New("not pollable")
	}
	return nil
}

func TestMeasureWidths(t *testing.T) {
	report := func(cols ...int) string {
		var b strings.Builder
		for _, c := range cols {
			fmt.Fprintf(&b, "\x1b[12;%dR", c)
		}
		return b.String()
	}
	for _, c := range []struct {
		name  string
		reply string
		want  widths
	}{
		{"a grapheme at a time, a lone letter narrow, as tmux 3.8", report(3, 3, 3, 3, 2, 3, 3), widths{widthsGraphemes, 1, 2, 2}},
		{"a character at a time", report(2, 4, 5, 5, 2, 3, 2), widths{widthsPerChar, 1, 2, 1}},
		{"neither: joined, but the symbol drawn wide", report(3, 5, 3, 3, 3, 3, 3), widths{widthsSplit, 2, 2, 2}},
		{"neither: the sun one cell, the rest joined", report(2, 3, 3, 3, 2, 3, 2), widths{widthsSplit, 1, 2, 1}},
		{"a flag drawn as one narrow glyph", report(3, 3, 3, 3, 2, 2, 3), widths{widthsGraphemes, 1, 1, 2}},
		{"a key typed first", "q\x1b[A" + report(3, 3, 3, 3, 3, 3, 2), widths{widthsGraphemes, 2, 2, 1}},
		{"no answer for the text emoji", report(3, 3, 3, 3, 2, 3), widths{widthsGraphemes, 1, 2, 0}},
		{"no answer for the flag", report(3, 3, 3, 3, 2), widths{widthsGraphemes, 1, 0, 0}},
		{"no answer for any of those", report(3, 3, 3, 3), widths{widthsGraphemes, 0, 0, 0}},
		{"silent", "", widths{}},
		{"stops answering", report(3, 3), widths{}},
	} {
		f := &fakeTTY{reply: c.reply}
		if got := measureWidths(f, time.Second); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if w := f.written.String(); !strings.Contains(w, widthProbes[0]+"\x1b[6n") || !strings.HasSuffix(w, "\r\x1b[K") {
			t.Errorf("%s: wrote %q, want the probes then the line cleared", c.name, w)
		}
	}
}

func TestMeasureWidthsNeedsADeadline(t *testing.T) {
	f := &fakeTTY{reply: "\x1b[1;3R", noDeadline: true}
	if measureWidths(f, time.Second) != (widths{}) || f.written.Len() > 0 {
		t.Errorf("probed a terminal it couldn't stop waiting for: wrote %q", f.written.String())
	}
}

func TestCursorColumn(t *testing.T) {
	for in, want := range map[string]int{"\x1b[5;17R": 17, "x\x1b[1;1Ry": 1} {
		if got, _, ok := cursorColumn([]byte(in)); !ok || got != want {
			t.Errorf("%q: got %d %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "\x1b[5;17", "\x1b[A", "\x1b[;R"} {
		if _, _, ok := cursorColumn([]byte(in)); ok {
			t.Errorf("%q: found a report in it", in)
		}
	}
}

func TestFlagLetters(t *testing.T) {
	ghosttyInTmux := session{tmux: true, clientTerm: "ghostty 1.2.3 xterm-ghostty"}
	windowsInTmux := session{tmux: true, remote: true, clientTerm: "xterm-256color"}
	for _, c := range []struct {
		name    string
		setting string
		s       session
		w       widths
		want    bool
	}{
		{"Windows Terminal through tmux: tmux can't say, so letters", "auto", windowsInTmux, widths{flag: 2}, true},
		{"Ghostty through tmux draws flags", "auto", ghosttyInTmux, widths{flag: 2}, false},
		{"kitty through tmux, known by its TERM", "", session{tmux: true, clientTerm: "xterm-kitty"}, widths{flag: 2}, false},
		{"no client known through tmux", "auto", session{tmux: true}, widths{}, true},
		{"Ghostty itself, measured", "auto", session{}, widths{flag: 2}, false},
		{"a terminal drawing a flag in one cell, measured", "auto", session{}, widths{flag: 1}, true},
		{"a silent terminal", "auto", session{}, widths{}, false},
		{"letters asked for", "letters", ghosttyInTmux, widths{flag: 2}, true},
		{"emoji asked for", "emoji", windowsInTmux, widths{flag: 1}, false},
	} {
		if got := flagLetters(c.setting, c.s, c.w); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
