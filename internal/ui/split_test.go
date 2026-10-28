package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

func (h *harness) pane(key string) {
	h.m.Update(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if code, ok := map[string]rune{"←": tea.KeyLeft, "→": tea.KeyRight, "↑": tea.KeyUp, "↓": tea.KeyDown}[key]; ok {
		h.key(code)
		return
	}
	h.keys(key)
}

func TestSplitPanes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if !strings.Contains(h.screen(), "ctrl+w panes") {
		t.Error("the footer mentions panes")
	}
	h.m.Update(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if !strings.Contains(h.screen(), "v split right") {
		t.Error("the footer lists pane keys after ctrl+w")
	}
	h.keys("v")
	if got := h.m.lay.String(); got != "right 50 (chats, *packets)" || h.m.tab != 3 {
		t.Fatalf("split right = %s, tab %d", got, h.m.tab)
	}
	scr := h.screen()
	if !strings.Contains(scr, "CHATS · 3 UNREAD") || !strings.Contains(scr, "PACKETS") || !strings.Contains(scr, "┏━▌PACKETS") {
		t.Fatalf("both screens, packets focused:\n%s", scr)
	}

	// A packet heard shows in the packets pane while chats stay on screen.
	h.hear(channelPacket(t, h, "Public", "Bob", "hello all", nil), 6)
	if scr := h.screen(); !strings.Contains(scr, "GRP_TXT") || !strings.Contains(scr, "CHATS") {
		t.Errorf("live packets beside chats:\n%s", scr)
	}

	// Keys go to the focused pane; moving left focuses chats.
	h.pane("←")
	if h.m.tab != 0 || h.m.lay.focus.tab != 0 {
		t.Fatalf("focus left: tab %d", h.m.tab)
	}
	h.pane("→")
	if h.m.tab != 3 {
		t.Error("focus right")
	}
	h.pane("s")
	if got := h.m.lay.String(); got != "right 50 (chats, down 50 (packets, *contacts))" {
		t.Errorf("split down = %s", got)
	}
	h.pane("↑")
	if h.m.tab != 3 {
		t.Error("focus up")
	}
	h.pane("↓")
	h.pane("w")
	if h.m.tab != 0 {
		t.Errorf("w cycles to the first pane, tab %d", h.m.tab)
	}

	// Number keys focus a screen that's already in a pane; others replace the focused pane's screen.
	h.keys("4")
	if h.m.tab != 3 || h.m.lay.String() != "right 50 (chats, down 50 (*packets, contacts))" {
		t.Errorf("4 focuses packets: %s", h.m.lay.String())
	}
	h.keys("7")
	if h.m.lay.String() != "right 50 (chats, down 50 (*stats, contacts))" {
		t.Errorf("7 replaces packets: %s", h.m.lay.String())
	}
	h.key(tea.KeyTab)
	if h.m.tab != 2 {
		t.Errorf("tab skips screens open elsewhere: %d", h.m.tab)
	}
	h.keys("4")

	// Resizing and evening out.
	h.pane(">")
	h.pane("+")
	if got := h.m.lay.String(); got != "right 45 (chats, down 55 (*packets, contacts))" {
		t.Errorf("resized = %s", got)
	}
	h.pane("=")
	if got := h.m.lay.String(); got != "right 33 (chats, down 50 (*packets, contacts))" {
		t.Errorf("even = %s", got)
	}
	h.pane("-")
	h.pane("<")
	if got := h.m.lay.String(); got != "right 38 (chats, down 45 (*packets, contacts))" {
		t.Errorf("shrunk = %s", got)
	}

	// Zoom shows just the focused pane.
	h.pane("z")
	if scr := h.screen(); !strings.Contains(scr, "ZOOMED") || strings.Contains(scr, "CHATS ·") {
		t.Errorf("zoomed:\n%s", scr)
	}
	h.pane("z")

	// Closing hands the space back.
	h.pane("q")
	if got := h.m.lay.String(); got != "right 38 (chats, *contacts)" || h.m.tab != 1 {
		t.Errorf("closed = %s", got)
	}
	h.pane("q")
	h.pane("q")
	if h.m.lay.split() || !strings.Contains(h.lastLog(), "only pane") {
		t.Errorf("last pane stays: %s", h.lastLog())
	}
}

func TestSplitLimitsAndCommands(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := func(line string) {
		h.keys(":")
		h.typeLine(line)
	}
	run("split right stats")
	if h.m.lay.String() != "right 50 (chats, *stats)" {
		t.Fatalf("split command = %s", h.m.lay.String())
	}
	for _, bad := range []string{"split", "split sideways", "split down nowhere", "split down chats"} {
		run(bad)
	}
	log := plainLog(h)
	for _, want := range []string{"split needs right or down", "try right or down", `no screen called "nowhere"`, "chats is already showing"} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q in\n%s", want, log)
		}
	}
	if got := splitCommand().complete(h.m); len(got) != 2+len(tabNames) {
		t.Errorf("completions = %v", got)
	}
	run("close")
	if h.m.lay.split() {
		t.Error(":close")
	}

	// Too small to split, and out of screens.
	h.m.Update(tea.WindowSizeMsg{Width: 50, Height: 44})
	h.pane("v")
	if !strings.Contains(h.lastLog(), "no room to split") {
		t.Errorf("narrow: %s", h.lastLog())
	}
	h.m.Update(tea.WindowSizeMsg{Width: 2000, Height: 200})
	for range tabNames {
		h.pane("v")
	}
	if !strings.Contains(h.lastLog(), "every screen is already showing") || len(h.m.lay.leaves()) != len(tabNames) {
		t.Errorf("all screens: %s", h.lastLog())
	}
	h.pane("y") // an unbound key does nothing
}

