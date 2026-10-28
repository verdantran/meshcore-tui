package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

func has(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("screen missing %q:\n%s", w, got)
		}
	}
}

func TestContactsOverlaysRender(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("2")
	h.pick("Alice")

	h.keys("p")
	has(t, h.screen(), "SET ROUTE TO ALICE", "me › A3 › 1E › Alice", "shares its hash with")
	h.key(tea.KeyBackspace)
	h.key(tea.KeyBackspace)
	has(t, h.screen(), "in direct range")
	h.keys("kj")
	h.key(tea.KeyEscape)

	h.keys("I")
	h.keys("meshcore://zz")
	has(t, h.screen(), "IMPORT A CONTACT", "meshcore://zz")
	h.key(tea.KeyBackspace)
	h.key(tea.KeyEnter)
	if l := h.m.log[len(h.m.log)-1]; l.tone != radio.ToneFail || !strings.Contains(l.text, "IMPORT") {
		t.Fatalf("a damaged link should fail before reaching the radio: %+v", l)
	}

	h.keys("x")
	has(t, h.screen(), "REMOVE CONTACT", "Remove Alice from the radio?")
	h.keys("n")
	if len(h.m.chat.Contacts()) != 15 {
		t.Fatal("any key but y cancels removal")
	}

	h.keys("A")
	h.until("auto-add", func() bool { return h.m.contacts.autoReady })
	has(t, h.screen(), "AUTO-ADD", "[x] Repeaters", "Hop limit: no limit")
	h.keys("jjjjj")
	h.keys("++")
	has(t, h.screen(), "up to 1 hops away")
	h.keys("-")
	has(t, h.screen(), "direct neighbours only")
	h.keys("kkk")
	h.key(tea.KeyEscape)

	h.keys("P")
	has(t, h.screen(), "PRUNE CONTACTS", "least recently heard")
	h.key(tea.KeyEscape)

	// Share on a terminal too small for the code.
	h.m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.keys("e")
	h.until("link", func() bool { return h.m.contacts.link != "" })
	has(t, h.screen(), "too small for the QR code")
	h.keys("q") // any other key closes
	if h.m.contacts.overlay != noOverlay {
		t.Fatal("share should close")
	}
}

func TestContactsSortFilterAndErrors(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("2")
	for i := range sortNames {
		has(t, h.screen(), "sort "+sortNames[i])
		h.keys("o")
	}
	for i := range typeFilters {
		has(t, h.screen(), "type "+typeFilters[i].name)
		h.keys("F")
	}
	h.keys("/zzz")
	has(t, h.screen(), "nothing matches")
	h.key(tea.KeyBackspace)
	h.key(tea.KeyEscape)
	h.keys("a")
	has(t, h.screen(), "Nothing yet")
	h.keys("jk")
	h.key(tea.KeyEscape)

	// Alice has a route; take it away and the trace needs discovery first.
	h.pick("Alice")
	h.keys("r")
	h.until("route reset", func() bool { return strings.Contains(h.m.log[len(h.m.log)-1].text, "ROUTE RESET") })
	ct := h.contact("Alice")
	ct.PathLen, ct.Path = -1, nil
	h.m.chat.ContactUpdated(ct)
	has(t, h.screen(), "needs a stored route")
	h.keys("t")
	has(t, h.m.log[len(h.m.log)-1].text, "has no stored route")

	// Radio errors on each contact call show up in the log.
	for _, msg := range []tea.Msg{
		contactsSyncedMsg{err: errors.New("sync")}, traceStartedMsg{err: errors.New("trace")},
		exportedMsg{err: errors.New("export")}, autoAddMsg{err: errors.New("auto")}, opDoneMsg{what: "SAVE", err: errors.New("save")},
		advertPathMsg{err: errors.New("ignored")},
	} {
		h.m.Update(msg)
	}
	fails := 0
	for _, l := range h.m.log {
		if l.tone == radio.ToneFail {
			fails++
		}
	}
	if fails < 6 {
		t.Errorf("expected each failure logged, got %d", fails)
	}

	// A trace that never answers is marked lost.
	h.m.contacts.traces[42] = &traceRun{key: h.contact("Dundry").Key, path: []byte{0xA3}, started: h.clk.t}
	h.m.contacts.lastTrace[h.contact("Dundry").Key] = 42
	h.m.Update(traceTimeoutMsg{tag: 42})
	h.key(tea.KeyEscape)
	h.pick("Dundry")
	has(t, h.screen(), "[FAIL] no answer along A3")

	// Events about things we don't know are ignored.
	h.m.Update(eventMsg{radio.TraceResult{Tag: 999}})
	h.m.Update(eventMsg{radio.PathFound{Prefix: [6]byte{0xEE}}})
	h.m.Update(eventMsg{radio.ContactRemoved{Key: h.contact("Dundry").Key}})
	if len(h.m.chat.Contacts()) != 14 {
		t.Error("a contact the radio dropped should go")
	}
	h.m.Update(eventMsg{radio.Heard{Contact: radio.Contact{Key: [32]byte{1}, Name: "Eve"}}})
	h.m.Update(eventMsg{radio.Heard{Contact: radio.Contact{Key: [32]byte{1}, Name: "Eve"}}})
	if len(h.m.contacts.heard) != 1 {
		t.Error("hearing the same node twice keeps one entry")
	}
}

