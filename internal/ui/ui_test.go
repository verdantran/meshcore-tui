package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

// stubRadio feeds nothing; tests push events straight into Update.
type stubRadio struct{ ch chan radio.Event }

func (s *stubRadio) Start(context.Context)      {}
func (s *stubRadio) Events() <-chan radio.Event { return s.ch }
func (s *stubRadio) Close() error               { return nil }
func (s *stubRadio) Describe() string           { return "/dev/cu.test" }
func (s *stubRadio) Transport() string          { return "USB" }
func (s *stubRadio) SendText(context.Context, [32]byte, string) (radio.Sent, error) {
	return radio.Sent{Ack: 1, Timeout: time.Second}, nil
}
func (s *stubRadio) SendChannel(context.Context, int, string) error { return nil }
func (s *stubRadio) ResetPath(context.Context, [32]byte) error      { return nil }
func (s *stubRadio) SyncContacts(context.Context) ([]radio.Contact, error) {
	return nil, nil
}
func (s *stubRadio) SaveContact(context.Context, radio.Contact) error { return nil }
func (s *stubRadio) RemoveContact(context.Context, [32]byte) error    { return nil }
func (s *stubRadio) Trace(context.Context, []byte) (uint32, error)    { return 1, nil }
func (s *stubRadio) DiscoverPath(context.Context, [32]byte) error     { return nil }
func (s *stubRadio) ImportContact(context.Context, string) error      { return nil }
func (s *stubRadio) SetAutoAdd(context.Context, radio.AutoAdd) error  { return nil }
func (s *stubRadio) AutoAdd(context.Context) (radio.AutoAdd, error)   { return radio.AutoAdd{}, nil }
func (s *stubRadio) ExportContact(context.Context, [32]byte) (string, error) {
	return radio.LinkPrefix + "00", nil
}
func (s *stubRadio) AdvertPath(context.Context, [32]byte) (radio.AdvertPath, error) {
	return radio.AdvertPath{}, nil
}
func (s *stubRadio) Login(context.Context, [32]byte, string) error     { return nil }
func (s *stubRadio) Logout(context.Context, [32]byte) error            { return nil }
func (s *stubRadio) RequestStatus(context.Context, [32]byte) error     { return nil }
func (s *stubRadio) RequestTelemetry(context.Context, [32]byte) error  { return nil }
func (s *stubRadio) SendCLI(context.Context, [32]byte, string) error   { return nil }
func (s *stubRadio) Device() radio.Device                              { return nil }
func (s *stubRadio) Channels(context.Context) ([]radio.Channel, error) { return nil, nil }
func (s *stubRadio) SetChannel(context.Context, radio.Channel) error   { return nil }
func (s *stubRadio) BinaryRequest(context.Context, [32]byte, []byte) (uint32, error) {
	return 1, nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestModel(t *testing.T, level motion.Level, w, h int) (*Model, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 10, 13, 10, 3, 0, 0, time.UTC)}
	m := New(Options{
		Theme: theme.Neon, Motion: level, Clock: c.now,
		Dial:  func(Target) radio.Radio { return &stubRadio{ch: make(chan radio.Event)} },
		Start: &Target{Kind: "USB", Addr: "/dev/cu.test"},
	})
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m, c
}

// tick advances the clock and delivers a frame, as the frame clock would.
func tick(m *Model, c *clock, d time.Duration) {
	c.t = c.t.Add(d)
	m.Update(frameMsg(c.t))
}

// bootDone steps the clock until the boot log's closing pause has run out.
func bootDone(t *testing.T, m *Model, c *clock) {
	t.Helper()
	for i := 0; i < 20 && m.phase != phaseMain; i++ {
		tick(m, c, 500*time.Millisecond)
	}
	if m.phase != phaseMain {
		t.Fatal("still in boot after the handover delay")
	}
}

func send(m *Model, c *clock, after time.Duration, e radio.Event) {
	c.t = c.t.Add(after)
	m.Update(eventMsg{e})
}

func screen(t testing.TB, m *Model) []string {
	t.Helper()
	lines := strings.Split(m.View().Content, "\n")
	if len(lines) > m.h {
		t.Fatalf("rendered %d rows for a %d-row terminal", len(lines), m.h)
	}
	for i, l := range lines {
		if w := renderWidth.StringWidth(l); w != m.w {
			t.Fatalf("row %d is %d cells wide, want %d: %q", i, w, m.w, ansi.Strip(l))
		}
	}
	return lines
}

