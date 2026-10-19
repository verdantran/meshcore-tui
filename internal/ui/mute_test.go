package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
)

func runPalette(m *Model, line string) {
	typeKeys(m, ":"+line)
	key(m, tea.KeyEnter)
}

func convoRow(t *testing.T, m *Model, title string) string {
	t.Helper()
	for _, l := range strings.Split(plain(screen(t, m)), "\n") {
		if i := strings.Index(l, "│"); i > 0 && strings.Contains(l[:i], title) {
			return l[:i]
		}
	}
	t.Fatalf("no row for %s:\n%s", title, plain(screen(t, m)))
	return ""
}

func TestMutedConversationsKeepQuiet(t *testing.T) {
	m, c, _ := newChatModel(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	m.opt.Config, _ = config.Load(path)

	runPalette(m, "mute alice")
	send(m, c, 0, aliceSays(c, "are you on tonight?"))
	tick(m, c, time.Second)
	if len(m.notes.pending) != 0 {
		t.Fatal("a muted DM doesn't notify")
	}
	if row := convoRow(t, m, "Alice"); !strings.Contains(row, "×") || !strings.Contains(row, " 1 ") {
		t.Errorf("a muted conversation is marked and keeps its unread count: %q", row)
	}

	runPalette(m, "mute bristol 8h")
	mention := radio.Incoming{At: c.t, Channel: true, ChannelIdx: 1, Hops: 2, SentAt: c.t, Text: "Carol: @MeshCore-k4 is Purdown up?"}
	send(m, c, 0, mention)
	if len(m.notes.pending) != 0 {
		t.Fatal("a mention in a muted channel doesn't notify")
	}

	saved, err := config.Load(path)
	if err != nil || len(saved.Notifications.Muted) != 2 {
		t.Fatalf("mutes are saved: %v %+v", err, saved.Notifications.Muted)
	}
	alice, bristol := saved.Notifications.Muted[0], saved.Notifications.Muted[1]
	if alice.Contact != radio.KeyHex(aliceKey[:]) || !alice.Until.IsZero() || bristol.Channel != "#bristol" || !bristol.Until.Equal(c.t.Add(8*time.Hour).Truncate(time.Second)) {
		t.Errorf("saved mutes = %+v", saved.Notifications.Muted)
	}
	if again := New(Options{Config: saved, Clock: c.now}); len(again.notes.muted) != 2 {
		t.Error("mutes are read back at startup")
	}

	c.t = c.t.Add(8*time.Hour + time.Minute)
	mention.At, mention.SentAt = c.t, c.t
	send(m, c, 0, mention)
	if len(m.notes.pending) != 1 {
		t.Fatal("a mute for a while runs out")
	}
	m.notes.pending, m.notes.waiting = nil, false

	selectConvo(t, m, "Alice")
	if head := plain(screen(t, m)); !strings.Contains(head, "muted until unmuted (m)") {
		t.Errorf("the open conversation says it's muted:\n%s", head)
	}
	typeKeys(m, "m")
	if _, ok := m.mutedUntil(store.ContactConvo(aliceKey), "Alice"); ok {
		t.Fatal("m unmutes the open conversation")
	}
	if l := m.log[len(m.log)-1].text; !strings.Contains(l, "UNMUTED  Alice") {
		t.Errorf("log = %q", l)
	}
	typeKeys(m, "m")
	if _, ok := m.mutedUntil(store.ContactConvo(aliceKey), "Alice"); !ok {
		t.Fatal("m mutes it again")
	}
	runPalette(m, "unmute")
	if _, ok := m.mutedUntil(store.ContactConvo(aliceKey), "Alice"); ok {
		t.Fatal(":unmute alone unmutes the open conversation")
	}
}

func TestMuteCommandMistakes(t *testing.T) {
	m, _, _ := newChatModel(t)
	for _, line := range []string{"mute nobody", "mute alice bristol", "unmute bristol"} {
		runPalette(m, line)
		if l := m.log[len(m.log)-1]; l.tone != radio.ToneFail {
			t.Errorf(":%s should fail, logged %q", line, l.text)
		}
	}
	for s, want := range map[string]time.Duration{"8h": 8 * time.Hour, "90m": 90 * time.Minute, "2d": 48 * time.Hour} {
		if d, ok := muteLength(s); !ok || d != want {
			t.Errorf("muteLength(%s) = %s %v", s, d, ok)
		}
	}
	for _, s := range []string{"-1h", "0d", "soon", "alice"} {
		if _, ok := muteLength(s); ok {
			t.Errorf("%s isn't a length of time", s)
		}
	}
}

func TestMuteMatchesASenderKnownByPrefix(t *testing.T) {
	key := radio.KeyHex(aliceKey[:])
	var p [6]byte
	copy(p[:], aliceKey[:6])
	if !sameMute(muteFor(store.UnknownConvo(p), ""), config.Mute{Contact: strings.ToUpper(key)}) {
		t.Error("a sender known by their key's first bytes matches the whole key")
	}
	if sameMute(config.Mute{Contact: key[:4]}, config.Mute{Contact: key}) {
		t.Error("a few hex digits are too short to match on")
	}
	if sameMute(config.Mute{Channel: "Public"}, config.Mute{Channel: "#bristol"}) {
		t.Error("channels match by name")
	}
}

// rawFrom runs a command and returns what it would write to the terminal.
func rawFrom(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var out string
		for _, c := range msg {
			out += rawFrom(c)
		}
		return out
	case tea.RawMsg:
		return msg.Msg.(string)
	}
	return ""
}

func TestNotificationsAreLimitedThenSummed(t *testing.T) {
	m, c, _ := newChatModel(t)
	m.notes.limit = 2
	for i, text := range []string{"one", "two", "three", "four"} {
		send(m, c, 5*time.Second, aliceSays(c, text))
		_, cmd := m.Update(notifyFlushMsg{})
		if i < 2 {
			if got := rawFrom(cmd); !strings.Contains(got, "Alice: "+text) {
				t.Fatalf("notification %d = %q", i+1, got)
			}
		} else if len(m.notes.held) != i-1 || !m.notes.summing {
			t.Fatalf("notification %d should be held for the summary, %d held", i+1, len(m.notes.held))
		}
		if !strings.Contains(m.toasts[len(m.toasts)-1].body, text) {
			t.Errorf("the toast still shows message %d", i+1)
		}
	}
	c.t = c.t.Add(45 * time.Second) // a minute after the first
	_, cmd := m.Update(noteSummaryMsg{})
	if got := rawFrom(cmd); !strings.Contains(got, "2 more messages: Alice") {
		t.Fatalf("summary = %q", got)
	}
	_, cmd = m.Update(noteSummaryMsg{})
	if got := rawFrom(cmd); got != "" {
		t.Errorf("the summary goes once: %q", got)
	}

	m.notes.limit = 0
	for range 5 {
		send(m, c, time.Second, aliceSays(c, "again"))
		if got := flushed(m); !strings.Contains(got, "Alice: again") {
			t.Fatalf("0 is no limit: %q", got)
		}
	}
}