func TestHiddenPanesStillWork(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys(":")
	h.typeLine("split right stats")
	h.until("stats polling", func() bool { return h.m.rstats.polling })
	h.pane("←")
	if h.m.statsMsg(statsTickMsg{}) == nil || !h.m.rstats.polling {
		t.Error("stats keep polling while visible but unfocused")
	}
	h.keys("6") // device replaces chats in the focused pane
	h.until("device settings", func() bool { return h.m.device.loaded })
	if !h.m.visible(5) || h.m.visible(0) {
		t.Error("visible")
	}
}

func TestLayoutRemembered(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cfg, err := config.Load(t.TempDir() + "/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	h.m.opt.Config = cfg
	h.pane("v")
	h.pane("s")
	back, _ := config.Load(cfg.Path())
	if back.UI.Layout != "right 50 (chats, down 50 (packets, *contacts))" {
		t.Fatalf("saved = %q", back.UI.Layout)
	}
	m := New(Options{Theme: theme.Neon, Motion: motion.Full, Clock: h.clk.now, Config: back})
	if m.lay.String() != back.UI.Layout || m.tab != 1 {
		t.Errorf("restored = %s, tab %d", m.lay.String(), m.tab)
	}
	h.pane("q")
	h.pane("q")
	if back, _ := config.Load(cfg.Path()); back.UI.Layout != "" {
		t.Errorf("one pane saves as empty, got %q", back.UI.Layout)
	}
}

func TestParseLayout(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"chats", "right 50 (*chats, packets)", "down 30 (right 60 (chats, *stats), packets)"} {
		l, ok := parseLayout(s)
		if !ok || l.String() != s && "*"+s != l.String() {
			t.Errorf("%q → %q %v", s, l.String(), ok)
		}
	}
	for _, bad := range []string{"", "nowhere", "right 50 (chats, chats)", "right 90 (chats, packets)", "right x (chats, packets)",
		"right 50 chats packets", "right 50 (chats packets)", "right 50 (chats, packets", "chats packets", "down 50 (chats, nowhere)"} {
		if _, ok := parseLayout(bad); ok {
			t.Errorf("%q should be refused", bad)
		}
	}
	l, _ := parseLayout("right 50 (chats, packets)")
	if l.focus.tab != 0 {
		t.Error("focus defaults to the first pane")
	}
	if l.resize(stacked, true) {
		t.Error("no stacked split to resize")
	}
	if l.neighbour(0, 1, 100, 40) != nil {
		t.Error("nothing below")
	}
}

func TestNarrowPacketList(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys(":")
	h.typeLine("split right packets")
	h.hear(channelPacket(t, h, "Public", "Bob", "hello all", nil), 6)
	h.hear(channelPacket(t, h, "Public", "Carol", "and you", nil), -3)
	scr := h.screen()
	if !strings.Contains(scr, "Bob: hello all") || !strings.Contains(scr, "Carol: and you") || strings.Contains(scr, "ROUTE  TYPE") {
		t.Errorf("compact list shows the messages:\n%s", scr)
	}
}

func TestClicks(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if h.m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("clicks are reported")
	}
	click := func(x, y int) {
		_, cmd := h.m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		h.start(cmd)
		h.settle()
	}
	clickLabel := func(label string) {
		t.Helper()
		row := strings.Split(plain(screen(t, h.m)), "\n")[1]
		i := strings.Index(row, label)
		if i < 0 {
			t.Fatalf("no %q in %q", label, row)
		}
		click(visibleWidth(row[:i])+len(label)-1, 1) // its last letter
	}
	clickLabel("3 MAP")
	if h.m.tab != 2 {
		t.Fatalf("clicked MAP, tab %d", h.m.tab)
	}
	clickLabel("2 CONTACTS")
	if h.m.tab != 1 {
		t.Fatalf("clicked CONTACTS, tab %d", h.m.tab)
	}
	click(0, 1)
	click(h.m.w-1, 1)
	if h.m.tab != 1 {
		t.Error("clicks off the labels do nothing")
	}

	// In split panes a click focuses the pane it lands in.
	h.keys("1")
	h.pane("v")
	if h.m.tab != 3 {
		t.Fatalf("split: tab %d", h.m.tab)
	}
	click(2, 10)
	if h.m.tab != 0 || h.m.lay.focus.tab != 0 {
		t.Errorf("clicked the left pane: tab %d", h.m.tab)
	}
	click(h.m.w-2, 10)
	if h.m.tab != 3 {
		t.Errorf("clicked the right pane: tab %d", h.m.tab)
	}

	// Only the left button picks.
	h.m.Update(tea.MouseClickMsg{X: 2, Y: 10, Button: tea.MouseRight})
	if h.m.tab != 3 {
		t.Error("right click")
	}

	h.keys(":")
	h.m.Update(tea.PasteMsg{Content: "mouse off"})
	h.key(tea.KeyEnter)
	if h.m.View().MouseMode != tea.MouseModeNone {
		t.Error(":mouse off leaves the mouse to the terminal")
	}
}

// A pane too narrow for its title cuts the title, not its corner.
func TestNarrowPaneKeepsItsCorner(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.keys(":")
	h.typeLine("split right contacts")
	top := strings.Split(h.screen(), "\n")[2]
	if n := strings.Count(top, "┐") + strings.Count(top, "┓"); n != 2 {
		t.Errorf("the top row has %d corners on the right, want 2: %q", n, top)
	}
}