func TestChatEdges(t *testing.T) {
	t.Parallel()
	m, c, r := newChatModel(t)
	startConvo(t, m, "Alice")
	key(m, tea.KeyEnter)
	typeKeys(m, "hello x")
	key(m, tea.KeyBackspace)
	key(m, tea.KeyEscape)
	if m.chats.writing || m.chats.input.text != "hello " {
		t.Fatalf("esc stops writing and keeps the draft: %q", m.chats.input.text)
	}

	// The radio refuses the send outright.
	r.failSend = errors.New("table full")
	key(m, tea.KeyEnter)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	follow(t, m, c, cmd, false)
	tick(m, c, time.Second)
	has(t, plain(screen(t, m)), "[FAIL] not delivered after 1 tries")
	r.failSend = nil

	// Pending and retrying states, as they'd be mid-flight.
	cur := m.chats.msgs[len(m.chats.msgs)-1]
	cur.State, cur.Attempts = 1, 0
	if !strings.Contains(m.messageMeta(cur), "sending") {
		t.Error("pending reads as sending")
	}
	cur.Attempts = 1
	if !strings.Contains(m.messageMeta(cur), "retrying · try 2") {
		t.Error("a retry says which try")
	}
	cur.State, cur.Flood, cur.Attempts = 2, true, 3
	if !strings.Contains(m.messageMeta(cur), "awaiting ack · flood · try 3") {
		t.Error("awaiting ack over flood")
	}
	cur.Convo = "ch:0"
	if !strings.Contains(m.messageMeta(cur), "sent") {
		t.Error("channel messages just say sent")
	}

	for _, k := range []rune{tea.KeyPgUp, tea.KeyPgDown, 'K', 'J'} {
		key(m, k)
	}
	typeKeys(m, "/hel")
	key(m, tea.KeyBackspace)
	key(m, tea.KeyEnter)
	key(m, tea.KeyEscape)
	if m.chats.filter.text != "" {
		t.Error("esc clears the filter")
	}
}

func TestStoppedScanLeavesNothingRunning(t *testing.T) {
	returned := make(chan struct{})
	m := New(Options{Theme: theme.Neon, Motion: motion.Full,
		Scan: func(ctx context.Context, found func(Target)) error {
			defer close(returned)
			for i := range 40 { // more than the channel holds, and nobody reading
				found(Target{Addr: fmt.Sprintf("AA:%02d", i)})
			}
			return ctx.Err()
		}})
	before := runtime.NumGoroutine()
	cmd := m.startScan()
	m.connect.stopScan()
	<-returned
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("%d goroutines after the scan stopped, %d before", n, before)
	}
	if m.connect.scanning || m.connect.scanned(cmd().(scanMsg)) != nil || len(m.connect.found) != 0 {
		t.Error("what a stopped scan heard is ignored")
	}
}

