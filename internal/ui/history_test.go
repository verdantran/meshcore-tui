package ui

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

// heldMatchesStore checks the conversation list the app holds against a fresh read of the store.
func heldMatchesStore(t *testing.T, s *chat.Service) []chat.Info {
	t.Helper()
	held, err := s.Conversations("")
	if err != nil {
		t.Fatal(err)
	}
	s.Pruned()
	fresh, _ := s.Conversations("")
	if !reflect.DeepEqual(held, fresh) {
		t.Fatalf("held list differs from the store:\n%+v\nwant\n%+v", held, fresh)
	}
	return held
}

func TestHistoryKeepAsksThenPrunes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cfg, _ := config.Load(filepath.Join(t.TempDir(), "config.toml"))
	h.m.opt.Config = cfg
	pub, _ := h.channel("Public")
	stranger := store.UnknownConvo([6]byte{0xEE, 1})
	lastYear := h.clk.t.Add(-400 * 24 * time.Hour)
	for _, c := range []store.Convo{store.ChannelConvo(pub.Idx), store.ChannelConvo(pub.Idx), store.ChannelConvo(pub.Idx), stranger} {
		m := store.Message{Convo: c, Sender: "Carol", Text: "from last year", SentAt: lastYear, RecvAt: lastYear}
		if err := h.m.opt.Store.AddMessage(h.m.chat.Device(), &m); err != nil {
			t.Fatal(err)
		}
	}
	h.m.chat.Pruned()
	h.m.jumpToConvo(store.ChannelConvo(pub.Idx))
	h.m.refreshChats()
	if !strings.Contains(h.screen(), "from last year") {
		t.Fatal("old messages not shown to begin with")
	}

	h.keys(":")
	h.typeLine("history keep 365")
	if h.m.asking == nil || !strings.Contains(h.m.askLine(), "DELETE 4 MESSAGES OLDER THAN 365 DAYS?") {
		t.Fatalf("no question: %v", h.m.asking)
	}
	h.keys("n")
	if h.m.keepDays() != 0 || h.m.asking != nil {
		t.Fatal("cancelled, but set anyway")
	}

	h.keys(":")
	h.typeLine("history keep 365")
	h.keys("y")
	h.until("pruned", func() bool { return strings.Contains(plainLog(h), "PRUNED  4 messages older than 365 days") })
	if back, _ := config.Load(cfg.Path()); back.Messages.KeepDays != 365 {
		t.Errorf("not saved: %d", back.Messages.KeepDays)
	}
	if got := h.screen(); strings.Contains(got, "from last year") {
		t.Errorf("the open conversation still shows pruned messages:\n%s", got)
	}
	for _, info := range h.m.chats.infos {
		if info.Convo == stranger {
			t.Error("a conversation with nothing left is still listed")
		}
	}
	heldMatchesStore(t, h.m.chat)

	h.keys(":")
	h.typeLine("history keep 30")
	if h.m.asking != nil || h.m.keepDays() != 30 {
		t.Error("nothing to delete, so nothing to ask")
	}
	h.keys(":")
	h.typeLine("history keep all")
	if h.m.keepDays() != 0 || !strings.HasSuffix(plainLog(h), "HISTORY  keeping every message\n") {
		t.Errorf("keep all: %d\n%s", h.m.keepDays(), plainLog(h))
	}
	for _, bad := range []string{"history keep -1", "history keep soon", "history forget"} {
		h.keys(":")
		h.typeLine(bad)
		if h.m.keepDays() != 0 || h.m.log[len(h.m.log)-1].tone != radio.ToneFail {
			t.Errorf("%s: %s", bad, h.lastLog())
		}
	}

	h.m.openPalette()
	for in, want := range map[string]string{"hist": "history ", "history k": "history keep ", "history keep a": "history keep all ", "history keep 3": "history keep 3"} {
		h.m.pal.input.set(in)
		if got := h.m.completeInput(in); got != want {
			t.Errorf("complete %q = %q, want %q", in, got, want)
		}
	}
}

