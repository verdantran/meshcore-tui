// Package ui is the Bubble Tea front end.
package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"meshcore-tui/internal/chat"
	"meshcore-tui/internal/config"
	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/store"
	"meshcore-tui/internal/theme"
)

// Target is something the user can connect to: a serial port, a TCP host, or the simulator.
type Target struct {
	Kind   string // USB, TCP or SIM
	Addr   string
	Detail string
	Saved  string // the saved device's name, if this came from config
}

type Options struct {
	Theme  theme.Theme
	Motion motion.Level
	Dial   func(Target) radio.Radio
	Ports  func() []Target // lists serial ports for the connect screen
	Start  *Target         // skip the connect screen
	Clock  func() time.Time
	Config *config.Config // nil in tests; settings changed in the app are saved here
	Store  *store.Store   // message history; nil disables chats

	Env       func(string) string // environment lookups for notification detection; nil uses the process's
	ExportDir string              // where :export writes; empty uses the data directory
}

type phase int

const (
	phaseConnect phase = iota
	phaseBoot
	phaseMain
)

type Model struct {
	opt   Options
	th    theme.Theme // what's drawn now; mid-crossfade it's a blend of themeFrom and themeTo
	level motion.Level

	themeFrom, themeTo theme.Theme
	themeAt            time.Time
	now                time.Time
	w, h               int

	phase   phase
	connect connectModel
	target  Target
	radio   radio.Radio
	cancel  context.CancelFunc

	boot    bootModel
	readyAt time.Time
	mainAt  time.Time
	ready   radio.Ready
	linked  linkState

	tab     int
	tabAt   time.Time
	debug   bool
	frames  []radio.Frame
	log     []logLine
	ticker  ticker
	stats   stats
	glitch  time.Time
	ticking bool

	chat  *chat.Service
	chats chatsModel

	queued tea.Cmd // a radio call started by a palette command

	notes   notifier
	focused bool // the terminal window has focus, as far as we know
	search  searchModel

	pal           palette
	help          bool
	quitting      bool
	tickerOn      bool
	extraCommands []command
}

type linkState int

const (
	linkUp linkState = iota
	linkLost
	linkDown
)

type logLine struct {
	at   time.Time
	text string
	tone radio.Tone
}

type stats struct {
	packets          int
	lastRx           time.Time
	snrFrom, snrTo   float64
	snrAt            time.Time
	rssi             int8
	rxLamp, txLamp   time.Time
	battFrom, battTo float64
	battAt           time.Time
}

type eventMsg struct{ e radio.Event }
type frameMsg time.Time

func New(opt Options) *Model {
	if opt.Clock == nil {
		opt.Clock = time.Now
	}
	m := &Model{opt: opt, th: opt.Theme, themeTo: opt.Theme, level: opt.Motion, now: opt.Clock(), tickerOn: true}
	retries, flood := 3, true
	if opt.Config != nil {
		m.tickerOn = opt.Config.UI.PacketTicker
		retries, flood = opt.Config.Messages.DMRetries, opt.Config.Messages.FloodFallback
	}
	if opt.Store != nil {
		m.chat = chat.New(opt.Store, retries, flood)
	}
	m.chats = newChats()
	m.notes = newNotifier(opt)
	m.focused = true
	m.extraCommands = append(m.extraCommands, msgCommand(), exportCommand())
	m.connect = newConnect(opt.Ports, m.savedTargets)
	m.ticker = newTicker()
	return m
}

func (m *Model) Init() tea.Cmd {
	if m.opt.Start != nil {
		return m.dial(*m.opt.Start)
	}
	m.connect.openedAt = m.now
	return nil
}

func (m *Model) dial(t Target) tea.Cmd {
	if m.radio != nil {
		m.radio.Close()
	}
	m.target = t
	m.radio = m.opt.Dial(t)
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.radio.Start(ctx)
	m.phase = phaseBoot
	m.boot = newBoot(m.now, t)
	m.linked = linkUp
	return tea.Batch(waitEvent(m.radio.Events()), m.ensureTick())
}