func TestConnectBluetoothScan(t *testing.T) {
	t.Parallel()
	c := &clock{t: time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)}
	var dialled []Target
	scans := 0
	opt := Options{Theme: theme.Neon, Motion: motion.Full, Clock: c.now,
		Scan: func(ctx context.Context, found func(Target)) error {
			scans++
			if scans == 2 {
				return errors.New("bluetooth is powered off: turn Bluetooth on and try again")
			}
			found(Target{Addr: "AA:BB:CC:DD:EE:01", Detail: "MeshCore-k4 · -70 dBm"})
			found(Target{Addr: "AA:BB:CC:DD:EE:02", Detail: "MeshCore-shed · -88 dBm"})
			found(Target{Addr: "AA:BB:CC:DD:EE:01", Detail: "MeshCore-k4 · -61 dBm"})
			return nil
		},
		Dial: func(t Target) radio.Radio {
			dialled = append(dialled, t)
			return &stubRadio{ch: make(chan radio.Event)}
		}}
	m := New(opt)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	has(t, plain(screen(t, m)), "b Bluetooth")

	cmd := m.startScan()
	if m.startScan() != nil {
		t.Error("one scan at a time")
	}
	has(t, plain(screen(t, m)), "looking for Bluetooth radios")
	for cmd != nil {
		cmd = m.connect.scanned(cmd().(scanMsg))
	}
	scr := plain(screen(t, m))
	has(t, scr, "BLE  AA:BB:CC:DD:EE:01", "MeshCore-k4 · -61 dBm", "MeshCore-shed")
	if strings.Contains(scr, "-70 dBm") || m.connect.scanning {
		t.Errorf("a radio heard again is updated, and the scan ends:\n%s", scr)
	}

	for cmd = m.startScan(); cmd != nil; {
		cmd = m.connect.scanned(cmd().(scanMsg))
	}
	has(t, plain(screen(t, m)), "BLUETOOTH  bluetooth is powered off", "MeshCore-shed")

	for m.connect.items[m.connect.sel].Kind != "BLE" {
		typeKeys(m, "j")
	}
	key(m, tea.KeyEnter)
	if len(dialled) != 1 || dialled[0].Kind != "BLE" || dialled[0].Addr != "AA:BB:CC:DD:EE:01" {
		t.Errorf("dialled = %+v", dialled)
	}

	m = New(opt)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	typeKeys(m, "b")
	if !m.connect.scanning {
		t.Error("b scans")
	}
	m.connect.stopScan()
	typeKeys(m, "m")
	if strings.Contains(plain(screen(t, m)), "b Bluetooth") {
		t.Error("repeater admin mode is USB only")
	}
}

func TestConnectScreenKeys(t *testing.T) {
	t.Parallel()
	c := &clock{t: time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)}
	var dialled []Target
	m := New(Options{Theme: theme.Neon, Motion: motion.Full, Clock: c.now,
		Ports: func() []Target { return []Target{{Kind: "USB", Addr: "/dev/cu.a", Detail: "Espressif"}} },
		Scan:  func(ctx context.Context, found func(Target)) error { <-ctx.Done(); return nil },
		Dial: func(t Target) radio.Radio {
			dialled = append(dialled, t)
			return &stubRadio{ch: make(chan radio.Event)}
		}})
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// Every key shows, wrapping to a second line rather than cutting the last ones off.
	has(t, plain(screen(t, m)), "SELECT LINK", "/dev/cu.a", "TCP", "SIM", "T theme", "q quit")
	typeKeys(m, "jkjr") // down, up, down, rescan
	if m.connect.items[m.connect.sel].Kind != "TCP" {
		t.Fatalf("selection = %+v", m.connect.items[m.connect.sel])
	}
	typeKeys(m, "d") // nothing saved to forget
	key(m, tea.KeyEnter)
	typeKeys(m, "10.0.0.4 x") // host names have no spaces, so the space is ignored
	key(m, tea.KeyBackspace)
	has(t, plain(screen(t, m)), "10.0.0.4█", "type a host")
	key(m, tea.KeyEscape)
	key(m, tea.KeyEnter)
	key(m, tea.KeyEnter)
	if len(dialled) != 1 || dialled[0].Kind != "TCP" || dialled[0].Addr != "10.0.0.4" {
		t.Fatalf("dialled = %+v", dialled)
	}

	// After a failure: retry, or go back and choose another link.
	send(m, c, 0, radio.Fatal{Err: errors.New("refused")})
	typeKeys(m, "r")
	if len(dialled) != 2 {
		t.Fatal("r retries the same link")
	}
	send(m, c, 0, radio.Fatal{Err: errors.New("refused")})
	typeKeys(m, "c")
	if m.phase != phaseConnect {
		t.Fatal("c returns to the connect screen")
	}
	if _, quit := m.key(tea.KeyPressMsg{Code: 'q', Text: "q"}); !quit {
		t.Error("q quits from the connect screen")
	}
}