func plain(lines []string) string { return ansi.Strip(strings.Join(lines, "\n")) }

var ready = radio.Ready{
	Self:     radio.SelfInfo{Name: "MeshCore-k4", FreqKHz: 869618, BwHz: 62500, SF: 8},
	Device:   radio.DeviceInfo{Model: "Heltec V4", Firmware: "v1.17.1"},
	Contacts: 47, Channels: 3, Battery: radio.Battery{MilliVolts: 4020},
}

func boot(m *Model, c *clock) {
	send(m, c, 0, radio.BootStep{Key: "link", Label: "LINK", State: radio.StepWait})
	send(m, c, 200*time.Millisecond, radio.BootStep{Key: "link", Label: "LINK", Detail: "/dev/cu.test", State: radio.StepOK})
	send(m, c, 200*time.Millisecond, radio.BootStep{Key: "hello", Label: "HELLO", Detail: "MeshCore-k4", State: radio.StepOK})
	send(m, c, 500*time.Millisecond, ready)
}

func TestBootThenMainScreen(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 60}} {
		m, c := newTestModel(t, motion.Full, size[0], size[1])
		boot(m, c)
		got := plain(screen(t, m))
		for _, want := range []string{"LINK SEQUENCE", "LINK", "[ OK ]", "LINK ESTABLISHED"} {
			if !strings.Contains(got, want) {
				t.Errorf("%v boot screen missing %q:\n%s", size, want, got)
			}
		}

		tick(m, c, time.Second)
		if m.phase != phaseBoot {
			t.Fatalf("%v: the finished boot log stays up long enough to read", size)
		}
		bootDone(t, m, c)
		send(m, c, 100*time.Millisecond, radio.Packet{SNR: 7.25, RSSI: -92, Raw: []byte{0x15, 0x02, 0xA3, 0x5F, 0x01, 0x02}})
		send(m, c, 0, radio.Log{Text: "PATH UPDATED  Alice → A3", Tone: radio.ToneOK})
		tick(m, c, 2*time.Second)
		got = plain(screen(t, m))
		for _, want := range []string{"MeshCore-k4", "Heltec V4", "869.618 SF8 BW62.5", "LINKED", "1 CHATS", "SYSTEM LOG", "PATH UPDATED", "SNR +7.25"} {
			if !strings.Contains(got, want) {
				t.Errorf("%v main screen missing %q:\n%s", size, want, got)
			}
		}
	}
}