func waitEvent(ch <-chan radio.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg{e}
	}
}

func (m *Model) shutdown() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.radio != nil {
		m.radio.Close()
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.now = m.opt.Clock()
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case frameMsg:
		m.ticking = false
	case eventMsg:
		cmds = append(cmds, m.apply(msg.e))
		if m.radio != nil {
			cmds = append(cmds, waitEvent(m.radio.Events()))
		}
	case tea.FocusMsg:
		m.focused = true
	case tea.BlurMsg:
		m.focused = false
	case notifyFlushMsg:
		cmds = append(cmds, m.flushNotes())
	case sendResultMsg:
		cmds = append(cmds, m.chatResult(msg))
	case ackTimeoutMsg:
		cmds = append(cmds, m.chatTimeout(msg))
	case tea.KeyPressMsg:
		if cmd, quit := m.key(msg); quit {
			m.shutdown()
			return m, tea.Quit
		} else if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	m.handover()
	m.blendTheme()
	cmds = append(cmds, m.ensureTick())
	return m, tea.Batch(cmds...)
}

// ensureTick runs the frame clock only while something is animating.
func (m *Model) ensureTick() tea.Cmd {
	if m.ticking || !m.animating() {
		return nil
	}
	m.ticking = true
	return tea.Tick(motion.FrameDur, func(t time.Time) tea.Msg { return frameMsg(t) })
}

func (m *Model) animating() bool {
	if m.level == motion.Off {
		return false
	}
	now := m.now
	if m.level.Allows(motion.Wipe) && motion.Running(m.themeAt, themeFadeDur, now) {
		return true
	}
	switch m.phase {
	case phaseConnect:
		return motion.Running(m.connect.openedAt, logoDecodeDur, now)
	case phaseBoot:
		return !m.boot.failed || motion.Running(m.glitch, glitchDur*3, now) || m.boot.typing(m.level, now)
	}
	return motion.Running(m.mainAt, slideDur, now) ||
		motion.Running(m.tabAt, wipeDur, now) ||
		motion.Running(m.stats.rxLamp, lampDur, now) || motion.Running(m.stats.txLamp, lampDur, now) ||
		motion.Running(m.stats.snrAt, countDur, now) || motion.Running(m.stats.battAt, countDur, now) ||
		motion.Running(m.glitch, glitchDur, now) ||
		m.latestTyping() || m.linked == linkLost || m.chatsAnimating() ||
		m.ticker.moving(m.level, now, m.w)
}

func (m *Model) apply(e radio.Event) tea.Cmd {
	now := m.now
	switch e := e.(type) {
	case radio.BootStep:
		m.boot.step(e, now)
		if e.State == radio.StepFail {
			m.glitch = now
		}
	case radio.Ready:
		m.ready = e
		m.readyAt = now
		m.boot.done(now)
		m.stats.battFrom, m.stats.battTo, m.stats.battAt = float64(e.Battery.MilliVolts), float64(e.Battery.MilliVolts), time.Time{}
		m.addLog(now, "LINK ESTABLISHED  "+e.Self.Name+" · "+e.Device.Model, radio.ToneOK)
		if m.level == motion.Off {
			m.enterMain()
		}
		if c := m.opt.Config; c != nil && m.target.Kind != "SIM" {
			c.Remember(config.Device{Name: e.Self.Name, Kind: m.target.Kind, Addr: m.target.Addr, LastUsed: now})
			m.saveConfig()
		}
	case radio.Fatal:
		m.boot.fail(e.Err, now)
		m.glitch = now
		m.linked = linkDown
	case radio.Lost:
		m.linked = linkLost
		m.glitch = now
		m.addLog(now, "LINK LOST  "+e.Err.Error()+"  reconnecting…", radio.ToneFail)
	case radio.Restored:
		m.linked = linkUp
		m.addLog(now, "LINK RESTORED", radio.ToneOK)
	case radio.Packet:
		s := &m.stats
		s.packets++
		s.lastRx = now
		s.rxLamp = now
		s.snrFrom = s.currentSNR(now)
		s.snrTo, s.snrAt, s.rssi = float64(e.SNR), now, e.RSSI
		m.ticker.push(e.Raw, now, m.w)
	case radio.Transmit:
		m.stats.txLamp = now
	case radio.Battery:
		s := &m.stats
		s.battFrom = s.currentBatt(now)
		s.battTo, s.battAt = float64(e.MilliVolts), now
	case radio.Log:
		m.addLog(now, e.Text, e.Tone)
	case radio.Frame:
		m.frames = append(m.frames, e)
		if len(m.frames) > 400 {
			m.frames = m.frames[len(m.frames)-400:]
		}
	}
	return m.chatEvent(e)
}

