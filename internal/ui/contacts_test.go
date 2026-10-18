package ui

import (
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/sim"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

// harness runs the model against the real simulator like a small Bubble Tea runtime: every command
// runs in the background and its result, however late, goes back through Update.
type harness struct {
	t       testing.TB
	m       *Model
	s       *sim.Sim
	clk     *clock
	results chan tea.Msg
	wait    time.Duration // how long until gives up; 0 means 3s
}

func newHarness(t testing.TB) *harness {
	return newHarnessWith(t, sim.New(5).Fast().Lossless().Quiet())
}

func newHarnessWith(t testing.TB, s *sim.Sim) *harness {
	t.Helper()
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	c := &clock{t: time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)}
	m := New(Options{Theme: theme.Neon, Motion: motion.Full, Clock: c.now, Store: st, ExportDir: t.TempDir(),
		Dial: func(Target) radio.Radio { return s }, Start: &Target{Kind: "SIM", Addr: "demo"},
		Env: func(string) string { return "" }})
	t.Cleanup(func() { s.Close() })
	h := &harness{t: t, m: m, s: s, clk: c, results: make(chan tea.Msg, 1024)}
	h.start(m.Init())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	h.until("ready", func() bool { return m.ready.Self.Name != "" })
	h.toMain()
	return h
}

// toMain lets the boot log's closing pause run out.
func (h *harness) toMain() {
	for i := 0; i < 20 && h.m.phase != phaseMain; i++ {
		h.advance(500 * time.Millisecond)
	}
	if h.m.phase != phaseMain {
		h.t.Fatal("the boot log never handed over to the main screen")
	}
}

func (h *harness) start(cmd tea.Cmd) {
	if cmd != nil {
		go func() { h.results <- cmd() }()
	}
}

// drain handles every result that has arrived so far.
func (h *harness) drain() {
	for {
		select {
		case msg := <-h.results:
			switch msg := msg.(type) {
			case tea.BatchMsg:
				for _, c := range msg {
					h.start(c)
				}
			case nil, frameMsg, notifyFlushMsg, tea.RawMsg:
				// The frame clock and terminal output don't matter here.
			default:
				_, next := h.m.Update(msg)
				h.start(next)
			}
		default:
			return
		}
	}
}

// settle lets quick radio calls finish.
func (h *harness) settle() {
	for i := 0; i < 15; i++ {
		h.drain()
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *harness) until(what string, ok func() bool) {
	h.t.Helper()
	wait := h.wait
	if wait == 0 {
		wait = 3 * time.Second
	}
	deadline := time.Now().Add(wait)
	for !ok() {
		if time.Now().After(deadline) {
			var tail []string
			for _, l := range h.m.log[max(len(h.m.log)-6, 0):] {
				tail = append(tail, l.text)
			}
			h.t.Fatalf("timed out waiting for %s; recent log:\n  %s", what, strings.Join(tail, "\n  "))
		}
		h.drain()
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *harness) advance(d time.Duration) {
	h.clk.t = h.clk.t.Add(d)
	h.m.Update(frameMsg(h.clk.t))
}

func (h *harness) keys(s string) {
	for _, r := range s {
		_, cmd := h.m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		h.start(cmd)
		h.settle()
	}
}

func (h *harness) key(code rune) {
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: code})
	h.start(cmd)
	h.settle()
}

func (h *harness) screen() string {
	h.advance(time.Second)
	return plain(screen(h.t, h.m))
}

func (h *harness) pick(name string) {
	h.t.Helper()
	h.keys("/" + strings.ToLower(name))
	h.key(tea.KeyEnter)
	if ct, ok := h.m.selectedContact(); !ok || ct.Name != name {
		h.t.Fatalf("couldn't select %s", name)
	}
}

func (h *harness) contact(name string) radio.Contact {
	for _, c := range h.m.chat.Contacts() {
		if c.Name == name {
			return c
		}
	}
	h.t.Fatalf("no contact %s", name)
	return radio.Contact{}
}

