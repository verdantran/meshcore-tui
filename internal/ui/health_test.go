package ui

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"meshcore-tui/internal/admin"
	"meshcore-tui/internal/config"
	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/theme"
)

func (h *harness) watchTick() {
	_, cmd := h.m.Update(watchTickMsg{h.m.adm.watchGen})
	h.start(cmd)
	h.settle()
}

func TestWatchOverTheAir(t *testing.T) {
	h := newHarness(t)
	h.keys("5")
	dundry := h.choose("Dundry")
	h.keys("w")
	if !h.m.watched(dundry.Key) || !strings.Contains(h.lastLog(), "WATCHING  Dundry: its status is polled every 10m") {
		t.Fatalf("watch: %s", h.lastLog())
	}
	if scr := h.screen(); !strings.Contains(scr, "◉Dundry") || !strings.Contains(scr, "RX 24H") {
		t.Errorf("the list marks watched nodes:\n%s", scr)
	}

	// Without a saved password the watch can't log in, and Health says why.
	h.watchTick()
	if !h.m.adm.noPassword[dundry.Key] {
		t.Fatal("no password to poll with")
	}
	h.keys("H")
	scr := h.screen()
	if h.m.adm.pane != paneHealth || !strings.Contains(scr, "◉ WATCHED") || !strings.Contains(scr, "password isn't saved") || !strings.Contains(scr, "No polls kept") {
		t.Fatalf("health without a login:\n%s", scr)
	}
	h.keys("u")
	if !strings.Contains(h.lastLog(), "log in to Dundry first") {
		t.Errorf("poll now needs a login: %s", h.lastLog())
	}
	h.key(tea.KeyEscape)

	// With one, each poll logs in if need be and keeps what the node says.
	h.m.opt.Keychain.Set(dundry.Key, "password")
	h.clk.t = h.clk.t.Add(11 * time.Minute)
	h.watchTick()
	h.until("the first poll", func() bool { return len(h.m.historyOf(dundry.Key)) == 1 })
	h.watchTick()
	if len(h.m.historyOf(dundry.Key)) != 1 {
		t.Error("not due again yet")
	}
	h.clk.t = h.clk.t.Add(11 * time.Minute)
	h.watchTick()
	h.until("the second poll", func() bool { return len(h.m.historyOf(dundry.Key)) == 2 })
	if rows, _ := h.m.opt.Store.Status(dundry.Key, time.Time{}); len(rows) != 2 {
		t.Errorf("stored = %d", len(rows))
	}
	if strings.TrimSpace(h.m.trend(dundry.Key, 12)) == "" {
		t.Error("the list shows a trend once there are rates")
	}
	h.keys("H")
	scr = h.screen()
	for _, want := range []string{"RECEIVED", "DUPLICATES", "NOISE FLOOR", "2 polls in this window", "-24h", "last poll"} {
		if !strings.Contains(scr, want) {
			t.Errorf("health lacks %q:\n%s", want, scr)
		}
	}

	// A watch poll that gets no answer is noted quietly.
	h.m.adm.background[999] = true
	h.m.watchMissed(999, "no answer")
	if !strings.Contains(h.lastLog(), "WATCH  a watched node: no answer; trying again in 10m") || h.m.adm.background[999] {
		t.Errorf("missed: %s", h.lastLog())
	}

	// Clearing the history asks first.
	h.keys("C")
	h.typeLine("Dundry")
	if len(h.m.historyOf(dundry.Key)) != 0 || !strings.Contains(h.lastLog(), "HISTORY CLEARED") {
		t.Errorf("cleared: %s", h.lastLog())
	}
	if rows, _ := h.m.opt.Store.Status(dundry.Key, time.Time{}); len(rows) != 0 {
		t.Error("cleared from the store")
	}
	h.keys("C")
	if !strings.Contains(h.lastLog(), "there's none kept") {
		t.Error("nothing to clear")
	}
	h.keys("w")
	if h.m.watched(dundry.Key) || !strings.Contains(h.lastLog(), "UNWATCHED") {
		t.Error("unwatch from the node")
	}
	if !strings.Contains(h.screen(), "○ NOT WATCHED") {
		t.Error("health shows it's not watched")
	}
	h.keys("w")
	h.key(tea.KeyEscape)
	h.keys("w")
	if h.m.watched(dundry.Key) {
		t.Error("unwatch from the list")
	}
}

func TestWatchWaitsItsTurn(t *testing.T) {
	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Dundry", "password")
	h.idle(sess)
	h.keys("w")
	h.clk.t = h.clk.t.Add(time.Hour)
	h.m.linked = linkLost
	h.watchTick()
	h.m.linked = linkUp
	if _, running := h.m.admin.Running(); running {
		t.Error("no polls while the link is down")
	}
	j := h.m.admin.RequestTelemetry(h.contact("Kelston"), h.m.now)
	if h.m.pollWatched() != nil {
		t.Error("no polls while the radio is busy")
	}
	h.m.admin.TimedOut(j.ID, h.m.now)
	if h.m.adminMsg(watchTickMsg{h.m.adm.watchGen - 1}) != nil {
		t.Error("a tick from an earlier connection is ignored")
	}
}