func (s *stats) currentSNR(now time.Time) float64 {
	return motion.Count(s.snrFrom, s.snrTo, s.snrAt, countDur, now)
}

func (s *stats) currentBatt(now time.Time) float64 {
	return motion.Count(s.battFrom, s.battTo, s.battAt, countDur, now)
}

func (m *Model) addLog(at time.Time, text string, tone radio.Tone) {
	m.log = append(m.log, logLine{at: at, text: text, tone: tone})
	if len(m.log) > 500 {
		m.log = m.log[len(m.log)-500:]
	}
}

const (
	lampDur       = 400 * time.Millisecond
	countDur      = 500 * time.Millisecond
	wipeDur       = 140 * time.Millisecond
	slideDur      = 320 * time.Millisecond
	glitchDur     = 120 * time.Millisecond
	logoDecodeDur = 700 * time.Millisecond
	themeFadeDur  = 420 * time.Millisecond
	logCPS        = 140.0
	handoverDelay = 650 * time.Millisecond
)

func (m *Model) latestTyping() bool {
	if len(m.log) == 0 || !m.level.Allows(motion.Typing) {
		return false
	}
	l := m.log[len(m.log)-1]
	return motion.Running(l.at, motion.TypeDur(l.text, logCPS), m.now)
}

var tabNames = []string{"CHATS", "CONTACTS", "MAP", "PACKETS", "ADMIN", "DEVICE", "STATS"}

func (m *Model) key(k tea.KeyPressMsg) (tea.Cmd, bool) {
	s := k.String()
	if s == "ctrl+c" {
		return nil, true
	}
	if m.pal.open {
		m.paletteKey(s)
		cmd := m.queued
		m.queued = nil
		return cmd, m.quitting
	}
	if m.help {
		if s == "esc" || s == "?" || s == "q" || s == "enter" {
			m.help = false
		}
		return nil, false
	}
	if m.search.open {
		m.searchKey(s)
		return nil, false
	}
	if s == "ctrl+f" && m.phase == phaseMain {
		m.openSearch()
		return nil, false
	}
	if m.phase == phaseMain && m.tab == 0 && m.chat != nil {
		if cmd, handled := m.chatsKey(s); handled {
			return cmd, false
		}
	}
	if s == "T" && !(m.phase == phaseConnect && m.connect.editing) {
		m.cycleTheme()
		return nil, false
	}
	switch m.phase {
	case phaseConnect:
		t, chosen, quit := m.connect.key(s, m.now, m.forget)
		if quit {
			return nil, true
		}
		if chosen {
			return m.dial(t), false
		}
		return nil, false
	case phaseBoot:
		switch s {
		case "q":
			return nil, true
		case "r":
			if m.boot.failed {
				return m.dial(m.target), false
			}
		case "c":
			if m.boot.failed && m.opt.Ports != nil {
				m.radio.Close()
				m.radio = nil
				m.phase = phaseConnect
				m.connect = newConnect(m.opt.Ports, m.savedTargets)
				m.connect.openedAt = m.now
			}
		case "enter", "space", "esc":
			// Any key skips the remaining handover delay once the radio is ready.
			if !m.readyAt.IsZero() {
				m.enterMain()
			}
		}
		return nil, false
	}
	switch s {
	case "q":
		return nil, true
	case ":":
		m.openPalette()
	case "?":
		m.help = true
	case "f12", "ctrl+d":
		m.debug = !m.debug
	case "v":
		m.setMotion((m.level + 1) % 3)
	case "tab":
		m.setTab((m.tab + 1) % len(tabNames))
	case "shift+tab":
		m.setTab((m.tab + len(tabNames) - 1) % len(tabNames))
	default:
		if len(s) == 1 && s[0] >= '1' && s[0] <= '7' {
			m.setTab(int(s[0] - '1'))
		}
	}
	return nil, false
}

