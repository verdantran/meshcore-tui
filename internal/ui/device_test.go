package ui

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/companion"

	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/theme"
)

// moveTo selects a Device row by id.
func (h *harness) moveTo(id string) devRow {
	h.t.Helper()
	for i, r := range h.m.deviceRows() {
		if r.id == id {
			h.m.device.sel = i
			return r
		}
	}
	h.t.Fatalf("no row %s", id)
	return devRow{}
}

func (h *harness) settings() radio.Settings {
	h.t.Helper()
	s, err := h.s.Settings(t0ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

// clear empties the input being typed; backspace starts no radio calls, so nothing needs to settle.
func (h *harness) clear() {
	for range 40 {
		h.m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
}

func (h *harness) lastLog() string { return h.m.log[len(h.m.log)-1].text }

// edit opens a row's editor, replaces what's there, and sends it.
func (h *harness) edit(id, value string) {
	h.t.Helper()
	h.moveTo(id)
	h.key(tea.KeyEnter)
	h.clear()
	h.typeLine(value)
}

func (h *harness) reloaded() {
	h.t.Helper()
	h.until("the device to settle", func() bool { return len(h.m.device.busy) == 0 && !h.m.device.loading })
}

func TestDeviceScreen(t *testing.T) {
	h := newHarness(t)
	h.keys("6")
	h.until("settings", func() bool { return h.m.device.loaded })
	scr := h.screen()
	for _, want := range []string{"DEVICE · MeshCore-k4 · Heltec V4", "Battery 4.02 V ████░▌ 80%", "412 of 1536 KB", "EU/UK narrow (the current default)",
		"869.618 MHz", "contacts given permission", "bristol"} {
		if !strings.Contains(scr, want) {
			t.Errorf("device screen lacks %q:\n%s", want, scr)
		}
	}
	h.keys("jk")

	h.edit("name", "")
	if !strings.Contains(h.lastLog(), "1 to 31 bytes") || h.m.device.prompt != devEdit {
		t.Fatal("an empty name is refused and the editor stays open")
	}
	h.clear()
	h.typeLine("k4-portable")
	h.reloaded()
	if h.settings().Self.Name != "k4-portable" || !strings.Contains(h.screen(), "k4-portable") {
		t.Fatal("name")
	}
	for _, bad := range []string{"51.4", "x, 1", "51, y", "95, 0"} {
		h.edit("location", bad)
		h.key(tea.KeyEscape)
	}
	h.edit("location", "51.4545, -2.5879")
	h.reloaded()
	if s := h.settings(); s.Self.Lat != 51.4545 || s.Self.Lon != -2.5879 {
		t.Errorf("location = %v, %v", s.Self.Lat, s.Self.Lon)
	}
	h.moveTo("locpolicy")
	h.key(tea.KeyEnter)
	h.reloaded()
	if h.settings().Other.AdvertLocPolicy != radio.LocNone {
		t.Error("location sharing off")
	}
	h.moveTo("advert1")
	h.key(tea.KeyEnter)
	h.reloaded()
	if !strings.Contains(h.lastLog(), "ADVERT SENT  flood") {
		t.Error("flood advert")
	}
	h.moveTo("advert0")
	h.key(tea.KeyEnter)

	// Radio changes ask first; presets set all four values together.
	h.edit("sf", "13")
	h.key(tea.KeyEscape)
	h.edit("bw", "60")
	h.key(tea.KeyEscape)
	h.edit("cr", "x")
	h.key(tea.KeyEscape)
	h.edit("freq", "2400")
	h.key(tea.KeyEscape)
	h.edit("sf", "9")
	if !strings.Contains(h.screen(), "CHANGE THE RADIO SETTINGS") {
		t.Fatalf("radio confirm:\n%s", h.screen())
	}
	h.keys("n")
	if h.settings().Self.SF != 8 {
		t.Fatal("cancelled")
	}
	h.edit("bw", "250")
	h.keys("y")
	h.reloaded()
	if h.settings().Radio.BwHz != 250000 || !strings.Contains(h.screen(), "custom") {
		t.Fatalf("bandwidth = %+v", h.settings().Radio)
	}
	h.moveTo("preset")
	h.key(tea.KeyEnter)
	if !strings.Contains(h.screen(), "RADIO PRESETS") {
		t.Fatalf("presets:\n%s", h.screen())
	}
	h.keys("jjjk")
	h.m.device.pick = 0
	h.key(tea.KeyEnter)
	h.keys("y")
	h.reloaded()
	if p := h.settings().Radio; p.BwHz != 62500 || p.SF != 8 {
		t.Fatalf("preset = %+v", p)
	}
	h.moveTo("repeat")
	h.key(tea.KeyEnter)
	h.reloaded()
	if !h.settings().Radio.Repeat {
		t.Fatal("repeat is allowed on 869.618 MHz")
	}
	h.edit("freq", "868.1")
	h.keys("y")
	h.reloaded()
	if s := h.settings(); s.Radio.Repeat || s.Self.FreqKHz != 868100 {
		t.Errorf("moving off an allowed frequency turns repeat off: %+v", s.Radio)
	}
	h.moveTo("repeat")
	h.key(tea.KeyEnter)
	if !strings.Contains(h.lastLog(), "not allowed on 868.1 MHz") {
		t.Errorf("repeat refused: %s", h.lastLog())
	}
	h.edit("tx", "30")
	h.key(tea.KeyEscape)
	h.edit("tx", "14")
	h.reloaded()
	if h.settings().Self.TxPower != 14 {
		t.Error("tx power")
	}

	h.edit("rxdelay", "-1")
	h.key(tea.KeyEscape)
	h.edit("rxdelay", "5")
	h.edit("af", "2.5")
	h.reloaded()
	if tu := h.settings().Tuning; tu.RxDelay != 5 || tu.AirtimeFactor != 2.5 {
		t.Errorf("tuning = %+v", tu)
	}
	for _, id := range []string{"manual", "multiacks", "hashmode", "telbase", "telloc", "telenv"} {
		h.moveTo(id)
		h.key(tea.KeyEnter)
		h.reloaded()
	}
	s := h.settings()
	if !s.Other.ManualAdd || s.Other.MultiAcks != 1 || s.PathHashMode != 1 || s.Other.TelemetryBase != radio.ShareEveryone ||
		s.Other.TelemetryLoc != radio.SharePerContact {
		t.Errorf("toggles = %+v %d", s.Other, s.PathHashMode)
	}
	h.edit("scope", "two words")
	h.key(tea.KeyEscape)
	h.edit("scope", "#bath")
	h.reloaded()
	if h.settings().Scope != "bath" {
		t.Error("scope")
	}
	h.edit("scope", "")
	h.reloaded()
	if h.settings().Scope != "" || !strings.Contains(h.screen(), "none") {
		t.Error("no scope")
	}

	h.edit("pin", "12345")
	h.key(tea.KeyEscape)
	h.edit("pin", "246810")
	h.reloaded()
	if h.settings().Pin != 246810 {
		t.Error("pin")
	}
	h.edit("clock", "")
	h.moveTo("var:gps")
	h.key(tea.KeyEnter)
	h.clear()
	h.typeLine("1")
	h.reloaded()
	if vs, _ := h.s.CustomVars(t0ctx); vs[0].Value != "1" {
		t.Error("custom var")
	}
	h.keys("u")
	h.reloaded()
	h.moveTo("clock")
	h.key(tea.KeyEnter)
	h.reloaded()
	if !strings.Contains(h.lastLog(), "CLOCK SYNCED") {
		t.Error("clock sync")
	}
}

var t0ctx = context.Background()

func TestDeviceSecurity(t *testing.T) {
	h := newHarness(t)
	h.keys("6")
	h.until("settings", func() bool { return h.m.device.loaded })

	h.moveTo("export")
	h.key(tea.KeyEnter)
	if !strings.Contains(h.screen(), "SHOW THE PRIVATE KEY") {
		t.Fatalf("export asks:\n%s", h.screen())
	}
	h.keys("y")
	h.until("key", func() bool { return h.m.device.key != "" })
	if !strings.Contains(h.screen(), "5ec27a") {
		t.Fatalf("key shown:\n%s", h.screen())
	}
	for _, l := range h.m.log {
		if strings.Contains(l.text, "5ec27a") {
			t.Fatal("the key never reaches the log")
		}
	}
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	if cmd == nil || !strings.Contains(h.lastLog(), "COPIED") {
		t.Error("c copies")
	}
	h.keys("x")
	if h.m.device.key != "" || h.m.device.prompt != devNone {
		t.Error("any other key hides it")
	}

	h.edit("import", "abcd")
	if h.m.device.prompt != devEdit || !strings.Contains(h.lastLog(), "128 hex characters") {
		t.Fatal("short key refused")
	}
	h.clear()
	for _, r := range strings.Repeat("ab", 64) {
		h.m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if strings.Contains(h.screen(), "abab") {
		t.Fatal("a typed key isn't shown")
	}
	h.key(tea.KeyEnter)
	if !strings.Contains(h.screen(), "Type import to go ahead") {
		t.Fatalf("import asks:\n%s", h.screen())
	}
	h.typeLine("imprt")
	if !strings.Contains(h.lastLog(), "NOT CONFIRMED") {
		t.Error("misspelt")
	}
	h.clear()
	h.typeLine("import")
	h.reloaded()
	if k, _ := h.s.ExportKey(t0ctx); k[0] != 0xAB {
		t.Error("imported")
	}

	h.moveTo("reboot")
	h.key(tea.KeyEnter)
	h.keys("y")
	h.until("reboot and back", func() bool {
		return strings.Contains(h.screen(), "LINK RESTORED") || h.m.linked == linkUp && strings.Contains(plainLog(h), "LINK RESTORED")
	})
	h.until("re-read after the reboot", func() bool { return h.m.device.loaded })

	h.moveTo("reset")
	h.key(tea.KeyEnter)
	h.typeLine("reset")
	h.until("reset and back", func() bool { return strings.Count(plainLog(h), "LINK RESTORED") == 2 })
	h.until("defaults", func() bool { return h.m.device.loaded && h.m.device.s.Self.Name == "MeshCore-k4" })

	// Any key but y cancels a y/n question, and esc closes the others.
	h.moveTo("export")
	h.key(tea.KeyEnter)
	h.keys("n")
	h.moveTo("preset")
	h.key(tea.KeyEnter)
	h.key(tea.KeyEscape)
	if h.m.device.prompt != devNone {
		t.Error("esc")
	}
}

func plainLog(h *harness) string {
	var b strings.Builder
	for _, l := range h.m.log {
		b.WriteString(l.text + "\n")
	}
	return b.String()
}

func TestDeviceFailures(t *testing.T) {
	h := newHarness(t)
	h.keys("6")
	h.until("settings", func() bool { return h.m.device.loaded })
	h.m.Update(devOpMsg{id: "name", what: "NAME", err: errors.New("radio busy")})
	if !strings.Contains(h.lastLog(), "NAME  radio busy") || !strings.Contains(h.screen(), "[FAIL]") {
		t.Errorf("failed op:\n%s", h.screen())
	}
	h.m.Update(keyExportedMsg{err: errors.New("refused")})
	h.m.Update(devLoadedMsg{err: errors.New("timed out")})
	h.m.device.loaded = false
	if !strings.Contains(h.screen(), "Couldn't read the settings: timed out") {
		t.Errorf("load failure:\n%s", h.screen())
	}
	h.m.device.err = nil
	h.m.device.loading = true
	if !strings.Contains(h.screen(), "reading the radio's settings") {
		t.Errorf("loading:\n%s", h.screen())
	}
	h.m.device.loading = false
	h.keys("u")
	h.reloaded()
	h.m.device.s.RepeatFreqs = nil
	if repeatHint(h.m.device.s) != "This firmware doesn't allow the companion to repeat." {
		t.Error("no repeat ranges")
	}
	h.m.device.clock = time.Time{}
	if h.m.clockText() != "unknown" {
		t.Error("no clock")
	}
	h.m.device.clock, h.m.device.clockAt = h.m.now.Add(-time.Minute), h.m.now
	if !strings.Contains(h.m.clockText(), "1m0s behind") {
		t.Errorf("behind: %s", h.m.clockText())
	}
	h.m.device.clock = h.m.now.Add(time.Minute)
	if !strings.Contains(h.m.clockText(), "ahead") {
		t.Error("ahead")
	}
	h.keys("?")
	if !strings.Contains(h.screen(), "radio changes, a new identity") {
		t.Errorf("help:\n%s", h.screen())
	}
}

func advertPacket(t *testing.T, name string, path []byte) []byte {
	t.Helper()
	adv, err := meshcore.NewAdvert(meshcore.NewLocalIdentityFromSeed(sha256.Sum256([]byte(name))), meshcore.AdvertAppData{Type: "REPEATER", Name: name}, 1760000000)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := adv.ToBytes()
	return rawPacket(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, path, payload)
}

func TestStatsScreen(t *testing.T) {
	h := newHarness(t)
	h.keys("7")
	if !strings.Contains(h.screen(), "Nothing heard yet") {
		t.Fatalf("empty mesh:\n%s", h.screen())
	}
	h.until("a sample", func() bool { return len(h.m.rstats.hist) == 1 })
	h.clk.t = h.clk.t.Add(statsEvery)
	_, cmd := h.m.Update(statsTickMsg{})
	h.start(cmd)
	h.until("two samples", func() bool { return len(h.m.rstats.hist) == 2 })

	h.hear(advertPacket(t, "Purdown", []byte{0xA3}), 9)
	h.hear(advertPacket(t, "Dundry", nil), 2)
	h.hear(channelPacket(t, h, "#bristol", "Carol", "morning", []byte{0xA3, 0x1E}), -12)
	h.hear(rawPacket(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpTxt, []byte{1, 2, 3, 4, 5, 6, 7, 8}, []byte{0xEE, 1, 2, 3, 4, 5}), 12)
	h.advance(time.Second)
	scr := h.screen()
	for _, want := range []string{"THIS RADIO", "Uptime 3h", "Battery 4.02 V", "Received", "/min", "Receive errors",
		"THE MESH, FROM WHAT WE'VE HEARD  4 packets", "2 this hour", "A3 Dundry", "Hops travelled", "7+ hops", "below -10 dB",
		"+10 dB and up", "#bristol", "(unknown channel)"} {
		if !strings.Contains(scr, want) {
			t.Errorf("stats lack %q:\n%s", want, scr)
		}
	}
	h.m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.screen()
	h.m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	h.screen()

	// Leaving the screen stops the polling.
	h.keys("1")
	h.m.Update(statsMsg{s: radio.Stats{}})
	if h.m.rstats.polling {
		t.Error("a reply after leaving stops polling")
	}
	h.m.rstats.polling = true
	h.m.Update(statsTickMsg{})
	if h.m.rstats.polling {
		t.Error("a tick after leaving stops polling")
	}
	h.keys("7")
	h.m.Update(statsMsg{err: errors.New("busy")})
	h.m.rstats.hist = nil
	if !strings.Contains(h.screen(), "couldn't read the counters: busy") {
		t.Errorf("error:\n%s", h.screen())
	}
	if _, ok := rate(nil, nil); ok {
		t.Error("rate needs two samples")
	}
	if busy(statSample{at: h.m.now}, statSample{at: h.m.now}) != 0 {
		t.Error("no time, no busy")
	}
	h.keys("?")
	if !strings.Contains(h.screen(), "read every 5s") {
		t.Errorf("help:\n%s", h.screen())
	}
}

func TestBatteryIcon(t *testing.T) {
	th := theme.Neon
	colour := map[string]theme.RGB{}
	paint := func(s string, c theme.RGB, _ bool) string {
		if strings.Contains(s, "█") {
			colour[s] = c
		}
		return s
	}
	for _, c := range []struct {
		pct  int
		want string
		col  theme.RGB
	}{
		{100, "█████▌", th.Green}, {80, "████░▌", th.Green}, {56, "███░░▌", th.Green}, {35, "██░░░▌", th.Yellow},
		{12, "█░░░░▌", th.Red}, {3, "█░░░░▌", th.Red}, {0, "░░░░░▌", th.Red},
	} {
		got := batteryIcon(th, c.pct, paint)
		if got != c.want {
			t.Errorf("%d%% = %q, want %q", c.pct, got, c.want)
		}
		if fill := strings.TrimRight(got, "░▌"); fill != "" && colour[fill] != c.col {
			t.Errorf("%d%% is the wrong colour", c.pct)
		}
	}
}

func TestBatteryIconsEverywhere(t *testing.T) {
	m, c := newTestModel(t, motion.Full, 100, 30)
	send(m, c, 0, radio.BootStep{Key: "power", Label: "POWER", Detail: "3.61 V · 34%", State: radio.StepOK})
	send(m, c, 2*time.Second, radio.BootStep{Key: "x", Label: "X", Detail: "no percentage", State: radio.StepOK})
	if got := plain(screen(t, m)); !strings.Contains(got, "3.61 V · ██░░░▌ 34%") {
		t.Errorf("boot POWER line:\n%s", got)
	}
	if _, _, ok := batteryPercent(bootLine{key: "power", state: radio.StepOK}, true, "nospace"); ok {
		t.Error("no percentage to find")
	}
	if _, _, ok := batteryPercent(bootLine{key: "power", state: radio.StepOK}, true, "3.6 V · lots"); ok {
		t.Error("not a number")
	}

	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Dundry", "password")
	h.idle(sess)
	if !strings.Contains(h.screen(), "Battery 4.08 V ████░▌ 86%") {
		t.Errorf("repeater status:\n%s", h.screen())
	}
	h.key(tea.KeyEscape)
	if !strings.Contains(h.screen(), "4.08 V ████░▌ 86%  12d 4h") {
		t.Errorf("fleet list:\n%s", h.screen())
	}
}

func TestFramesPanelHidesSecrets(t *testing.T) {
	h := newHarness(t)
	h.key(tea.KeyF12)
	cmd := companion.ImportPrivateKeyCommand{PrivateKey: [64]byte{0xDE, 0xAD, 0xBE, 0xEF}}.ToBytes()
	h.m.Update(eventMsg{radio.CommandFrame(h.m.now, cmd)})
	scr := h.screen()
	if !strings.Contains(scr, "FRAMES") || !strings.Contains(scr, "18 ·· ·· ··") || strings.Contains(scr, "DE AD BE EF") {
		t.Errorf("frames panel:\n%s", scr)
	}
}