func TestHealthRangesAndCharts(t *testing.T) {
	if got := axis(30, false); !strings.HasPrefix(got, "-24h") || !strings.HasSuffix(got, "now") || len([]rune(got)) != 30 {
		t.Errorf("day axis %q", got)
	}
	if got := axis(36, true); !strings.HasPrefix(got, "-7d") || !strings.Contains(got, "-3d") {
		t.Errorf("week axis %q", got)
	}
	for v, want := range map[float64]string{1234.4: "1,234/h", 5.25: "5.2%", 42: "42%"} {
		unit := "/h"
		if v < 100 {
			unit = "%"
		}
		if got := measure(v, unit); got != want {
			t.Errorf("measure %v = %q", v, got)
		}
	}
	if measure(4.1, "V") != "4.10 V" || measure(-112, "dBm") != "-112 dBm" {
		t.Error("levels")
	}
	if every(10*time.Minute) != "10m" || every(90*time.Second) != "90s" {
		t.Error("every")
	}
	if spanText(3*24*time.Hour) != "3 days" || spanText(90*time.Minute) != "1h 30m" || spanText(5*time.Minute) != "5m" {
		t.Error("spans")
	}
	if plural(1, "poll") != "1 poll" || plural(1200, "poll") != "1,200 polls" {
		t.Error("plural")
	}
	lo, hi := seriesRange(admin.Series{Unit: "V", Values: []float64{4.1, 4.1}})
	if math.Abs(lo-3.1) > 1e-9 || math.Abs(hi-5.1) > 1e-9 {
		t.Errorf("a flat level gets room either side: %v %v", lo, hi)
	}
}

func TestHealthGradesAndLines(t *testing.T) {
	m, _ := newTestModel(t, motion.Full, 100, 30)
	node := [32]byte{7}
	for _, c := range []struct {
		name string
		v    float64
		want grade
	}{
		{"Received", 120, veryGood}, {"Received", 0, bad}, {"Transmitting", 2, good}, {"Transmitting", 12, bad},
		{"Receive errors", 8, average}, {"Noise floor", -118, veryGood}, {"Noise floor", -90, bad},
		{"Battery", 4.1, veryGood}, {"Battery", 3.45, average}, {"Duplicates", math.NaN(), ungraded}, {"Uptime", 5, ungraded},
	} {
		if got := m.healthGrade(node, c.name, c.v); got != c.want {
			t.Errorf("%s %v graded %d, want %d", c.name, c.v, got, c.want)
		}
	}
	m.adminConfig().SetBattery(radio.KeyHex(node[:]), string(radio.Wired))
	if m.healthGrade(node, "Battery", 4.1) != ungraded {
		t.Error("a wired node's supply isn't graded")
	}

	th := m.th
	if pale := m.gradeColour(good); pale == th.Green || pale == th.Bg || m.gradeColour(average) != th.Yellow || m.gradeColour(bad) != th.Red {
		t.Error("pale green, strong green, amber, red")
	}
	red := func(float64) theme.RGB { return th.Red }
	rows := m.lineChart([]float64{0, 1, math.NaN(), 2, 3}, 0, 3, 6, 2, red, 1)
	got := plain(rows)
	if len(rows) != 2 || !strings.ContainsFunc(got, func(r rune) bool { return r > 0x2800 && r <= 0x28FF }) || !strings.Contains(got, "·") {
		t.Errorf("a line with a gap:\n%s", got)
	}
	if !strings.Contains(strings.Join(rows, ""), fmt.Sprintf("38;2;%d;%d;%d", th.Red.R, th.Red.G, th.Red.B)) {
		t.Error("cells take their value's colour")
	}
	if got := plain(m.lineChart([]float64{1, 2, 3}, 0, 3, 3, 1, red, 0)); strings.TrimSpace(got) != "" {
		t.Errorf("nothing is drawn before the reveal: %q", got)
	}
}

func TestEmptyPacketLog(t *testing.T) {
	h := newHarness(t)
	sess := &admin.Session{PacketLog: &admin.PacketLog{}, PacketLogAt: h.m.now}
	if out := plain(h.m.packetLogLines(sess, 100, 30)); !strings.Contains(out, "The log is empty") {
		t.Errorf("empty log: %s", out)
	}
}

