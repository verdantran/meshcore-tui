package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

// fullPane is the message pane drawn the long way: every message's lines, then the window onto them.
func fullPane(m *Model, w, rows int) []string {
	c := &m.chats
	var lines []string
	for _, msg := range c.msgs {
		lines = append(lines, m.messageLines(msg, w)...)
	}
	avail := rows - 3
	if p := m.picked(); c.reading && p >= 0 {
		height := func(i int) int { return len(m.messageLines(c.msgs[i], w)) }
		need, hidden := 0, 0
		for i := p; i < len(c.msgs); i++ {
			need += height(i)
		}
		for i := len(c.msgs) - c.scroll; i < len(c.msgs); i++ {
			hidden += height(i)
		}
		for c.scroll < len(c.msgs)-1-p && need-hidden > avail {
			c.scroll++
			hidden += height(len(c.msgs) - c.scroll)
		}
	}
	skip := 0
	for i := len(c.msgs) - 1; i >= len(c.msgs)-c.scroll && i >= 0; i-- {
		skip += len(m.messageLines(c.msgs[i], w))
	}
	end := max(len(lines)-skip, 0)
	body := lines[max(end-avail, 0):end]
	return append(make([]string, avail-len(body)), body...)
}

// TestChatsLongHistory: a full history draws only what the pane shows, the same as drawing all of it,
// wherever the reader has scrolled to, and in time.
func TestChatsLongHistory(t *testing.T) {
	h := newHarness(t)
	for i := range historyLen {
		h.clk.t = h.clk.t.Add(time.Second)
		text := fmt.Sprintf("Zed: note %d *bold* _it_ `code`", i)
		if i%3 == 0 {
			text += strings.Repeat(" and a few more words", i%9)
		}
		h.m.Update(eventMsg{radio.Incoming{At: h.clk.t, SentAt: h.clk.t, Channel: true, ChannelIdx: 0, Hops: i % 4, SNR: 5, HasSNR: true, Text: text}})
	}
	h.settle()
	h.openConvo("Public")
	h.advance(time.Minute)
	if len(h.m.chats.msgs) != historyLen {
		t.Fatalf("%d messages loaded", len(h.m.chats.msgs))
	}
	w, rows := 90, 40
	same := func(step string) {
		t.Helper()
		c := &h.m.chats
		scroll := c.scroll
		want := fullPane(h.m, w, rows)
		wantScroll := c.scroll
		c.scroll = scroll
		out := h.m.convoPane(w, rows)
		if got := out[2 : len(out)-1]; !slices.Equal(got, want) || c.scroll != wantScroll {
			t.Fatalf("%s: scroll %d, want %d; pane differs from drawing every message:\n%s\nwant\n%s",
				step, c.scroll, wantScroll, plain(got), plain(want))
		}
	}
	same("newest")
	h.key(tea.KeyRight)
	for i := range historyLen + 2 {
		h.m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
		same(fmt.Sprint("up ", i))
	}
	if !strings.Contains(plain(h.m.convoPane(w, rows)), "note 0 ") || h.m.chats.scroll < historyLen/2 {
		t.Fatalf("the oldest message is in view, scrolled by %d", h.m.chats.scroll)
	}
	if !testing.Short() {
		if per := medianFrame(h); per > frameBudget {
			t.Errorf("a frame with the oldest of %d messages picked takes %v", historyLen, per)
		}
	}
	for i := range 40 {
		h.m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		same(fmt.Sprint("down ", i))
	}
	w = 60
	same("narrower")
}
