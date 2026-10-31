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
		{"a grapheme at a time, a lone letter narrow, as tmux 3.8", report(3, 3, 3, 3, 2), widths{widthsGraphemes, 1}},
		{"a character at a time", report(2, 4, 5, 5, 2), widths{widthsPerChar, 1}},
		{"neither: joined, but the symbol drawn wide", report(3, 5, 3, 3, 3), widths{widthsSplit, 2}},
		{"neither: the sun one cell, the rest joined", report(2, 3, 3, 3, 2), widths{widthsSplit, 1}},
		{"a key typed first", "q\x1b[A" + report(3, 3, 3, 3, 3), widths{widthsGraphemes, 2}},
		{"no answer for the lone letter", report(3, 3, 3, 3), widths{widthsGraphemes, 0}},
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
