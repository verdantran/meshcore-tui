package ui

import (
	"strings"
	"testing"
)

func TestEmphasis(t *testing.T) {
	type want struct {
		clean string
		at    int
		e     emph
	}
	for in, w := range map[string]want{
		"*test* done":       {"test done", 0, emBold},
		"say _hello_":       {"say hello", 4, emItalic},
		"~gone~":            {"gone", 0, emStrike},
		"run `ls -l` now":   {"run ls -l now", 4, emCode},
		"**bold**":          {"bold", 0, emBold},
		"*_both_*":          {"both", 0, emBold | emItalic},
		"`*not bold*`":      {"*not bold*", 1, emCode},
		"snake_case_name":   {"snake_case_name", 0, 0},
		"2*3*4":             {"2*3*4", 0, 0},
		"a * b * c":         {"a * b * c", 0, 0},
		"@[_x_] hi":         {"@[_x_] hi", 3, 0},
		"*unclosed":         {"*unclosed", 0, 0},
		"line *one\ntwo* x": {"line *one\ntwo* x", 0, 0},
	} {
		clean, marks := emphasis(in)
		if clean != w.clean {
			t.Errorf("%q: text %q, want %q", in, clean, w.clean)
			continue
		}
		var got emph
		if marks != nil {
			got = marks[w.at]
		}
		if got != w.e {
			t.Errorf("%q: byte %d is %v, want %v", in, w.at, got, w.e)
		}
	}
}

func TestEmphasisInChats(t *testing.T) {
	h := newHarness(t)
	h.openConvo("Public")
	h.keys("i")
	h.keys("this is *really* _quite_ good")
	h.typeLine("")
	h.until("shown", func() bool { return strings.Contains(h.screen(), "this is really quite good") })
	last := h.m.chats.msgs[len(h.m.chats.msgs)-1]
	if rows := strings.Join(h.m.messageLines(last, 80), "\n"); !strings.Contains(rows, "\x1b[3;") {
		t.Errorf("_quite_ is italic: %q", rows)
	}
}