func TestShellEventsAndKeys(t *testing.T) {
	t.Parallel()
	m, c := newTestModel(t, motion.Full, 120, 34)
	boot(m, c)
	// Before the handover finishes, enter skips it.
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.phase != phaseMain {
		t.Fatal("enter should skip the handover")
	}
	tick(m, c, time.Second) // let the header finish sliding in
	send(m, c, 0, radio.Lost{Err: errors.New("unplugged")})
	tick(m, c, time.Second)
	has(t, plain(screen(t, m)), "RECONNECTING", "LINK LOST")
	send(m, c, 0, radio.Restored{})
	send(m, c, 0, radio.Battery{MilliVolts: 3800})
	send(m, c, 0, radio.Transmit{What: "SEND_SELF_ADVERT"})
	tick(m, c, time.Second)
	has(t, plain(screen(t, m)), "LINKED", "3.80V ███░░▌ 60%")
	m.linked = linkDown
	has(t, plain(screen(t, m)), "DOWN")
	m.linked = linkUp

	for i := 0; i < 600; i++ {
		m.addLog(c.t, "line", radio.ToneInfo)
		m.apply(radio.Frame{Name: "X"})
	}
	if len(m.log) != 500 || len(m.frames) != 400 {
		t.Errorf("log %d, frames %d: both are capped", len(m.log), len(m.frames))
	}

	typeKeys(m, "v")
	if m.level != motion.Reduced {
		t.Error("v cycles motion")
	}
	for _, k := range []rune{'1', '2', '3', '4', '5', '6', '7'} {
		typeKeys(m, string(k))
		typeKeys(m, "?")
		screen(t, m)
		key(m, tea.KeyEscape)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m.Update(tea.FocusMsg{})
	m.Update(tea.BlurMsg{})
	if _, quit := m.key(tea.KeyPressMsg{Code: 'q', Text: "q"}); !quit {
		t.Error("q quits")
	}
	m.shutdown()
}

func TestConfigSaveFailureIsLogged(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o644)
	cfg, _ := config.Load(filepath.Join(file, "config.toml"))
	m, _ := newTestModel(t, motion.Full, 100, 30)
	m.opt.Config = cfg
	m.cycleTheme()
	if l := m.log[len(m.log)-1]; !strings.Contains(l.text, "CONFIG NOT SAVED") {
		t.Errorf("save failure = %+v", l)
	}
}

func TestNotificationsOff(t *testing.T) {
	t.Parallel()
	m, c, _ := newChatModel(t)
	m.notes.method = "off"
	send(m, c, 0, aliceSays(c, "hi"))
	if got := flushed(m); got != "" {
		t.Errorf("off should send nothing, got %q", got)
	}
	typeKeys(m, ":notify")
	key(m, tea.KeyEnter)
	if l := m.log[len(m.log)-1]; l.tone != radio.ToneFail {
		t.Errorf(":notify with notifications off should explain: %+v", l)
	}
}

func TestEveryPaletteCommand(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := func(line string) logLine {
		h.keys(":" + line)
		h.key(tea.KeyEnter)
		return h.m.log[len(h.m.log)-1]
	}
	for _, bad := range []string{"theme nope", "motion", "motion lots", "msg", "msg nobody hi", "trace", "trace nobody", "export csv"} {
		if l := run(bad); l.tone != radio.ToneFail {
			t.Errorf(":%s should fail, logged %+v", bad, l)
		}
	}
	run("motion off")
	run("motion full")
	if h.m.level != motion.Full {
		t.Error(":motion")
	}
	run("ticker off")
	if h.m.tickerOn {
		t.Error(":ticker off")
	}
	run("ticker")
	if !h.m.tickerOn {
		t.Error(":ticker toggles")
	}
	run("frames")
	if !h.m.debug {
		t.Error(":frames")
	}
	run("theme")
	run("help")
	if !h.m.help {
		t.Error(":help")
	}
	h.key(tea.KeyEscape)
	for _, s := range tabNames {
		run(strings.ToLower(s))
		if tabNames[h.m.tab] != s {
			t.Errorf(":%s", strings.ToLower(s))
		}
	}
	run("trace dundry")
	h.until("trace", func() bool { return len(h.m.contacts.traces) == 1 })
	if h.m.tab != 1 {
		t.Error(":trace opens contacts")
	}

	// Typing in the palette: completion hints, history, editing.
	h.keys(":t")
	has(t, h.screen(), "theme", "ticker", "trace", "tab complete")
	h.keys("race ")
	has(t, h.screen(), "<contact>")
	h.key(tea.KeyBackspace)
	h.key(tea.KeyUp)
	h.key(tea.KeyUp)
	h.key(tea.KeyDown)
	h.key(tea.KeyDown)
	h.key(tea.KeyDown)
	h.key(tea.KeySpace)
	h.key(tea.KeyEscape)
	if h.m.pal.open {
		t.Error("esc closes the palette")
	}
	run("")
	h.keys(":quit")
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || !h.m.quitting {
		t.Error(":quit should quit")
	}
}

