package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestToASCII(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"┏━▌CHATS ━┓":          "+-|CHATS -+",
		"▸ POWER ✓ 4.02 V":     "> POWER v 4.02 V",
		"\x1b[38;2;1;2;3m◉ me": "\x1b[38;2;1;2;3m@ me",
		"⠀⠉⣀⡇⣿":                " '.:#",
		"東京 ok":                "???? ok",
		"👨‍👩‍👧 family":         "?????? family", // as wide as the renderer draws it
		"plain text":           "plain text",
		"Zoë café":             "Zoe cafe",
		"Zoe\u0308 cafe\u0301": "Zoe cafe", // the same accents as marks
	} {
		if got := toASCII(in); got != want {
			t.Errorf("toASCII(%q) = %q, want %q", in, got, want)
		}
		if renderWidth.StringWidth(in) != renderWidth.StringWidth(toASCII(in)) {
			t.Errorf("%q changes width", in)
		}
	}
}

// TestASCIIKeepsWidth: characters that take no cell, or join others, mustn't widen a row once it's been fit.
func TestASCIIKeepsWidth(t *testing.T) {
	defer func(m ansi.Method) { renderWidth = m }(renderWidth)
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		renderWidth = method
		for _, in := range []string{"a⁡b⁤c", "x᠎￹￻y", "tag\U000e0041\U000e007f", "café café",
			"1️⃣ key", "🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f flag", "👍🏽 ok", "🇬🇧 uk", "​"} {
			out := toASCII(in)
			if strings.ContainsFunc(out, func(r rune) bool { return r > 0x7e }) {
				t.Errorf("%v: toASCII(%q) = %q", method, in, out)
			}
			if w, ow := renderWidth.StringWidth(in), renderWidth.StringWidth(out); w != ow {
				t.Errorf("%v: %q is %d wide, %q is %d", method, in, w, out, ow)
			}
		}
	}
}

func TestEveryScreenInASCII(t *testing.T) {
	t.Parallel()
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
