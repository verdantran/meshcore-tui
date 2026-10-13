package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"meshcore-tui/internal/radio"
)

// flushed runs the notification flush and returns what would be written to the terminal.
func flushed(m *Model) string {
	_, cmd := m.Update(notifyFlushMsg{})
	var out strings.Builder
	var walk func(tea.Cmd)
	walk = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		switch msg := cmd().(type) {
		case tea.BatchMsg:
			for _, c := range msg {
				walk(c)
			}
		case tea.RawMsg:
			out.WriteString(msg.Msg.(string))
		}
	}
	walk(cmd)
	return out.String()
}

func aliceSays(c *clock, text string) radio.Incoming {
	var from [6]byte
	copy(from[:], aliceKey[:6])
	return radio.Incoming{At: c.t, From: from, Hops: -1, SentAt: c.t, Text: text, SNR: 9.5, HasSNR: true}
}

func TestNotifications(t *testing.T) {
	m, c, _ := newChatModel(t)

	// Looking at Public: a DM from Alice notifies.
	send(m, c, 0, aliceSays(c, "are you on tonight?"))
	if got := flushed(m); got != "\a\x1b]9;meshcore-tui · Alice: are you on tonight?\a" {
		t.Fatalf("dm notification = %q", got)
	}

	// Viewing Alice in a focused window: nothing.
	selectConvo(t, m, "Alice")
	send(m, c, 0, aliceSays(c, "still there?"))
	if len(m.notes.pending) != 0 {
		t.Fatal("no notification while you're reading the conversation")
	}
	// Same, but the window lost focus: notify again.
	m.Update(tea.BlurMsg{})
	send(m, c, 0, aliceSays(c, "hello?"))
	if got := flushed(m); !strings.Contains(got, "Alice: hello?") {
		t.Fatalf("blurred notification = %q", got)
	}
	m.Update(tea.FocusMsg{})

	// Channel chatter only notifies when it mentions you; bursts are grouped.
	send(m, c, 0, radio.Incoming{At: c.t, Channel: true, ChannelIdx: 1, Hops: 2, SentAt: c.t, Text: "Bob: morning all"})
	if len(m.notes.pending) != 0 {
		t.Fatal("plain channel messages shouldn't notify")
	}
	send(m, c, 0, radio.Incoming{At: c.t, Channel: true, ChannelIdx: 1, Hops: 2, SentAt: c.t, Text: "Carol: @MeshCore-k4 is Purdown up?"})
	tick(m, c, time.Second)
	marked := false
	for _, l := range strings.Split(plain(screen(t, m)), "\n") {
		marked = marked || strings.Contains(l, "bristol") && strings.Contains(l, "@")
	}
	if !marked {
		t.Errorf("#bristol should carry an @ for the unread mention:\n%s", plain(screen(t, m)))
	}
	selectConvo(t, m, "Public")
	send(m, c, 0, aliceSays(c, "and another"))
	if got := flushed(m); !strings.Contains(got, "2 new messages: Carol in #bristol, Alice") {
		t.Fatalf("grouped notification = %q", got)
	}
}

func TestFilterSearchAndExport(t *testing.T) {
	m, c, _ := newChatModel(t)
	send(m, c, 0, aliceSays(c, "the route via Purdown works"))
	send(m, c, 0, aliceSays(c, "battery is fine"))
	send(m, c, 0, radio.Incoming{At: c.t, Channel: true, ChannelIdx: 1, Hops: 2, SentAt: c.t, Text: "Carol: purdown is back"})

	selectConvo(t, m, "Alice")
	typeKeys(m, "/purdown")
	tick(m, c, time.Second)
	got := plain(screen(t, m))
	if !strings.Contains(got, "1 matching") || strings.Contains(got, "battery is fine") || !strings.Contains(got, "route via Purdown") {
		t.Fatalf("filter:\n%s", got)
	}
	key(m, tea.KeyEnter) // stop typing, keep the filter
	key(m, tea.KeyEscape)
	tick(m, c, time.Second)
	if got := plain(screen(t, m)); !strings.Contains(got, "battery is fine") {
		t.Fatalf("esc should clear the filter:\n%s", got)
	}

	m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	if !m.search.open {
		t.Fatal("ctrl+f should open search")
	}
	typeKeys(m, "purdown")
	if len(m.search.results) != 2 {
		t.Fatalf("search found %d", len(m.search.results))
	}
	got = plain(screen(t, m))
	if !strings.Contains(got, "2 found") || !strings.Contains(got, "#bristol") {
		t.Fatalf("search overlay:\n%s", got)
	}
	key(m, tea.KeyEnter) // newest first: Carol in #bristol
	if cur, _ := m.selectedConvo(); cur.Title != "#bristol" || m.chats.focus == 0 {
		t.Fatalf("jump went to %q (focus %d)", cur.Title, m.chats.focus)
	}

	typeKeys(m, ":export")
	key(m, tea.KeyEnter)
	typeKeys(m, ":export json all")
	key(m, tea.KeyEnter)
	files, _ := filepath.Glob(filepath.Join(m.opt.ExportDir, "*"))
	if len(files) != 2 {
		t.Fatalf("exports = %v (log: %+v)", files, m.log[len(m.log)-1])
	}
	for _, f := range files {
		data, _ := os.ReadFile(f)
		if strings.HasSuffix(f, ".json") {
			var rows []map[string]any
			if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 3 {
				t.Errorf("json export: %v, %d rows", err, len(rows))
			}
		} else if !strings.Contains(string(data), "# #bristol") || !strings.Contains(string(data), "Carol: purdown is back") {
			t.Errorf("text export:\n%s", data)
		}
	}
}

func TestNotifyCommand(t *testing.T) {
	m, _, _ := newChatModel(t)
	typeKeys(m, ":notify")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	var raw string
	var walk func(tea.Cmd)
	walk = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		switch msg := cmd().(type) {
		case tea.BatchMsg:
			for _, c := range msg {
				walk(c)
			}
		case tea.RawMsg:
			raw += msg.Msg.(string)
		}
	}
	walk(cmd)
	if !strings.Contains(raw, "\a") || !strings.Contains(raw, "\x1b]9;meshcore-tui · test") {
		t.Fatalf(":notify wrote %q", raw)
	}
	if l := m.log[len(m.log)-1]; !strings.Contains(l.text, "NOTIFIED  test · via terminal escape + bell") {
		t.Fatalf("log = %q", l.text)
	}
}