func TestConnectModes(t *testing.T) {
	cfg, err := config.Load(t.TempDir() + "/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	cfg.Remember(config.Device{Name: "Kitchen", Kind: "TCP", Addr: "10.0.0.4", LastUsed: now.Add(-2 * time.Hour)})
	cfg.Remember(config.Device{Name: "MeshCore-k4", Kind: "USB", Addr: "/dev/cu.a", LastUsed: now.Add(-time.Hour)})
	cfg.Remember(config.Device{Name: "Dundry", Kind: "USB", Addr: "/dev/cu.b", Mode: ModeRepeater, LastUsed: now})
	ports := func() []Target {
		return []Target{{Kind: "USB", Addr: "/dev/cu.a"}, {Kind: "USB", Addr: "/dev/cu.b"}, {Kind: "USB", Addr: "/dev/cu.c"}}
	}
	var dialled Target
	m := New(Options{Theme: theme.Neon, Motion: motion.Full, Clock: func() time.Time { return now }, Config: cfg, Ports: ports,
		Dial: func(t Target) radio.Radio { dialled = t; return nil }})
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	scr := plain(screen(t, m))
	if m.connect.mode != ModeRepeater || !strings.Contains(scr, "▌REPEATER ADMIN▐") || !strings.Contains(scr, "plugged in by USB") {
		t.Fatalf("starts in the mode used last:\n%s", scr)
	}
	addrs := func() string {
		var out []string
		for _, it := range m.connect.items {
			out = append(out, it.Kind+" "+it.Addr+" "+it.Mode)
		}
		return strings.Join(out, ", ")
	}
	if got := addrs(); got != "USB /dev/cu.b repeater, USB /dev/cu.a repeater, USB /dev/cu.c repeater, SIM demo repeater" {
		t.Errorf("repeater list = %s", got)
	}
	m.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	if got := addrs(); m.connect.mode != "" || got != "USB /dev/cu.a , TCP 10.0.0.4 , USB /dev/cu.b , USB /dev/cu.c , TCP  , SIM demo " {
		t.Errorf("companion list = %s", got)
	}
	if !m.animating() {
		t.Error("the new mode decodes in")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	func() {
		defer func() { recover() }() // the fake dial returns no radio
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	}()
	if dialled.Addr != "/dev/cu.c" || dialled.Mode != ModeRepeater {
		t.Errorf("a port chosen in repeater mode opens its console: %+v", dialled)
	}
}

func TestRepeaterModeIsRemembered(t *testing.T) {
	h := newHarness(t)
	cfg, _ := config.Load(t.TempDir() + "/config.toml")
	h.m.opt.Config = cfg
	h.m.target = Target{Kind: "USB", Addr: "/dev/cu.b"}
	h.m.apply(radio.Ready{Console: true, Self: radio.SelfInfo{Name: "Dundry"}})
	if d := cfg.Devices[0]; d.Mode != ModeRepeater || d.Name != "Dundry" {
		t.Errorf("saved = %+v", d)
	}
	h.m.adm.local = true
	h.m.lay = newLayout(4)
	cfg.UI.Layout = "right 50 (*chats, packets)"
	h.m.dial(Target{Kind: "SIM", Addr: "demo"})
	if h.m.repeaterMode() || h.m.lay.String() != "right 50 (*chats, packets)" {
		t.Errorf("a companion gets its own screens back: %s", h.m.lay.String())
	}
	if err := h.m.splitPane(sideBySide, 3); err == nil {
		h.m.adm.local = true
		if err := h.m.splitPane(sideBySide, 6); err == nil || !strings.Contains(err.Error(), "companion mode") {
			t.Errorf("split on a repeater: %v", err)
		}
	}
}

func TestHistoryStoreFailures(t *testing.T) {
	h := newHarness(t)
	ct := h.contact("Dundry")
	sess := h.m.admin.Session(ct)
	sess.Status = append(sess.Status, admin.StatusPoint{At: h.m.now})
	h.m.opt.Store.Close()
	h.m.keepStatus(sess, []byte{1})
	if !strings.Contains(plainLog(h), "HISTORY  couldn't read it") || !strings.Contains(h.lastLog(), "HISTORY NOT SAVED") {
		t.Errorf("store errors are reported:\n%s", plainLog(h))
	}
	h.m.keepStatus(sess, []byte{1})
	if strings.Count(plainLog(h), "HISTORY NOT SAVED") != 1 {
		t.Error("only once")
	}
	h.m.clearHistory(ct)
	if !strings.Contains(h.lastLog(), "couldn't clear it") {
		t.Error("clear fails")
	}
	h.m.keepStatus(&admin.Session{}, nil)
	if h.m.backgroundJob(nil) != nil {
		t.Error("no job")
	}
	h.m.opt.Store = nil
	delete(h.m.adm.kept, ct.Key)
	h.m.keepStatus(sess, nil)
	if len(h.m.historyOf(ct.Key)) != 1 {
		t.Error("kept in memory without a store")
	}
}
