// Package ui is the Bubble Tea front end.
package ui

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"meshcore-tui/internal/admin"
	"meshcore-tui/internal/chat"
	"meshcore-tui/internal/config"
	"meshcore-tui/internal/mapview"
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
	Mode   string // ModeRepeater for a repeater's own console; empty for a companion radio
}

// ModeRepeater opens a repeater or room server's serial console instead of a companion radio.
const ModeRepeater = "repeater"

type Options struct {
	Theme        theme.Theme
	Motion       motion.Level
	MotionChosen bool // set by flag or config, so a slow terminal doesn't override it
	Dial         func(Target) radio.Radio
	Ports        func() []Target // lists serial ports for the connect screen
	Start        *Target         // skip the connect screen
	Clock        func() time.Time
	Config       *config.Config // nil in tests; settings changed in the app are saved here
	Store        *store.Store   // message history; nil disables chats

	Env       func(string) string // environment lookups for notification detection; nil uses the process's
	ExportDir string              // where :export writes; empty uses the data directory
	Keychain  admin.Keychain      // node passwords; nil keeps them only while the program runs
	MapDir    string              // street detail and its download; empty uses the data directory
	MapIndex  string              // where the street detail's tile list is; empty uses OpenFreeMap
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

	tab     int // the focused pane's screen
	tabAt   time.Time
	lay     layout
	paneKey bool // ctrl+w was pressed; the next key works the panes
	debug   bool
	frames  []radio.Frame
	log     []logLine
	ticker  ticker
	stats   stats
	glitch  time.Time
	ticking bool

	chat      *chat.Service
	chats     chatsModel
	admin     *admin.Service
	adm       adminModel
	keys      *admin.Cached
	device    deviceModel
	rstats    radioStatsModel
	contacts  contactsModel
	inspector inspectorModel
	mapv      mapModel

	queued tea.Cmd // a radio call started by a palette command

	cfgFallback *config.Config // settings for this run when there's no config file, as in tests

	notes   notifier
	focused bool // the terminal window has focus, as far as we know
	search  searchModel

	pal           palette
	help          bool
	quitting      bool
	tickerOn      bool
	mouseOn       bool
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
	m := &Model{opt: opt, th: opt.Theme, themeTo: opt.Theme, level: opt.Motion, now: opt.Clock(), tickerOn: true, mouseOn: true}
	m.lay, m.tab = m.savedLayout()
	retries, flood := 3, true
	if opt.Config != nil {
		m.tickerOn, m.mouseOn = opt.Config.UI.PacketTicker, opt.Config.UI.Mouse
		retries, flood = opt.Config.Messages.DMRetries, opt.Config.Messages.FloodFallback
	}
	if opt.Store != nil {
		m.chat = chat.New(opt.Store, retries, flood)
	}
	m.chats = newChats()
	m.contacts = newContacts()
	m.inspector = newInspector()
	m.mapv = newMapModel()
	m.admin = admin.New(m.adminNames)
	if m.opt.Keychain == nil {
		m.opt.Keychain = &admin.MemoryKeychain{}
	}
	m.keys = admin.NewCached(m.opt.Keychain)
	m.opt.Keychain = m.keys
	m.adm = newAdmin()
	m.device = newDevice()
	m.notes = newNotifier(opt)
	m.focused = true
	m.extraCommands = append(m.extraCommands, msgCommand(), exportCommand(), notifyCommand(), traceCommand(), channelCommand(), mapCommand())
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
	if m.adm.local {
		m.adm.local, m.adm.opened = false, false
		m.lay, m.tab = m.savedLayout()
	}
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

// SlowTerminalMsg says writing frames to the terminal is taking longer than drawing them should (SPEC §6.2).
type SlowTerminalMsg struct{}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.now = m.opt.Clock()
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case frameMsg:
		m.ticking = false
	case SlowTerminalMsg:
		if m.level == motion.Full && !m.opt.MotionChosen {
			m.level = motion.Reduced
			m.addLog(m.now, "MOTION  reduced: the terminal is slow to draw to · v changes it", radio.ToneInfo)
		}
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
	case contactsSyncedMsg, advertPathMsg, traceStartedMsg, traceTimeoutMsg, exportedMsg, autoAddMsg, opDoneMsg:
		cmd, _ := m.contactsMsg(msg)
		cmds = append(cmds, cmd)
	case jobSentMsg, jobTimeoutMsg, cmdSentMsg, cmdTimeoutMsg, autoStatusMsg, fleetPollMsg, keysCheckedMsg, watchTickMsg:
		cmds = append(cmds, m.adminMsg(msg))
	case devLoadedMsg, devOpMsg, keyExportedMsg:
		cmds = append(cmds, m.deviceMsg(msg))
	case statsMsg, statsTickMsg:
		cmds = append(cmds, m.statsMsg(msg))
	case channelsMsg:
		cmds = append(cmds, m.channelsMsg(msg))
	case mapLoadedMsg, detailFetchedMsg:
		cmds = append(cmds, m.mapMsg(msg))
	case sendResultMsg:
		cmds = append(cmds, m.chatResult(msg))
	case signedSentMsg:
		cmds = append(cmds, m.signedSent(msg))
	case ackTimeoutMsg:
		cmds = append(cmds, m.chatTimeout(msg))
	case tea.PasteMsg:
		cmds = append(cmds, m.paste(msg.Content))
	case tea.MouseClickMsg:
		cmds = append(cmds, m.click(tea.Mouse(msg)))
	case tea.KeyPressMsg:
		if utf8.RuneCountInString(msg.Text) > 1 && m.typing() {
			cmds = append(cmds, m.typeText(msg.Text)) // an emoji made of several characters
			break
		}
		if cmd, quit := m.key(msg); quit {
			m.shutdown()
			return m, tea.Quit
		} else if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	m.handover()
	m.blendTheme()
	cmds = append(cmds, m.screenData())
	cmds = append(cmds, m.ensureTick())
	return m, tea.Batch(cmds...)
}

