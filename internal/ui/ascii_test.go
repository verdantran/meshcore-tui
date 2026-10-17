package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestToASCII(t *testing.T) {
	for in, want := range map[string]string{
		"┏━▌CHATS ━┓":          "+-|CHATS -+",
		"▸ POWER ✓ 4.02 V":     "> POWER v 4.02 V",
		"\x1b[38;2;1;2;3m◉ me": "\x1b[38;2;1;2;3m@ me",
		"⠀⠉⣀⡇⣿":                " '.:#",
		"東京 ok":                "???? ok",
		"👨‍👩‍👧 family":         "?? family",
		"plain text":           "plain text",
	} {
		if got := toASCII(in); got != want {
			t.Errorf("toASCII(%q) = %q, want %q", in, got, want)
		}
		if ansi.StringWidth(in) != ansi.StringWidth(toASCII(in)) {
			t.Errorf("%q changes width", in)
		}
	}
}

func TestEveryScreenInASCII(t *testing.T) {
	h := busyHarness(t)
	h.m.opt.ASCII = true
	for i := range tabNames {
		h.keys(fmt.Sprint(i + 1))
		if i == 2 {
			h.until("the map", func() bool { return h.m.mapv.world != nil })
		}
		h.advance(time.Second)
		for n, row := range screen(t, h.m) {
			plain := ansi.Strip(row)
			if j := strings.IndexFunc(plain, func(r rune) bool { return r > 0x7e }); j >= 0 {
				t.Errorf("%s row %d isn't ASCII at %d: %q", tabNames[i], n, j, plain)
				break
			}
		}
	}
}