func TestContactsBrowseTraceAndRoutes(t *testing.T) {
	h := newHarness(t)
	h.keys("2")
	got := h.screen()
	for _, want := range []string{"CONTACTS 15", "NAME", "ROUTE", "Dundry", "Alice", "Repeater", "advert path"} {
		if !strings.Contains(got, want) {
			t.Fatalf("contacts screen missing %q:\n%s", want, got)
		}
	}

	h.pick("Dundry")
	h.keys("t")
	h.until("trace result", func() bool {
		run := h.m.contacts.traces[h.m.contacts.lastTrace[h.contact("Dundry").Key]]
		return run != nil && run.result != nil
	})
	if got := h.screen(); !strings.Contains(got, "me ─") || !strings.Contains(got, "→ A3") {
		t.Fatalf("trace not drawn:\n%s", got)
	}

	h.key(tea.KeyEscape)
	h.pick("Bob")
	h.keys("d")
	h.until("route found", func() bool { _, ok := h.m.contacts.found[h.contact("Bob").Key]; return ok })
	h.until("route stored", func() bool { return h.contact("Bob").PathLen > 0 })
	if got := h.screen(); !strings.Contains(got, "found: out") {
		t.Fatalf("discovery result not shown:\n%s", got)
	}

	h.keys("p") // the editor starts from the current route, A3·1E; clear it first
	h.key(tea.KeyBackspace)
	h.key(tea.KeyBackspace)
	h.key(tea.KeyEnter) // first repeater alphabetically: Aust (90)
	h.keys("j")
	h.key(tea.KeyEnter) // then Blackdown (B2)
	h.keys("s")
	h.until("route saved", func() bool { c := h.contact("Bob"); return c.PathLen == 2 && c.Path[0] == 0x90 && c.Path[1] == 0xB2 })

	h.keys("f")
	h.until("favourite", func() bool { return h.contact("Bob").Flags&radio.FlagFavourite != 0 })
	if got := h.screen(); !strings.Contains(got, "★ favourite") {
		t.Fatalf("favourite not shown:\n%s", got)
	}
}

func TestContactsShareRemoveImport(t *testing.T) {
	h := newHarness(t)
	h.keys("2")
	h.pick("Carol")
	h.keys("e")
	h.until("link", func() bool { return h.m.contacts.link != "" })
	link := h.m.contacts.link
	got := h.screen()
	if !strings.Contains(got, "SHARE CAROL") || !strings.Contains(got, "▀") || !strings.Contains(got, "meshcore://") {
		t.Fatalf("share overlay:\n%s", got)
	}
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil || !strings.Contains(h.m.log[len(h.m.log)-1].text, "COPIED") {
		t.Fatal("y should copy the link")
	}
	h.key(tea.KeyEscape)

	h.keys("x")
	h.keys("y")
	h.until("removed", func() bool { return len(h.m.chat.Contacts()) == 14 })

	h.key(tea.KeyEscape) // clear the name filter
	h.keys("I")
	h.keys(link)
	h.key(tea.KeyEnter)
	h.keys("y")
	h.until("imported", func() bool { return len(h.m.chat.Contacts()) == 15 })
}