// ensureTick runs the frame clock only while something is animating.
func (m *Model) ensureTick() tea.Cmd {
	if m.ticking || !m.animating() {
		return nil
	}
	m.ticking = true
	every := motion.FrameDur
	if !m.moving() {
		every = beaconFrame // only the map's beacon is pulsing, and it's slow enough not to need 30 a second
	}
	return tea.Tick(every, func(t time.Time) tea.Msg { return frameMsg(t) })
}

const beaconFrame = time.Second / 12

func (m *Model) animating() bool { return m.moving() || m.mapBeaconOn() }

// moving reports animation that needs the full frame rate.
func (m *Model) moving() bool {
	if m.level == motion.Off {
		return false
	}
	now := m.now
	if m.level.Allows(motion.Wipe) && motion.Running(m.themeAt, themeFadeDur, now) {
		return true
	}
	switch m.phase {
	case phaseConnect:
		return motion.Running(m.connect.openedAt, logoDecodeDur, now) || motion.Running(m.connect.modeAt, wipeDur*2, now)
	case phaseBoot:
		return !m.boot.failed || motion.Running(m.glitch, glitchDur*3, now) || m.boot.typing(m.level, now)
	}
	return motion.Running(m.mainAt, slideDur, now) ||
		motion.Running(m.tabAt, wipeDur, now) ||
		motion.Running(m.stats.rxLamp, headerLampDur, now) || motion.Running(m.stats.txLamp, headerLampDur, now) ||
		motion.Running(m.stats.snrAt, countDur, now) || motion.Running(m.stats.battAt, countDur, now) ||
		motion.Running(m.glitch, glitchDur, now) ||
		m.latestTyping() || m.linked == linkLost || m.chatsAnimating() || m.contactsAnimating() || m.inspectorAnimating() || m.adminAnimating() || m.deviceAnimating() || m.mapAnimating() ||
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
			mode := ""
			if e.Console {
				mode = ModeRepeater
			}
			c.Remember(config.Device{Name: e.Self.Name, Kind: m.target.Kind, Addr: m.target.Addr, Mode: mode, LastUsed: now})
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
		m.device.loaded, m.device.err = false, nil // a reboot or reset may have changed everything
	case radio.Packet:
		s := &m.stats
		s.packets++
		s.lastRx = now
		s.rxLamp = now
		s.snrFrom = s.currentSNR(now)
		s.snrTo, s.snrAt, s.rssi = float64(e.SNR), now, e.RSSI
		m.heardPacket(e)
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
	m.mapEvent(e)
	return tea.Batch(m.chatEvent(e), m.contactsEvent(e), m.adminEvent(e))
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
	headerLampDur = 600 * time.Millisecond // RX and TX: long enough to catch on a busy mesh
	countDur      = 500 * time.Millisecond
	wipeDur       = 140 * time.Millisecond
	slideDur      = 320 * time.Millisecond
	glitchDur     = 120 * time.Millisecond
	logoDecodeDur = 700 * time.Millisecond
	themeFadeDur  = 420 * time.Millisecond
	logCPS        = 140.0
	handoverDelay = 1500 * time.Millisecond // long enough to read the finished, all-green boot log
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
	if m.paneKey {
		m.paneKey = false
		return m.paneKeyPressed(s), false
	}
	if s == "ctrl+w" && m.phase == phaseMain {
		if m.repeaterMode() {
			m.addLog(m.now, "PANES  split panes are for companion mode; a repeater's screens show one at a time", radio.ToneInfo)
			return nil, false
		}
		m.paneKey = true
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
	if m.phase == phaseMain && m.tab == 1 && m.chat != nil {
		if cmd, handled := m.contactsKey(s); handled {
			return cmd, false
		}
	}
	if m.phase == phaseMain && m.tab == 4 && m.chat != nil {
		if cmd, handled := m.adminKey(s); handled {
			return cmd, false
		}
	}
	if m.phase == phaseMain && m.tab == 5 && m.dev() != nil {
		if cmd, handled := m.deviceKey(s); handled {
			return cmd, false
		}
	}
	if m.phase == phaseMain && m.tab == 3 {
		if cmd, handled := m.inspectorKey(s); handled {
			return cmd, false
		}
	}
	if m.phase == phaseMain && m.tab == 2 && !m.repeaterMode() {
		if cmd, handled := m.mapKey(s); handled {
			return cmd, false
		}
	}
	if s == "M" && m.phase == phaseMain && !m.repeaterMode() {
		m.closePath()
		m.setTab(2)
		return nil, false
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
				m.connect.setMode(m.target.Mode)
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
	case "tab", "shift+tab", "left", "right":
		if m.repeaterMode() {
			n := adminPane(len(paneNames))
			step := adminPane(1)
			if s == "shift+tab" || s == "left" {
				step = n - 1
			}
			return m.setPane((m.adm.pane + step) % n), false
		}
		if s == "left" || s == "right" {
			break
		}
		step := 1
		if s == "shift+tab" {
			step = len(tabNames) - 1
		}
		next := (m.tab + step) % len(tabNames)
		for m.visible(next) && next != m.tab { // screens open in other panes are skipped
			next = (next + step) % len(tabNames)
		}
		m.setTab(next)
	default:
		if m.repeaterMode() && len(s) == 1 && s[0] >= '1' && int(s[0]-'1') < len(paneNames) {
			return m.setPane(adminPane(s[0] - '1')), false
		}
		if len(s) == 1 && s[0] >= '1' && s[0] <= '7' {
			m.setTab(int(s[0] - '1'))
		}
	}
	if m.tab == 1 {
		return m.onSelectContact(), false
	}
	return nil, false
}

// screenData starts reading what the current screen shows, the first time it's needed.
func (m *Model) screenData() tea.Cmd {
	if m.phase != phaseMain {
		return nil
	}
	if m.visible(4) && !m.adm.keysChecked {
		return m.checkKeys()
	}
	if m.visible(2) && m.mapv.world == nil && !m.mapv.loading && !m.repeaterMode() {
		return m.loadMap()
	}
	if m.dev() == nil {
		return nil
	}
	switch {
	case m.visible(5) && !m.device.loaded && !m.device.loading && m.device.err == nil:
		return m.loadDevice()
	case m.visible(6) && !m.rstats.polling:
		return m.pollStats()
	}
	return nil
}

// setTab shows a screen in the focused pane, or moves the focus to the pane already showing it.
func (m *Model) setTab(i int) {
	if i == m.tab {
		return
	}
	if p := m.lay.showing(i); p != nil && !m.lay.zoomed {
		m.focusPane(p)
	} else {
		if p != nil {
			p.tab = m.tab // zoomed: the hidden pane takes the screen being left
		}
		m.lay.focus.tab = i
		m.tab, m.tabAt = i, m.now
		m.saveLayout()
	}
	if i == 0 {
		m.refreshChats() // marks the open conversation read
	}
}

// savedLayout is the companion screens' split panes from the config, or one pane of Chats.
func (m *Model) savedLayout() (layout, int) {
	if m.opt.Config != nil {
		if l, ok := parseLayout(m.opt.Config.UI.Layout); ok {
			return l, l.focus.tab
		}
	}
	return newLayout(0), 0
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
	if m.phase != phaseBoot || m.readyAt.IsZero() {
		return
	}
	shown := m.readyAt
	if t := m.boot.typedBy(m.level); t.After(shown) {
		shown = t
	}
	if m.now.Sub(shown) >= handoverDelay {
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
		out = append(out, Target{Kind: d.Kind, Addr: d.Addr, Saved: d.Name, Mode: d.Mode, Detail: d.Name + " · used " + ago(m.now.Sub(d.LastUsed))})
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
	if m.tab == 5 {
		return []string{key("↑↓ j k", "choose a setting"), key("enter", "edit it, switch it, or run it"),
			key("u", "read the settings again"), key("esc", "cancel an edit"),
			key("", "radio changes, a new identity, a reboot or a factory reset ask first"), ""}
	}
	if m.tab == 6 {
		return []string{key("", fmt.Sprintf("the radio's counters are read every %s while this screen is open", statsEvery)),
			key("", "the mesh overview counts the packets heard since connecting"), ""}
	}
	if m.tab == 4 && m.repeaterMode() {
		return []string{key("1-9 ← →", "switch screens: status, health, settings, neighbours, access, regions, telemetry, log, console"),
			key("tab", "the next screen; in the console it completes a command"),
			key("u", "refresh what's shown"), key("a", "refresh the status every so often while it's open"),
			key("health", fmt.Sprintf("the node is polled every %s while connected and each poll is kept", every(m.watchEvery()))),
			key("r", "on Health, 24 hours or 7 days · on Log, read the log and sum it up"),
			key("console", "type commands as on the serial console; esc on an empty line leaves it"), ""}
	}
	if m.tab == 4 {
		return []string{key("↑↓ j k", "choose a repeater or room server"), key("enter", "open it, logging in first; an empty password is a guest login"),
			key("← →", "switch between status, settings, neighbours, access, regions, telemetry, log and console"),
			key("u", "refresh what's shown"), key("L", "log out"), key("esc", "back to the node list"),
			key("f F", "mark a favourite · show favourites only, which P then polls"), key("X", "forget a saved password"),
			key("w", fmt.Sprintf("watch a node: its status is polled every %s and kept, for the Health screen", every(m.watchEvery()))),
			key("H", "open a node's kept history without logging in"),
			key("console", "type commands as on the node's serial console; tab completes, ↑↓ recall"), ""}
	}
	if m.tab == 2 {
		return []string{key("←↑↓→ h j k l", "move the map"), key("+ -", "zoom in and out"), key("m", "back to your whole mesh"),
			key("[ ]", "pick out a route, newest first; esc shows them all again"),
			key("path", "M on a packet, message or contact shows just its path: [ ] other copies, m fits it, esc goes back"),
			key("d", "download street detail (roads, rivers, towns) for your mesh's area from OpenFreeMap, or more of it; esc stops"),
			key("L r", "town and node names · main roads"),
			key("t", "the traffic layer: links get more saturated the more packets crossed them lately (halving every hour)"),
			key("", fmt.Sprintf("the newest %d routes are drawn, fading out over %s; [ ] picks any of the last %d", mapview.RoutesShown, "ten minutes", mapRoutes)), key("M", "from any screen, open the map; on Packets, on that packet's route"),
			key(":map update", "download the street detail again · :map remove deletes it"),
			key("", "only adverts and messages on open channels travel the map, as on the Packets screen"),
			key("", "a hop's colour is its signal where known: measured for the last hop, from traces and neighbour lists for the rest"), ""}
	}
	if m.tab == 3 {
		return []string{key("↑↓ j k", "choose a packet; g jumps back to the newest"), key("[ ]", "step through decoded fields, highlighting their bytes"),
			key("space", "pause, so the list holds still"), key("R n", "filter by route · minimum signal"), key("/", "filter by text"),
			key("C", "clear"), key(":export packets", "save the visible packets as JSON lines"),
			key("M", "show the packet's path on the map, with each copy heard"), key("y", "copy the packet's bytes"), ""}
	}
	if m.tab == 1 {
		return []string{key("↑↓ j k", "choose a contact"), key("m", "message them"), key("t", "trace the route, showing signal at each hop"),
			key("d", "discover a fresh route"), key("p", "set the route by hand"), key("r", "reset the route, so messages flood"),
			key("f", "favourite"), key("e", "share as a QR code and meshcore:// link"), key("x", "remove from the radio"),
			key("M", "show the way to them on the map: the newest trace, else the stored route, else their advert's path"),
			key("I", "import a meshcore:// link"), key("a", "nodes heard but not added"), key("A", "auto-add settings"),
			key("P", "suggest contacts to prune"), key("F s /", "filter by type or favourites · sort · filter by name"), ""}
	}
	if m.tab == 0 {
		return []string{key("↑↓ j k", "choose a conversation"), key("enter i", "write a message; enter sends"), key("tab", "while writing, complete an @name; again for the next match"),
			key("← → home end", "while typing, move the cursor; alt+← → by word · alt+backspace deletes a word · ctrl+u ctrl+k to the start or end"),
			key("esc", "stop writing, or clear the filter"), key("/", "filter this conversation"),
			key("ctrl+f", "search every conversation"), key("K J", "scroll history"),
			key("→", "pick a message: ↑↓ move, r replies to it (naming them in a channel), M shows its route, y copies it, ← goes back"),
			key("paste", "pastes into whatever you're typing in; in Chats it starts a message"),
			key("S", "sign your messages on the open channel, or stop; ctrl+s while writing flips it for one message"),
			key("p P", "on a picked signed message, pin its key to the sender's name · replace a pin"),
			key("M", "show on the map the route of the newest message received in view"),
			key("R", "send your newest failed message again; on a picked one, R or enter sends that one"),
			key("L", "log in to the room server whose conversation is open"),
			key("n", "add a channel: #name, a private one with its secret, or a shared link"),
			key("s r x", "share, rename or leave the channel that's open"), ""}
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
