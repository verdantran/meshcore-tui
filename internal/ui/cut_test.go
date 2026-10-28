package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Text that measures differently cluster by cluster than as a whole, under one width method or the other.
var awkward = []string{
	"ab東京東京x",
	"a 👨‍👩‍👧‍👦 x",
	"a👩‍🚀 x",
	"nice ❤️ x",
	"a1️⃣b 1️⃣1️⃣1️⃣abcdefgh",
	"👍🏽 ok",
	"Zoë café",
	"\x1b[31m東京\x1b[0m and \x1b[1m👨‍👩‍👧‍👦\x1b[0m",
}

// Every cut must agree with visibleWidth, whichever way the terminal counts, or rows overrun the frame.
func TestCutsAgreeWithMeasure(t *testing.T) {
	defer setRenderWidth(renderWidth)
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		setRenderWidth(method)
		for _, s := range awkward {
			for n := 0; n <= visibleWidth(s)+1; n++ {
				if got := visibleWidth(fit(s, n)); got != n {
					t.Errorf("method %d: fit(%q, %d) is %d cells", method, s, n, got)
				}
				if got := visibleWidth(col(s, n)); got != n {
					t.Errorf("method %d: col(%q, %d) is %d cells", method, s, n, got)
				}
				if got := visibleWidth(truncate(s, n)); got > n {
					t.Errorf("method %d: truncate(%q, %d) is %d cells", method, s, n, got)
				}
				if got, want := visibleWidth(skip(s, n)), max(visibleWidth(s)-n, 0); got != want {
					t.Errorf("method %d: skip(%q, %d) is %d cells, want %d", method, s, n, got, want)
				}
				// An overlay drawn at any column leaves the row its full width.
				if got := visibleWidth(fit(s, n) + "[box]" + skip(s, n+5)); got != max(visibleWidth(s), n+5) {
					t.Errorf("method %d: overlay at %d over %q is %d cells", method, n, s, got)
				}
			}
		}
	}
}

// A cut keeps the styles around it and draws the … in the style of the text it ends.
func TestCutsKeepStyles(t *testing.T) {
	s := "\x1b[31mhello world\x1b[0m"
	if got := truncate(s, 6); got != "\x1b[31mhello…\x1b[0m" {
		t.Errorf("truncate = %q", got)
	}
	if got := skip(s, 6); got != "\x1b[31mworld\x1b[0m" {
		t.Errorf("skip = %q", got)
	}
	if got := skip("東京", 1); got != " 京" {
		t.Errorf("a wide character cut in two leaves a space: %q", got)
	}
	if got, _ := take("ab東", 3, ""); !strings.HasPrefix(got, "ab") || visibleWidth(got) != 2 {
		t.Errorf("take = %q", got)
	}
}

// Filters find a name however its accents were typed or stored.
func TestFoldMatchesAccents(t *testing.T) {
	for _, name := range []string{"Zoë", "Zoë", "ZOË"} {
		for _, q := range []string{"zoë", "zoë", "zoe", "Zoë"} {
			if !strings.Contains(fold(name), fold(q)) {
				t.Errorf("%q doesn't find %q", q, name)
			}
		}
	}
	if strings.Contains(fold("Zed"), fold("zoe")) {
		t.Error("zoe finds Zed")
	}
}