func (m *Model) setTab(i int) {
	if i != m.tab {
		m.tab = i
		m.tabAt = m.now
		if i == 0 {
			m.refreshChats() // marks the open conversation read
		}
	}
}

func (m *Model) enterMain() {
	if m.phase == phaseMain {
		return
	}
	m.phase = phaseMain
	m.mainAt = m.now
}

// handover moves from the boot log to the main screen shortly after Ready, so the last [ OK ] is seen.
func (m *Model) handover() {
	if m.phase == phaseBoot && !m.readyAt.IsZero() && m.now.Sub(m.readyAt) >= handoverDelay {
		m.enterMain()
	}
}

// cycleTheme starts a crossfade from whatever is on screen to the next theme.
func (m *Model) cycleTheme() { m.setTheme(m.themeTo.Next()) }

func (m *Model) setTheme(t theme.Theme) {
	m.themeFrom = m.th
	m.themeTo = t
	m.themeAt = m.now
	m.blendTheme()
	m.addLog(m.now, "THEME  "+t.Name, radio.ToneInfo)
	if m.opt.Config != nil {
		m.opt.Config.UI.Theme = t.Name
		m.saveConfig()
	}
}

func (m *Model) setMotion(l motion.Level) {
	m.level = l
	m.addLog(m.now, "MOTION  "+l.String(), radio.ToneInfo)
	if m.opt.Config != nil {
		m.opt.Config.UI.Motion = l.String()
		m.saveConfig()
	}
}

func (m *Model) saveConfig() {
	if err := m.opt.Config.Save(); err != nil {
		m.addLog(m.now, "CONFIG NOT SAVED  "+err.Error(), radio.ToneWarn)
	}
}

// savedTargets lists remembered devices for the connect screen, most recent first.
func (m *Model) savedTargets() []Target {
	if m.opt.Config == nil {
		return nil
	}
	var out []Target
	for _, d := range m.opt.Config.Devices {
		out = append(out, Target{Kind: d.Kind, Addr: d.Addr, Saved: d.Name, Detail: d.Name + " · used " + ago(m.now.Sub(d.LastUsed))})
	}
	return out
}

func (m *Model) forget(t Target) {
	if m.opt.Config != nil && t.Saved != "" {
		m.opt.Config.Forget(t.Kind, t.Addr)
		m.saveConfig()
	}
}

// screenKeys are the help lines for the current screen; screens add their own as they're built.
func (m *Model) screenKeys() []string {
	th := m.th
	key := func(k, what string) string {
		return th.Fg(th.Cyan, true).Render(fmt.Sprintf("%-12s", k)) + th.Fg(th.Muted, false).Render(what)
	}
	if m.tab == 0 {
		return []string{key("↑↓ j k", "choose a conversation"), key("enter i", "write a message; enter sends"),
			key("esc", "stop writing, or clear the filter"), key("/", "filter this conversation"),
			key("ctrl+f", "search every conversation"), key("K J", "scroll history"), ""}
	}
	return nil
}

func (m *Model) blendTheme() {
	if m.themeAt.IsZero() || !m.level.Allows(motion.Wipe) || !motion.Running(m.themeAt, themeFadeDur, m.now) {
		m.th = m.themeTo
		return
	}
	m.th = theme.Blend(m.themeFrom, m.themeTo, motion.EaseOut(motion.Progress(m.themeAt, themeFadeDur, m.now)))
}

// Theme is the theme being shown, or faded to.
func (m *Model) Theme() theme.Theme { return m.themeTo }