func TestContactsHeardAutoAddAndPrune(t *testing.T) {
	h := newHarness(t)
	h.keys("2")
	eve := radio.Contact{Key: [32]byte{0xE5, 0x01}, Name: "Eve", Type: radio.TypeChat, PathLen: -1, LastAdvert: h.clk.t}
	_, cmd := h.m.Update(eventMsg{radio.Heard{Contact: eve}})
	h.start(cmd)
	if got := h.screen(); !strings.Contains(got, "1 HEARD") {
		t.Fatalf("heard count missing:\n%s", got)
	}
	h.keys("a")
	if got := h.screen(); !strings.Contains(got, "HEARD THIS SESSION") || !strings.Contains(got, "Eve") {
		t.Fatalf("heard list:\n%s", got)
	}
	h.key(tea.KeyEnter)
	h.until("eve added", func() bool { return len(h.m.chat.Contacts()) == 16 })
	h.key(tea.KeyEscape)

	h.keys("A")
	h.until("auto-add loaded", func() bool { return h.m.contacts.autoReady })
	h.key(tea.KeySpace) // chat nodes on
	h.keys("s")
	h.until("auto-add saved", func() bool { a, _ := h.s.AutoAdd(nil); return a.Chat })

	_, cmd = h.m.Update(eventMsg{radio.ContactsFull{}})
	h.start(cmd)
	if got := h.screen(); !strings.Contains(got, "TABLE FULL") {
		t.Fatalf("full banner missing:\n%s", got)
	}
	h.keys("P")
	if got := h.screen(); !strings.Contains(got, "PRUNE CONTACTS") {
		t.Fatalf("prune overlay:\n%s", got)
	}
	h.keys("y")
	h.until("pruned", func() bool { return len(h.m.chat.Contacts()) == 11 })
	if h.m.contacts.full {
		t.Error("pruning should clear the full banner")
	}
}

func TestContactsMessageJumpsToChat(t *testing.T) {
	h := newHarness(t)
	h.keys("2")
	h.pick("Alice")
	h.keys("m")
	if cur, _ := h.m.selectedConvo(); h.m.tab != 0 || cur.Title != "Alice" {
		t.Fatalf("m should open Alice's conversation, got tab %d %q", h.m.tab, cur.Title)
	}
	h.keys("2")
	h.pick("Dundry")
	h.keys("m")
	if h.m.tab != 1 || h.m.log[len(h.m.log)-1].tone != radio.ToneFail {
		t.Fatal("messaging a repeater should be refused")
	}
	for _, size := range [][2]int{{80, 24}, {200, 60}} {
		h.m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		h.screen()
	}
}

// Names with emoji or other wide characters take two cells each; the columns after them must still line up.
func TestColumnsLineUpWithWideNames(t *testing.T) {
	h := newHarness(t)
	cs := h.m.chat.Contacts()
	cs = append(cs,
		radio.Contact{Key: [32]byte{0xE1}, Name: "📡 Hilltop Relay", Type: radio.TypeRepeater, PathLen: -1},
		radio.Contact{Key: [32]byte{0xE2}, Name: "東京ノード", Type: radio.TypeChat, PathLen: -1},
		radio.Contact{Key: [32]byte{0xE3}, Name: "📡📡📡📡📡📡📡📡📡📡📡📡📡📡📡📡 very long", Type: radio.TypeRepeater, PathLen: -1})
	h.m.chat.ReplaceContacts(cs)
	aligned := func(where string, lines []string, marker string) {
		t.Helper()
		at := -1
		for _, l := range lines {
			plain := ansi.Strip(l)
			i := strings.Index(plain, marker)
			if i < 0 {
				continue
			}
			cells := ansi.StringWidth(plain[:i])
			if at >= 0 && cells != at {
				t.Errorf("%s: %q starts at cell %d, not %d:\n%s", where, marker, cells, at, plain)
			}
			at = cells
		}
		if at < 0 {
			t.Errorf("%s: no %q column", where, marker)
		}
	}
	h.keys("2")
	h.m.contacts.typeFilter = 3 // repeaters: one wide name, one too long, the rest plain
	aligned("contacts", strings.Split(h.screen(), "\n"), "Repeater ")
	h.m.contacts.typeFilter = 0
	h.keys("5")
	aligned("admin", strings.Split(h.screen(), "\n"), "signed out")
}