func TestPruneRunsOnceADay(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.m.adminConfig().Messages.KeepDays = 7
	advance := func(d time.Duration) {
		h.clk.t = h.clk.t.Add(d)
		_, cmd := h.m.Update(frameMsg(h.clk.t))
		h.start(cmd)
		h.until("prune done", func() bool { return h.m.prune.stop == nil })
	}
	advance(time.Second)
	first := h.m.prune.gen
	if first == 0 {
		t.Fatal("no prune once the radio was ready")
	}
	advance(time.Hour)
	if h.m.prune.gen != first {
		t.Fatal("pruned again within the day")
	}
	advance(pruneEvery)
	if h.m.prune.gen != first+1 {
		t.Fatal("not pruned again the next day")
	}
}

// TestBigPruneLeavesTheScreenFree prunes 100,000 messages in a file database while the app keeps taking
// messages: no Update may wait for more than a batch or two.
func TestBigPruneLeavesTheScreenFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mc.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	key := [32]byte{0xC0, 0x4F}
	old := now.Add(-400 * 24 * time.Hour).UnixMilli()
	const n = 100_000
	raw, _ := sql.Open("sqlite", path)
	_, err = raw.Exec(`WITH RECURSIVE i(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM i WHERE n < ?)
		INSERT INTO messages (device, convo, out, sender, text, sent_at, recv_at, hops, snr, has_snr, signed, state, ack, attempts, rtt_ms, flood)
		SELECT ?, 'ch:' || (n % 3), 0, 'Bob', 'old news ' || n, ? + n, ? + n, 1, 0, 0, 0, 0, 0, 0, 0, 0 FROM i`, n, radio.KeyHex(key[:]), old, old)
	raw.Close()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Messages.KeepDays = 365
	m := New(Options{Theme: theme.Neon, Motion: motion.Full, Clock: func() time.Time { return now }, Store: st, Config: cfg})
	m.chatEvent(radio.Ready{Self: radio.SelfInfo{Name: "MeshCore-k4", PubKey: key},
		ChannelList: []radio.Channel{{Idx: 0, Name: "Public"}, {Idx: 1, Name: "#bristol"}, {Idx: 2, Name: "#ops"}}})
	prune := m.pruneDue()
	if prune == nil {
		t.Fatal("no prune once the radio was ready")
	}
	done := make(chan tea.Msg)
	start := time.Now()
	go func() { done <- prune() }()
	var slowest time.Duration
	updates := 0
	var result tea.Msg
	for result == nil {
		select {
		case result = <-done:
		default:
			t0 := time.Now()
			m.Update(eventMsg{radio.Incoming{Channel: true, ChannelIdx: 0, Text: fmt.Sprintf("Bob: still here %d", updates), At: now, SentAt: now}})
			slowest = max(slowest, time.Since(t0))
			updates++
		}
	}
	took := time.Since(start)
	m.Update(result)
	t.Logf("pruned %d messages in %v; %d updates meanwhile, the slowest %v", n, took, updates, slowest)
	if !strings.Contains(m.log[len(m.log)-1].text, "PRUNED  100,000 messages older than 365 days") {
		t.Fatalf("log: %s", m.log[len(m.log)-1].text)
	}
	if left, _ := st.CountOlder(now.Add(time.Second)); left != updates {
		t.Errorf("%d messages left, want the %d that came meanwhile", left, updates)
	}
	// CI's Windows disks flush slowly enough to stall one write for seconds; measure on a real PC before tightening.
	if timed && runtime.GOOS != "windows" && (slowest > 250*time.Millisecond || slowest > took/3) {
		t.Errorf("an update waited %v on a prune that took %v", slowest, took)
	}
	held := heldMatchesStore(t, m.chat)
	if len(held) != 3 || held[0].Unread != updates || held[1].Last != nil {
		t.Errorf("list after: %+v", held)
	}
}
