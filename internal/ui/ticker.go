package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/theme"
)

const (
	tickerSpeed = 14.0 // cells per second
	tickerFade  = 1800 * time.Millisecond
	tickerBytes = 20
)

type tickItem struct {
	text string
	born time.Time
	x    float64 // position on the track; the window's right edge is at offset+width
}

// ticker is the footer's strip of packets: the bytes of adverts and messages on open channels scroll
// right to left. Everything else is only counted; the header's RX lamp shows it arriving.
type ticker struct {
	items []tickItem
	t0    time.Time
}

func newTicker() ticker { return ticker{} }

// hexBytes writes the start of a packet; bytes hidden reports true for are shown as ··.
func hexBytes(b []byte, hidden func(int) bool) string {
	n := min(len(b), tickerBytes)
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = fmt.Sprintf("%02X", b[i])
		if hidden != nil && hidden(i) {
			parts[i] = "··"
		}
	}
	s := strings.Join(parts, " ")
	if len(b) > n {
		s += " …"
	}
	return s
}

func (t *ticker) offset(now time.Time) float64 {
	if t.t0.IsZero() {
		return 0
	}
	return now.Sub(t.t0).Seconds() * tickerSpeed
}

func (t *ticker) add(text string, now time.Time, width int) {
	if t.t0.IsZero() {
		t.t0 = now
	}
	x := t.offset(now) + float64(width)
	if n := len(t.items); n > 0 {
		x = math.Max(x, t.items[n-1].x+float64(len([]rune(t.items[n-1].text)))+3)
	}
	t.items = append(t.items, tickItem{text: text, born: now, x: x})
	if len(t.items) > 60 {
		t.items = t.items[len(t.items)-60:]
	}
}

// moving reports whether anything is still scrolling or fading in view.
func (t *ticker) moving(level motion.Level, now time.Time, width int) bool {
	if !level.Allows(motion.Ambient) {
		return len(t.items) > 0 && now.Sub(t.items[len(t.items)-1].born) < tickerFade
	}
	off := t.offset(now)
	for _, it := range t.items {
		if it.x+float64(len([]rune(it.text))) > off {
			return true
		}
	}
	return false
}

func (t *ticker) render(th theme.Theme, level motion.Level, now time.Time, width int) string {
	cells := make([]rune, width)
	cols := make([]theme.RGB, width)
	for i := range cells {
		cells[i], cols[i] = ' ', th.Dim
	}
	place := func(text string, x int, c theme.RGB) {
		for j, r := range []rune(text) {
			if p := x + j; p >= 0 && p < width {
				cells[p], cols[p] = r, c
			}
		}
	}
	fade := func(it tickItem) theme.RGB {
		return th.Cyan.Lerp(th.Dim, float64(now.Sub(it.born))/float64(tickerFade))
	}
	if level.Allows(motion.Ambient) {
		off := t.offset(now)
		for _, it := range t.items {
			place(it.text, int(math.Round(it.x-off)), fade(it))
		}
	} else {
		// Without ambient motion the strip holds still: newest packet first.
		x := 1
		for i := len(t.items) - 1; i >= 0 && x < width; i-- {
			place(t.items[i].text, x, fade(t.items[i]))
			x += len([]rune(t.items[i].text)) + 3
		}
	}
	var sb strings.Builder
	start := 0
	for i := 1; i <= width; i++ {
		if i == width || cols[i] != cols[start] {
			sb.WriteString(th.Fg(cols[start], false).Render(string(cells[start:i])))
			start = i
		}
	}
	return sb.String()
}