func TestContactsPlaceByHand(t *testing.T) {
	h := newHarness(t)
	h.keys("2")
	name := h.m.chat.Contacts()[0].Name
	h.pick(name)
	ct, _ := h.m.selectedContact()
	h.keys("l")
	if scr := h.screen(); !strings.Contains(scr, "PLACE "+strings.ToUpper(name)) || !strings.Contains(scr, "yours is used instead") {
		t.Fatalf("the prompt says what it overrides:\n%s", scr)
	}
	h.key(tea.KeyEscape)

	h.keys("l")
	h.keys("51.40, -2.70")
	h.key(tea.KeyEnter)
	if p, ok := h.m.manualPlace(ct.Key); !ok || p.Lat != 51.40 || p.Lon != -2.70 {
		t.Fatalf("placed at %v %v", p, ok)
	}
	if scr := h.screen(); !strings.Contains(scr, "51.4000, -2.7000 · set by you") || !strings.Contains(scr, "✎") || !strings.Contains(h.lastLog(), "LOCATION  "+name+" placed") {
		t.Errorf("shown as yours:\n%s", scr)
	}
	placed := false
	for _, n := range h.m.mapNodes() {
		placed = placed || n.Key == ct.Key && n.Pos.Lat == 51.40
	}
	if !placed {
		t.Error("the map puts it there")
	}

	h.keys("l")
	if h.m.contacts.placeIn.text != "51.40000, -2.70000" {
		t.Errorf("the prompt starts from yours: %q", h.m.contacts.placeIn.text)
	}
	h.m.contacts.placeIn.set("somewhere")
	h.key(tea.KeyEnter)
	if !strings.Contains(h.lastLog(), "latitude, longitude") {
		t.Errorf("a bad location says what's wanted: %s", h.lastLog())
	}
	h.keys("l")
	h.m.contacts.placeIn.set("NaN, -2.7")
	h.key(tea.KeyEnter)
	if p, _ := h.m.manualPlace(ct.Key); p.Lat != 51.40 || !strings.Contains(h.lastLog(), "latitude must be between") {
		t.Errorf("NaN isn't a latitude: %v %s", p, h.lastLog())
	}
	h.keys("l")
	h.m.contacts.placeIn.set("")
	h.key(tea.KeyEnter)
	if _, ok := h.m.manualPlace(ct.Key); ok || !strings.Contains(h.lastLog(), "back to what it shares") {
		t.Errorf("empty clears it: %s", h.lastLog())
	}
}

func TestRemappedKeys(t *testing.T) {
	h := newHarness(t)
	h.m.opt.Keys = config.Keys{"all": {"ctrl+n": "down"}, "map": {"w": "up", "+": ""}}
	h.openMap()
	h.screen() // the map takes its size from the first frame
	mv := &h.m.mapv
	start := mv.view
	h.keys("w")
	if mv.view.Center.Lat <= start.Center.Lat {
		t.Error("w does what up does on the map")
	}
	zoom := mv.view.Zoom
	h.keys("+")
	if mv.view.Zoom != zoom {
		t.Error("+ is unbound on the map")
	}
	h.keys("?")
	if scr := h.screen(); !strings.Contains(scr, "YOUR KEYS") || !strings.Contains(scr, "does what up does") || !strings.Contains(scr, "does nothing here") {
		t.Errorf("help lists them:\n%s", scr)
	}
	h.key(tea.KeyEscape)

	h.keys("2")
	sel := h.m.contacts.sel
	h.m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if h.m.contacts.sel != sel+1 {
		t.Errorf("ctrl+n does what down does everywhere: %d from %d", h.m.contacts.sel, sel)
	}
	h.m.opt.Keys["all"]["w"] = "up"
	h.m.Update(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if !h.m.paneKey {
		t.Fatal("ctrl+w starts a pane chord")
	}
	h.keys("w")
	if h.m.paneKey || h.m.contacts.sel != sel+1 {
		t.Error("the key after ctrl+w is the chord's, not rebound")
	}
	delete(h.m.opt.Keys["all"], "w")
	h.keys("/w")
	if h.m.contacts.filter.text != "w" {
		t.Errorf("a text field takes the key as typed: %q", h.m.contacts.filter.text)
	}
}
