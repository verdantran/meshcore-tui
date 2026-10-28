package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

// keyCmd presses a key and hands back what it started, rather than dropping it as typeKeys does.
func keyCmd(m *Model, r rune) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	return cmd
}

func TestSearchWaitsForTypingAndDropsStaleResults(t *testing.T) {
	t.Parallel()
	m, c, _ := newChatModel(t)
	send(m, c, 0, aliceSays(c, "the route via Purdown works"))
	send(m, c, 0, radio.Incoming{At: c.t, Channel: true, ChannelIdx: 1, Hops: 2, SentAt: c.t, Text: "Carol: purdown is back"})
	m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})

	// The keystroke itself doesn't touch the store: it only starts the pause.
	first := keyCmd(m, 'p')
	if first == nil || len(m.search.results) != 0 {
		t.Fatalf("a keystroke searched straight away: %d results", len(m.search.results))
	}
	due := first()
	typeKeys(m, "urdown")
	if _, cmd := m.Update(due); cmd != nil {
		t.Fatal("a pause from an earlier keystroke still searched")
	}

	// An older query's results are dropped when they come after a newer one's.
	_, late := m.Update(searchDueMsg{m.search.gen})
	stale := late()
	finish(m, keyCmd(m, 'x'))
	if len(m.search.results) != 0 {
		t.Fatalf("purdownx found %d", len(m.search.results))
	}
	m.Update(stale)
	if len(m.search.results) != 0 {
		t.Fatal("results for an older query replaced the newer ones")
	}

	// enter before the results come opens the first of them when they do.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.search.open {
		t.Fatal("enter closed the search before its results came")
	}
	finish(m, cmd)
	if cur, _ := m.selectedConvo(); m.search.open || cur.Title != "#bristol" || m.chats.focus == 0 {
		t.Fatalf("jump went to %q (open %v)", cur.Title, m.search.open)
	}
}

func TestExportRunsInTheBackground(t *testing.T) {
	t.Parallel()
	m, c, _ := newChatModel(t)
	send(m, c, 0, radio.Incoming{At: c.t, Channel: true, ChannelIdx: 1, Hops: 2, SentAt: c.t, Text: "Carol: purdown is back"})
	typeKeys(m, ":export all")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	files, _ := filepath.Glob(filepath.Join(m.opt.ExportDir, "*"))
	if len(files) != 1 {
		t.Fatalf("exports = %v", files)
	}
	if data, _ := os.ReadFile(files[0]); len(data) != 0 {
		t.Fatalf("the export was written in Update:\n%s", data)
	}
	finish(m, cmd)
	data, _ := os.ReadFile(files[0])
	if !strings.Contains(string(data), "Carol: purdown is back  (2 hops)  [unsigned]") || !strings.Contains(m.log[len(m.log)-1].text, "EXPORTED  1 messages") {
		t.Fatalf("export:\n%s\nlog: %s", data, m.log[len(m.log)-1].text)
	}
}