func TestInspectorAndSearchKeys(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("4")
	for i := 0; i < 5; i++ {
		h.hear(channelPacket(t, h, "Public", "Bob", fmt.Sprintf("test %d", i), []byte{0xA3}), float32(i))
	}
	h.hear([]byte{0x15}, 0) // malformed: not shown
	has(t, h.screen(), "1 OTHERS NOT SHOWN")
	h.keys("/test 0")
	h.key(tea.KeyBackspace)
	h.key(tea.KeyEnter)
	h.key(tea.KeyEscape)
	h.keys("/x")
	h.key(tea.KeyEscape)
	h.keys("jjjg")
	if h.m.inspector.sel != 0 {
		t.Error("g returns to the newest")
	}
	h.keys("j][[")
	h.keys("FFF")
	h.keys("Cy")
	if len(h.m.inspector.rows) != 0 {
		t.Error("C clears")
	}
	h.key(tea.KeyEscape)

	// The inspector keeps the newest packets once full.
	for i := 0; i < packetKeep+5; i++ {
		h.m.heardPacket(radio.Packet{Raw: channelPacket(t, h, "Public", "Bob", fmt.Sprint(i), nil), SNR: 1})
	}
	if len(h.m.inspector.rows) != packetKeep {
		t.Errorf("kept %d packets", len(h.m.inspector.rows))
	}

	// Export somewhere it can't write.
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o644)
	h.m.opt.ExportDir = filepath.Join(file, "exports")
	if err := h.m.exportPackets(); err == nil {
		t.Error("export under a file should fail")
	}

	// Search keys.
	h.m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	has(t, h.screen(), "Type to search")
	h.keys("bob x")
	h.key(tea.KeyBackspace)
	h.key(tea.KeyBackspace)
	h.key(tea.KeyDown)
	h.key(tea.KeyUp)
	has(t, h.screen(), "find▸ bob")
	h.key(tea.KeyEscape)
	if h.m.search.open {
		t.Error("esc closes search")
	}
}

func TestMonoAndHelpers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.m.setTheme(theme.Mono)
	for _, tab := range "12345" {
		h.keys(string(tab))
		h.screen()
	}
	h.keys("2")
	h.keys("e")
	h.until("link", func() bool { return h.m.contacts.link != "" })
	has(t, h.screen(), "▀")
	if ago(3*time.Hour) != "3h ago" || ago(5*time.Minute) != "5m ago" || ago(0) != "now" {
		t.Error("ago")
	}
	if truncate("abc", 0) != "" || truncate("abcdef", 4) != "abc…" {
		t.Error("truncate")
	}
	if wipe("x", 10, 1, theme.Neon) != "x" {
		t.Error("a finished wipe leaves the line alone")
	}
	if orFlood(nil, 1) != "in range" || orFlood([]byte{0xA3}, 1) != "A3" || orFlood([]byte{0xA3, 0x01, 0x1E, 0x02}, 2) != "A301·1E02" {
		t.Error("orFlood")
	}
	for typ := radio.ContactType(1); typ <= 4; typ++ {
		typeColour(theme.Neon, typ)
		routeText(radio.Contact{Type: typ, PathLen: int(typ) - 2, Path: []byte{1}})
	}
}