func TestTabsDebugAndMotionOff(t *testing.T) {
	m, c := newTestModel(t, motion.Off, 100, 30)
	boot(m, c)
	if m.phase != phaseMain {
		t.Fatal("motion off should go straight to the main screen")
	}
	send(m, c, 0, radio.Frame{Dir: radio.ToRadio, Code: 22, Name: "DEVICE_QUERY", Bytes: []byte{22, 3}})
	m.Update(tea.KeyPressMsg{Code: '4', Text: "4"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyF12})
	got := plain(screen(t, m))
	for _, want := range []string{"PACKETS", "FRAMES", "DEVICE_QUERY", "16 03"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if m.animating() {
		t.Error("nothing should animate with motion off")
	}
}

func TestFailedBootStopsTicking(t *testing.T) {
	m, c := newTestModel(t, motion.Full, 100, 30)
	send(m, c, 0, radio.BootStep{Key: "link", Label: "LINK", State: radio.StepWait})
	send(m, c, 100*time.Millisecond, radio.BootStep{Key: "link", Label: "LINK", Detail: "no such port", State: radio.StepFail})
	send(m, c, 0, radio.Fatal{Err: errors.New("open serial port: no such file")})
	tick(m, c, 2*time.Second)
	if got := plain(screen(t, m)); !strings.Contains(got, "[FAIL]") || !strings.Contains(got, "r retry") {
		t.Errorf("failed boot screen:\n%s", got)
	}
	if m.animating() {
		t.Error("a failed boot screen should stop the frame clock once the glitch ends")
	}
}

func TestIdleMainScreenStopsTicking(t *testing.T) {
	m, c := newTestModel(t, motion.Full, 100, 30)
	boot(m, c)
	bootDone(t, m, c)
	tick(m, c, 30*time.Second)
	if m.animating() {
		t.Error("an idle main screen should not tick")
	}
}

func TestReducedMotionUsesTheSlowClock(t *testing.T) {
	for _, level := range []motion.Level{motion.Full, motion.Reduced} {
		m, c := newTestModel(t, level, 100, 30)
		boot(m, c)
		bootDone(t, m, c)
		tick(m, c, 30*time.Second)
		m.stats.rxLamp = c.t
		tick(m, c, 0)
		want := beaconFrame
		if level == motion.Full {
			want = motion.FrameDur
		}
		if !m.animating() || m.frameEvery() != want {
			t.Errorf("%s: the receive lamp ticks every %v, want %v", level, m.frameEvery(), want)
		}
	}
}

func TestToastMovesOnlyWhileSliding(t *testing.T) {
	cases := []struct {
		level           motion.Level
		in, middle, out time.Duration // 0: the clock stops
	}{
		{motion.Full, motion.FrameDur, beaconFrame, motion.FrameDur},
		{motion.Reduced, beaconFrame, beaconFrame, beaconFrame},
		{motion.Off, 0, 0, 0},
	}
	for _, cs := range cases {
		m, c := newTestModel(t, cs.level, 100, 30)
		boot(m, c)
		bootDone(t, m, c)
		tick(m, c, 30*time.Second)
		m.pushToast("Alice", "are you about tonight?")
		for _, at := range []struct {
			after time.Duration
			want  time.Duration
		}{{toastSlide / 2, cs.in}, {toastFor / 2, cs.middle}, {toastFor - toastSlide/2, cs.out}} {
			c.t = m.toasts[0].at.Add(at.after)
			tick(m, c, 0)
			if got := m.frameEvery(); m.animating() != (at.want != 0) || (at.want != 0 && got != at.want) {
				t.Errorf("%s, %v in: animating %v every %v, want %v", cs.level, at.after, m.animating(), got, at.want)
			}
		}
		c.t = m.toasts[0].at.Add(toastFor)
		tick(m, c, 0)
		if m.animating() {
			t.Errorf("%s: the clock runs on after the toast has gone", cs.level)
		}
	}
}

func TestThemeCycleCrossfades(t *testing.T) {
	m, c := newTestModel(t, motion.Full, 100, 30)
	boot(m, c)
	bootDone(t, m, c)
	m.Update(tea.KeyPressMsg{Code: 'T', Text: "T"})
	if m.Theme().Name != theme.Dusk.Name {
		t.Fatalf("T should move to dusk, got %s", m.Theme().Name)
	}
	tick(m, c, themeFadeDur/2)
	if m.th.Bg == theme.Neon.Bg || m.th.Bg == theme.Dusk.Bg {
		t.Error("halfway through the fade the background should be a blend")
	}
	tick(m, c, themeFadeDur)
	if m.th.Bg != theme.Dusk.Bg {
		t.Error("the fade should finish on the new theme")
	}
	// Every theme, mono included, renders at the full width.
	for range theme.All {
		m.Update(tea.KeyPressMsg{Code: 'T', Text: "T"})
		tick(m, c, time.Second)
		if got := plain(screen(t, m)); !strings.Contains(got, "theme "+m.Theme().Name) {
			t.Errorf("footer doesn't name theme %s", m.Theme().Name)
		}
	}
}

func typeKeys(m *Model, s string) {
	for _, r := range s {
		k := tea.KeyPressMsg{Code: r, Text: string(r)}
		if r == ' ' {
			k = tea.KeyPressMsg{Code: tea.KeySpace}
		}
		m.Update(k)
	}
}

func key(m *Model, code rune) { m.Update(tea.KeyPressMsg{Code: code}) }

func TestPalette(t *testing.T) {
	m, c := newTestModel(t, motion.Full, 120, 30)
	boot(m, c)
	bootDone(t, m, c)

	typeKeys(m, ":")
	if !m.pal.open {
		t.Fatal(": should open the palette")
	}
	typeKeys(m, "th")
	key(m, tea.KeyTab)
	if m.pal.input.text != "theme " {
		t.Fatalf("tab completed to %q", m.pal.input.text)
	}
	typeKeys(m, "ic")
	key(m, tea.KeyTab)
	key(m, tea.KeyEnter)
	if m.Theme().Name != "ice" {
		t.Fatalf("theme ice didn't apply, got %s", m.Theme().Name)
	}

	typeKeys(m, ":contacts")
	key(m, tea.KeyEnter)
	if tabNames[m.tab] != "CONTACTS" {
		t.Fatalf("contacts command left tab on %s", tabNames[m.tab])
	}

	typeKeys(m, ":bogus")
	key(m, tea.KeyEnter)
	if l := m.log[len(m.log)-1]; l.tone != radio.ToneFail || !strings.Contains(l.text, "bogus") {
		t.Fatalf("unknown command logged %+v", l)
	}

	typeKeys(m, ":")
	key(m, tea.KeyUp)
	if m.pal.input.text != "bogus" {
		t.Fatalf("history up gave %q", m.pal.input.text)
	}
	got := plain(screen(t, m))
	if !strings.Contains(got, "mc▸ bogus") {
		t.Errorf("prompt not shown:\n%s", got)
	}
	key(m, tea.KeyEscape)

	typeKeys(m, "?")
	if got := plain(screen(t, m)); !strings.Contains(got, "HELP") || !strings.Contains(got, "j k scroll") {
		t.Errorf("help overlay:\n%s", got)
	}
	typeKeys(m, "G")
	if got := plain(screen(t, m)); !strings.Contains(got, "HELP · 100%") || !strings.Contains(got, "send a test notification") {
		t.Errorf("help scrolls to the commands at the end:\n%s", got)
	}
	typeKeys(m, "g")
	if m.helpScroll != 0 {
		t.Error("g goes back to the top")
	}
	key(m, tea.KeyEscape)
	if m.help {
		t.Error("esc should close help")
	}
}

func TestSavedDevices(t *testing.T) {
	cfg, err := config.Load(t.TempDir() + "/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 10, 13, 10, 3, 0, 0, time.UTC)}
	m := New(Options{
		Theme: theme.Neon, Motion: motion.Off, Clock: c.now, Config: cfg,
		Dial:  func(Target) radio.Radio { return &stubRadio{ch: make(chan radio.Event)} },
		Ports: func() []Target { return []Target{{Kind: "USB", Addr: "/dev/cu.test", Detail: "Espressif"}} },
	})
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	key(m, tea.KeyEnter) // the detected port
	boot(m, c)
	if len(cfg.Devices) != 1 || cfg.Devices[0].Name != "MeshCore-k4" || cfg.Devices[0].Addr != "/dev/cu.test" {
		t.Fatalf("device not remembered: %+v", cfg.Devices)
	}
	back, _ := config.Load(cfg.Path())
	if len(back.Devices) != 1 {
		t.Fatal("remembered device wasn't written to disk")
	}

	// A fresh session lists it once, starred, ahead of the detected ports.
	m2 := New(Options{Theme: theme.Neon, Motion: motion.Off, Clock: c.now, Config: back,
		Ports: func() []Target { return []Target{{Kind: "USB", Addr: "/dev/cu.test"}} }})
	m2.Init()
	m2.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	got := plain(screen(t, m2))
	if strings.Count(got, "/dev/cu.test") != 1 || !strings.Contains(got, "★USB") || !strings.Contains(got, "MeshCore-k4") {
		t.Errorf("connect screen:\n%s", got)
	}
	typeKeys(m2, "x")
	if len(back.Devices) != 1 || !strings.Contains(plain(screen(t, m2)), "FORGET MESHCORE-K4?") {
		t.Fatalf("x asks before forgetting:\n%s", plain(screen(t, m2)))
	}
	typeKeys(m2, "y")
	if len(back.Devices) != 0 {
		t.Error("y forgets the saved device")
	}
	if _, quit := m2.key(tea.KeyPressMsg{Code: tea.KeyEscape}); quit {
		t.Error("esc doesn't quit the connect screen")
	}
}

func TestThemeChoicePersists(t *testing.T) {
	path := t.TempDir() + "/config.toml"
	cfg, _ := config.Load(path)
	c := &clock{t: time.Date(2026, 10, 13, 10, 3, 0, 0, time.UTC)}
	m := New(Options{Theme: theme.ByName(cfg.UI.Theme), Motion: motion.Full, Clock: c.now, Config: cfg,
		Ports: func() []Target { return nil }})
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	typeKeys(m, "T") // works on the connect screen too
	back, _ := config.Load(path)
	if back.UI.Theme != "dusk" {
		t.Fatalf("after T the saved theme is %q", back.UI.Theme)
	}
	typeKeys(m, "TT")
	back, _ = config.Load(path)
	if back.UI.Theme != "ice" {
		t.Fatalf("after three presses the saved theme is %q", back.UI.Theme)
	}

	m2, c2 := newTestModel(t, motion.Full, 100, 30)
	m2.opt.Config = back
	boot(m2, c2)
	bootDone(t, m2, c2)
	typeKeys(m2, ":theme ice")
	key(m2, tea.KeyEnter)
	back, _ = config.Load(path)
	if back.UI.Theme != "ice" {
		t.Fatalf("after :theme ice the saved theme is %q", back.UI.Theme)
	}
}

func TestEnterSkipsTheBootPause(t *testing.T) {
	m, c := newTestModel(t, motion.Full, 100, 30)
	boot(m, c)
	tick(m, c, 100*time.Millisecond)
	if m.phase != phaseBoot {
		t.Fatal("the boot log is still up")
	}
	key(m, tea.KeyEnter)
	if m.phase != phaseMain {
		t.Error("enter goes straight on")
	}
	if m.boot.typedBy(motion.Off) != m.boot.lines[len(m.boot.lines)-1].at {
		t.Error("without typing, the log is shown as soon as each line arrives")
	}
}

func TestHeaderLampsLightUp(t *testing.T) {
	for _, level := range []motion.Level{motion.Full, motion.Off} {
		m, c := newTestModel(t, level, 120, 30)
		boot(m, c)
		bootDone(t, m, c)
		tick(m, c, 2*time.Second) // the header finishes sliding in
		idle := m.header()
		send(m, c, 0, radio.Packet{SNR: 5, Raw: []byte{0x15, 0x00}})
		lit := m.header()
		if lit == idle || ansi.Strip(lit) != ansi.Strip(idle) {
			t.Errorf("%v: the RX lamp lights without moving anything", level)
		}
		tick(m, c, 300*time.Millisecond)
		if level == motion.Full && (m.header() == lit || m.header() == idle) {
			t.Errorf("%v: it fades", level)
		}
		tick(m, c, time.Second)
		if m.header() != idle {
			t.Errorf("%v: and goes out", level)
		}
	}
}

func TestSlowTerminalReducesMotion(t *testing.T) {
	for _, chosen := range []bool{false, true} {
		m := New(Options{Theme: theme.Neon, Motion: motion.Full, MotionChosen: chosen})
		m.Update(SlowTerminalMsg{})
		if want := map[bool]motion.Level{false: motion.Reduced, true: motion.Full}[chosen]; m.level != want {
			t.Errorf("chosen %v: motion %v, want %v", chosen, m.level, want)
		}
	}
}

func TestStartupNotes(t *testing.T) {
	m := New(Options{Theme: theme.Neon, Notes: []string{"CONSOLE  try Windows Terminal"}})
	if len(m.log) != 1 || m.log[0].text != "CONSOLE  try Windows Terminal" {
		t.Errorf("log = %+v", m.log)
	}
}

func TestBeaconChoicePersists(t *testing.T) {
	path := t.TempDir() + "/config.toml"
	cfg, _ := config.Load(path)
	if !cfg.UI.Beacon {
		t.Fatal("the beacon is on by default")
	}
	m, c := newTestModel(t, motion.Full, 100, 30)
	m.opt.Config = cfg
	boot(m, c)
	bootDone(t, m, c)
	typeKeys(m, ":beacon off")
	key(m, tea.KeyEnter)
	back, _ := config.Load(path)
	if back.UI.Beacon {
		t.Fatal(":beacon off wasn't saved")
	}
	m2 := New(Options{Theme: theme.Neon, Motion: motion.Full, Clock: c.now, Config: back})
	if m2.beaconOn {
		t.Error("a saved beacon = false should start with it off")
	}
}

func TestVersionShows(t *testing.T) {
	h := newHarness(t)
	h.m.opt.Version = "v1.2.3"
	h.toMain()
	h.keys(":")
	h.typeLine("version")
	if !strings.Contains(h.lastLog(), "VERSION  meshcore-tui v1.2.3") {
		t.Errorf(":version: %s", h.lastLog())
	}
	h.keys("?")
	if !strings.Contains(h.screen(), "HELP · meshcore-tui v1.2.3") {
		t.Errorf("help title doesn't carry the version:\n%s", h.screen())
	}
	if !strings.Contains(strings.Join(h.m.logo(h.m.now), "\n"), "companion radio console · v1.2.3") {
		t.Error("the logo doesn't carry the version")
	}
}
