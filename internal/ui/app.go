// Package ui is the Bubble Tea front end.
package ui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/mapview"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
	"github.com/verdantran/meshcore-tui/internal/untrusted"
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
	Ports        func() []Target                                     // lists serial ports for the connect screen
	Scan         func(ctx context.Context, found func(Target)) error // looks for Bluetooth radios until ctx ends; nil without Bluetooth
	Start        *Target                                             // skip the connect screen
	Clock        func() time.Time
	Config       *config.Config // nil in tests; settings changed in the app are saved here
	Store        *store.Store   // message history; nil disables chats

	Env         func(string) string // environment lookups for notification detection; nil uses the process's
	Remote      bool                // the terminal is reached over SSH or mosh, as worked out at startup
	SplitEmoji  bool                // the terminal draws joined emoji neither way the app can count, so they're drawn as their parts
	LoneLetter  int                 // how wide the terminal draws a lone regional indicator; 0 when it didn't say
	Flag        int                 // how wide the terminal draws a flag, two regional indicators; 0 when it didn't say
	FlagLetters bool                // draw every flag and lone regional indicator as letters, as [ui] flags says or tmux needs
	ExportDir   string              // where :export writes; empty uses the data directory
	Keychain    admin.Keychain      // node passwords; nil keeps them only while the program runs
	MapDir      string              // street detail and its download; empty uses the data directory
	MapIndex    string              // where the street detail's tile list is; empty uses OpenFreeMap
	MapClient   *http.Client        // fetches street detail; nil uses the default
	Notes       []string            // warnings from startup, put in the event log
	ASCII       bool                // plain ASCII in place of braille, box drawing and symbols
	Keys        config.Keys         // keys.toml: a key pressed does what another does by default
	Version     string              // meshcore-tui's own release or commit; empty in tests
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
	toasts  []toast
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
	prune  pruner

	cfgFallback *config.Config // settings for this run when there's no config file, as in tests

	notes   notifier
	focused bool // the terminal window has focus, as far as we know
	search  searchModel

	pal           palette
	help          bool
	helpScroll    int
	asking        *question
	clipGen       int // counts copies, so a timed clear never wipes something copied since
	wifiNoted     bool
	quitting      bool
	tickerOn      bool
	beaconOn      bool
	mouseOn       bool
	extraCommands []command
	trust         radioTrust // whether the radio has proved it's the one saved, which saved passwords need
	trustWhy      string     // why it isn't verified, when it isn't
	renewKey      bool       // the radio's key is expected to change: a factory reset, a new key, or :radio trust
}

// question is something that waits for y before it's done: anything that deletes data or changes another node.
type question struct {
	what, verb string
	run        func() tea.Cmd
}

// hint is a key and what it does, imperative: "u refresh".
type hint struct{ k, v string }

// hintLine joins hints as a screen's bottom line: key verb · key verb.
func hintLine(hs []hint) string {
	parts := make([]string, len(hs))
	for i, h := range hs {
		parts[i] = strings.TrimSpace(h.k + " " + h.v)
	}
	return strings.Join(parts, " · ")
}

// hintLines wraps hints to lines no wider than w, breaking between hints rather than inside one.
func hintLines(hs []hint, w int) []string {
	var lines []string
	cur := ""
	for _, h := range hs {
		part := strings.TrimSpace(h.k + " " + h.v)
		switch {
		case cur == "":
			cur = part
		case visibleWidth(cur+" · "+part) <= w:
			cur += " · " + part
		default:
			lines, cur = append(lines, cur), part
		}
	}
	return append(lines, cur)
}

// fitHints joins as many hints as fit in w, dropping whole ones from the end rather than cutting one short.
func fitHints(hs []string, w int) string {
	for n := len(hs); n > 0; n-- {
		if s := strings.Join(hs[:n], " · "); visibleWidth(s) <= w {
			return s
		}
	}
	return ""
}

func (m *Model) ask(what, verb string, run func() tea.Cmd) { m.asking = &question{what, verb, run} }

func (m *Model) askLine() string {
	th := m.th
	return th.Fg(th.Red, true).Render(strings.ToUpper(m.asking.what)) + th.Fg(th.Muted, false).Render(" · y "+m.asking.verb+" · any other key cancel")
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
	m := &Model{opt: opt, th: opt.Theme, themeTo: opt.Theme, level: opt.Motion, now: opt.Clock(), tickerOn: true, beaconOn: true, mouseOn: true}
	m.lay, m.tab = m.savedLayout()
	retries, flood := 3, true
	if opt.Config != nil {
		m.tickerOn, m.beaconOn, m.mouseOn = opt.Config.UI.PacketTicker, opt.Config.UI.Beacon, opt.Config.UI.Mouse
		retries, flood = opt.Config.Messages.DMRetries, opt.Config.Messages.FloodFallback
	}
	if opt.Store != nil {
		m.chat = chat.New(opt.Store, retries, flood)
		if opt.Version != "" {
			m.chat.App = "meshcore-tui " + opt.Version
		}
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
	m.extraCommands = append(m.extraCommands, msgCommand(), exportCommand(), notifyCommand(), historyCommand(), traceCommand(), channelCommand(), mapCommand(), radioCommand())
	m.connect = newConnect(opt.Ports, m.savedTargets)
	m.ticker = newTicker()
	for _, n := range opt.Notes {
		m.addLog(m.now, n, radio.ToneWarn)
	}
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
	m.stopPrune()
	if m.cancel != nil {
		m.cancel()
	}
	if m.radio != nil {
		m.radio.Close()
	}
}

// SlowTerminalMsg says writing frames to the terminal is taking longer than drawing them should.
type SlowTerminalMsg struct{}

// safely handles what came from the radio or the mesh, so that data nobody foresaw is set aside with a note
// in the log rather than closing the app.
func (m *Model) safely(what string, handle func() tea.Cmd) (cmd tea.Cmd) {
	defer func() {
		if r := recover(); r != nil {
			m.addLog(m.now, fmt.Sprintf("SKIPPED  %s that couldn't be read: %v", what, r), radio.ToneWarn)
			cmd = nil
		}
	}()
	return handle()
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.now = m.opt.Clock()
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.ModeReportMsg:
		// Bubble Tea switches its renderer to grapheme widths on the same answer.
		if msg.Mode == ansi.ModeUnicodeCore && (msg.Value == ansi.ModeReset || msg.Value == ansi.ModeSet || msg.Value == ansi.ModePermanentlySet) && renderWidth != ansi.GraphemeWidth {
			setRenderWidth(ansi.GraphemeWidth)
			m.opt.SplitEmoji = false             // a terminal that says it counts graphemes draws them whole
			cmds = append(cmds, tea.ClearScreen) // rows already drawn were laid out a character at a time
		}
	case frameMsg:
		m.ticking = false
	case scanMsg:
		cmds = append(cmds, m.connect.scanned(msg))
	case SlowTerminalMsg:
		if m.level == motion.Full && !m.opt.MotionChosen {
			m.level = motion.Reduced
			m.addLog(m.now, "MOTION  reduced: the terminal is slow to draw to · v changes it", radio.ToneInfo)
		}
	case eventMsg:
		cmds = append(cmds, m.safely("something the radio sent", func() tea.Cmd { return m.apply(msg.e) }))
		if m.radio != nil {
			cmds = append(cmds, waitEvent(m.radio.Events()))
		}
	case tea.FocusMsg:
		m.focused = true
	case tea.BlurMsg:
		m.focused = false
	case notifyFlushMsg:
		cmds = append(cmds, m.flushNotes())
	case noteSummaryMsg:
		cmds = append(cmds, m.summariseNotes())
	case contactsSyncedMsg, advertPathMsg, traceStartedMsg, traceTimeoutMsg, exportedMsg, autoAddMsg, opDoneMsg:
		cmd, _ := m.contactsMsg(msg)
		cmds = append(cmds, cmd)
	case jobSentMsg, jobTimeoutMsg, cmdSentMsg, cmdTimeoutMsg, cmdLateMsg, autoStatusMsg, fleetPollMsg, keysCheckedMsg, watchTickMsg:
		cmds = append(cmds, m.safely("a reply from a node", func() tea.Cmd { return m.adminMsg(msg) }))
	case devLoadedMsg, devOpMsg, keyExportedMsg, clipClearMsg:
		cmds = append(cmds, m.deviceMsg(msg))
	case backupDoneMsg, restoredMsg:
		cmds = append(cmds, m.backupMsg(msg))
	case cliRanMsg:
		cmds = append(cmds, m.consoleMsg(msg))
	case radioCheckedMsg:
		m.radioChecked(msg)
	case statsMsg, statsTickMsg:
		cmds = append(cmds, m.statsMsg(msg))
	case channelsMsg:
		cmds = append(cmds, m.channelsMsg(msg))
	case mapLoadedMsg, detailFetchedMsg:
		cmds = append(cmds, m.mapMsg(msg))
	case searchDueMsg, searchDoneMsg:
		cmds = append(cmds, m.searchMsg(msg))
	case exportDoneMsg:
		m.exportDone(msg)
	case pruneDoneMsg:
		m.pruneDone(msg)
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
	case tea.MouseWheelMsg:
		cmds = append(cmds, m.wheel(tea.Mouse(msg)))
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
	m.flushChats()
	cmds = append(cmds, m.pruneDue())
	if m.linked == linkUp && m.chat != nil {
		if err := m.chat.Seen(m.now); err != nil {
			m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
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
	return tea.Tick(m.frameEvery(), func(t time.Time) tea.Msg { return frameMsg(t) })
}

// frameEvery is the frame clock's period. Nothing slides at reduced, and the beacon, a pulse or a
// toast's timer bar don't need 30 frames a second.
func (m *Model) frameEvery() time.Duration {
	if m.level == motion.Full && m.moving() {
		return motion.FrameDur
	}
	return beaconFrame
}

const beaconFrame = time.Second / 12

func (m *Model) animating() bool {
	return m.moving() || m.mapBeaconOn() || m.pulsing() || (m.level != motion.Off && m.toasting())
}

// pulsePeriod is the slow glow of something waiting on you; it runs on the beacon's slower frame clock.
const pulsePeriod = 1200 * time.Millisecond

func (m *Model) pulsing() bool {
	if !m.level.Allows(motion.Flash) || m.phase != phaseMain {
		return false
	}
	for _, tab := range []int{1, 4, 5} {
		if m.visible(tab) && m.dangerWaiting(tab) {
			return true
		}
	}
	if m.boardPulsing() {
		return true
	}
	used, limit := m.composeLoad()
	return m.visible(0) && m.chats.writing && limit > 0 && used*10 >= limit*9
}

// dangerWaiting reports a screen asking before something that can't be undone: a reboot, an erase,
// a radio change, removing a contact.
func (m *Model) dangerWaiting(tab int) bool {
	switch tab {
	case 1:
		return m.contacts.overlay == removeOverlay
	case 4:
		return m.adm.prompt == promptConfirm
	case 5:
		return m.device.prompt == devConfirm
	}
	return false
}

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
		return motion.Running(m.connect.openedAt, logoDecodeDur, now) || motion.Running(m.connect.modeAt, wipeDur*2, now) || m.connect.scanning
	case phaseBoot:
		return !m.boot.failed || motion.Running(m.glitch, glitchDur*3, now) || m.boot.typing(m.level, now)
	}
	return m.toastSliding() || motion.Running(m.mainAt, slideDur, now) ||
		motion.Running(m.tabAt, wipeDur, now) ||
		motion.Running(m.stats.rxLamp, headerLampDur, now) || motion.Running(m.stats.txLamp, headerLampDur, now) ||
		motion.Running(m.stats.snrAt, countDur, now) || motion.Running(m.stats.battAt, countDur, now) ||
		motion.Running(m.glitch, glitchDur, now) ||
		m.latestTyping() || m.linked == linkLost || m.chatsAnimating() || m.contactsAnimating() || m.inspectorAnimating() || m.adminAnimating() || m.deviceAnimating() || m.mapAnimating() ||
		m.ticker.moving(m.level, now, m.w)
}

func (m *Model) apply(e radio.Event) tea.Cmd {
	now := m.now
	var check tea.Cmd
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
		m.wifiNote()
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
		check = m.checkRadio()
	case radio.Fatal:
		m.boot.fail(e.Err, now)
		m.glitch = now
		m.linked = linkDown
	case radio.Lost:
		m.linked = linkLost
		m.glitch = now
		m.addLog(now, "LINK LOST  "+untrusted.Line(e.Err.Error())+"  reconnecting…", radio.ToneFail)
	case radio.Restored:
		m.linked = linkUp
		m.trust = trustChecking // whatever answered may not be the same radio; it proves itself again on Ready
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
	return tea.Batch(check, m.chatEvent(e), m.contactsEvent(e), m.adminEvent(e))
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
	// A question takes the key you pressed, so no binding can turn "no" into "yes".
	if !m.typing() && !m.confirming() {
		if to, ok := m.rebound(s); ok {
			if to == "" {
				return nil, false
			}
			s = to
		}
	}
	if m.pal.open {
		m.paletteKey(s)
		cmd := m.queued
		m.queued = nil
		return cmd, m.quitting
	}
	if q := m.asking; q != nil {
		m.asking = nil
		if s != "y" {
			return nil, false
		}
		return q.run(), m.quitting
	}
	if m.help {
		m.helpKey(s)
		return nil, false
	}
	if m.search.open {
		return m.searchKey(s), false
	}
	if m.paneKey {
		m.paneKey = false
		return m.paneKeyPressed(s), false
	}
	if s == "ctrl+w" && m.phase == phaseMain && !m.typing() {
		if m.repeaterMode() {
			m.addLog(m.now, "PANES  split panes are for companion mode; a repeater's screens show one at a time", radio.ToneInfo)
			return nil, false
		}
		m.paneKey = true
		return nil, false
	}
	if s == "ctrl+f" && m.phase == phaseMain && !m.typing() {
		m.openSearch()
		return nil, false
	}
	if s == "ctrl+k" && m.phase == phaseMain && !m.typing() {
		m.openPalette()
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
	if m.phase == phaseMain && m.tab == 6 {
		if cmd, handled := m.statsKey(s); handled {
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
		if s == "b" && !m.connect.editing && m.connect.mode == "" && m.opt.Scan != nil {
			return m.startScan(), false
		}
		if t := m.connect.items[m.connect.sel]; s == "x" && !m.connect.editing && t.Saved != "" {
			m.ask("forget "+t.Saved+"?", "forget", func() tea.Cmd {
				m.forget(t)
				m.connect.rescan()
				return nil
			})
			return nil, false
		}
		t, chosen, quit := m.connect.key(s, m.now)
		if quit {
			return nil, true
		}
		if chosen {
			m.connect.stopScan()
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
		if why := m.unfinished(); why != "" {
			m.ask("quit? "+why, "quit", func() tea.Cmd { m.quitting = true; return nil })
			return nil, false
		}
		return nil, true
	case ":":
		m.openPalette()
	case "?":
		m.help, m.helpScroll = true, 0
	case "f12":
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

// screenKeys are the help lines for the current screen.
func (m *Model) screenKeys() []string {
	th := m.th
	key := func(k, what string) string { return helpRow(th, k, what) }
	if m.tab == 5 {
		return []string{key("↑↓ j k", "choose a setting; g G the first or last"), key("enter space", "edit it, switch it, or run it"),
			key("u", "read the settings again"), key("c", "open the radio console, on companion firmware v1.18.0 and newer; :radio console too"),
			key("esc", "cancel an edit"),
			key("", "radio changes and a new identity ask for y; a reboot or factory reset needs a word typed"), ""}
	}
	if m.tab == 6 {
		return []string{key("", fmt.Sprintf("the radio's counters are read every %s while this screen is open", statsEvery)),
			key("", "the mesh overview counts the packets heard since connecting"), ""}
	}
	if m.tab == 4 {
		if _, sess, ok := m.openNode(); ok {
			lines := []string{th.Fg(th.Muted, true).Render(strings.ToUpper(paneNames[m.adm.pane]))}
			for _, h := range m.paneHints(sess) {
				lines = append(lines, key(h.k, h.v))
			}
			if m.adm.pane == paneHealth {
				lines = append(lines, key("", "with the cursor: ←→ step through time, g G the oldest or newest, esc stops"))
			}
			if m.adm.pane == paneConsole {
				lines = append(lines, key("", "type commands as on the node's serial console; erasing asks for y, and a reboot, new password or radio change needs the node's name"))
			}
			lines = append(lines, "", th.Fg(th.Muted, true).Render("ANY PANE"))
			if m.adm.local {
				lines = append(lines, key("1-9 ←→", "switch screens: status, health, settings, neighbours, access, regions, telemetry, log, console"))
			} else {
				lines = append(lines, key("←→", "switch between status, health, settings, neighbours, access, regions, telemetry, log and console"),
					key("L", "log in or out"), key("w", fmt.Sprintf("watch the node: polled every %s and kept, for Health", every(m.watchEvery()))), key("esc", "back to the node list"))
			}
			return append(lines, key("B", "battery type: Li-ion, LiPo, LiFePO4, or none when wired, for reading its charge"), "")
		}
		return []string{key("↑↓ j k", "choose a repeater or room server"), key("enter", "open it, logging in first; an empty password is a guest login"),
			key("L", "log in or out"), key("u U", "poll the node now · poll every node shown, one at a time"),
			key("f F", "mark a favourite · show favourites only"), key("x", "forget a saved password"),
			key("w", fmt.Sprintf("watch a node: its status is polled every %s and kept, for the Health screen", every(m.watchEvery()))),
			key("H", "open a node's kept history without logging in"),
			key("B", "battery type: Li-ion, LiPo, LiFePO4, or none when wired, for reading its charge"), ""}
	}
	if m.tab == 2 {
		return []string{key("←↑↓→ h j k l", "move the map"), key("+ -", "zoom in and out"), key("m", "back to your whole mesh"),
			key("[ ]", "pick out a route, newest first; esc shows them all again"),
			key("path", "M on a packet, message or contact shows just its path: [ ] other copies, m fits it, esc goes back"),
			key("d", "download streets: roads, rivers and towns for your mesh's area from OpenFreeMap, or more of it; esc stops"),
			key("n r", "town and node names · main roads"),
			key("space", "the show menu: switch repeaters, companions, room servers, sensors, recent routes, traffic, names and roads on or off"),
			key("t", "the traffic layer: links get more saturated the more packets crossed them lately; t again counts the last hour, then hides it"),
			key("f F", "pick out the busiest nodes' traffic one at a time, forwards or back, fading the rest; esc shows it all again"),
			key("esc", "stop a download, then drop the picked route, then the picked node"),
			key("", fmt.Sprintf("only the busiest %d links are drawn; dashed ones are guesses between repeaters that share a hash", trafficLinks)),
			key("", fmt.Sprintf("the newest %d routes are drawn, fading out over %s; [ ] picks any of the last %d", mapview.RoutesShown, "ten minutes", mapRoutes)),
			key(":map update", "download the street detail again · :map remove deletes it"),
			key("", "only adverts and messages on open channels travel the map, as on the Packets screen"),
			key("", "a hop's colour is its signal where known: measured for the last hop, from traces and neighbour lists for the rest"), ""}
	}
	if m.tab == 3 {
		return []string{key("↑↓ j k", "choose a packet; g G the newest or oldest"), key("enter M", "show the packet's path on the map, with each copy heard"),
			key("[ ]", "step through decoded fields, highlighting their bytes"),
			key("space", "pause, so the list holds still"), key("F", "filter by route: all, flood, direct"), key("+ -", "raise or lower the minimum signal"),
			key("/", "filter by text"), key("y", "copy the packet's bytes"), key("C", "clear the list"),
			key(":export packets", "save the visible packets as JSON lines"), ""}
	}
	if m.tab == 1 {
		return []string{key("↑↓ j k", "choose a contact"), key("enter m", "message them; enter on a repeater opens it in Admin"),
			key("t", "trace the route, showing signal at each hop"),
			key("d", "discover a fresh route"), key("p", "set the route by hand"), key("r", "reset the route, so messages flood"),
			key("f", "favourite"), key("K", "pin their signing key to their name, or unpin it"), key("e", "share as a QR code and meshcore:// link; y copies it"),
			key("x", "remove from the radio"),
			key("l", "place it on the map by hand, for a node that doesn't share a location; empty clears it"),
			key("M", "show the way to them on the map: the newest trace, else the stored route, else their advert's path"),
			key("I", "import a meshcore:// link"), key("a", "nodes heard but not added"), key("A", "auto-add settings"),
			key("P", "suggest contacts to prune"), key("F", "filter by type or favourites"), key("o", "sort"), key("/", "filter by name"), ""}
	}
	if m.tab == 0 && m.chats.board != nil {
		return []string{key("←↑↓→ h j k l", "move around the board"), key("enter space", "pick up one of your pieces, then put it down where it goes; esc puts it back"),
			key("click", "a piece, then where it goes, as enter does"), key("i", "type a move: Nf3, exd5, O-O, or squares like g1f3 and e7e8q"),
			key("f", "turn the board round"), key("n", "pick a side for a new game, w or b; with no game, your first move starts one as white"),
			key("d", "offer a draw, or accept theirs; a move turns an offer down"), key("R", "resign, once you've confirmed it"),
			key("S", "send your position when your boards don't match, so theirs is put back in step with yours"),
			key("r", "send your latest move again if it never got a receipt"),
			key("L", "the board alone, or with the conversation beside it"), key("tab", "between the board and the conversation beside it"),
			key("esc", "leave the board open and go back to the list, to pick another conversation; c comes back to it"),
			key("c", "close the board"), ""}
	}
	if m.tab == 0 {
		return []string{key("↑↓ j k", "choose a conversation; g G the first or last"), key("enter i", "write a message; enter sends"), key("tab", "while writing, complete an @name; again for the next match"),
			key("*bold*", "in messages, *bold*, _italic_, ~struck~ and `code` are drawn that way, as the chat apps write them"),
			key(":name:", "while writing, an emoji: :joy: becomes 😂, and :jo offers a list; ↑↓ choose, tab or enter insert, esc closes it"),
			key("esc", "stop writing, or clear the filter"), key("/", "filter this conversation"),
			key("pgup pgdn", "scroll history; ctrl+u ctrl+d too"),
			key("→", "pick a message: ↑↓ move, g G the oldest or newest, ← or esc goes back"),
			key("r", "reply to the picked message, or the newest one received; in a channel it names them"),
			key("y", "copy the picked message"),
			key("paste", "pastes into whatever you're typing in; in Chats it starts a message"),
			key("S", "sign your messages on the open channel, or stop; ctrl+s while writing flips it for one message"),
			key("K", "on a picked signed message, pin its key to the sender's name, or unpin it; replacing a pin asks first"),
			key("M", "show on the map the route of the picked message, or the newest received"),
			key("R", "send your newest failed message again; on a picked one, R or enter sends that one"),
			key("L", "log in to or out of the room server whose conversation is open"),
			key("m", "mute the open conversation, or unmute it: its messages still arrive, without notifications; :mute 8h mutes for a while"),
			key("n", "add a channel: #name, a private one with its secret, or a shared link; the + row under the channels does the same"),
			key("J", "on a picked message that mentions a #channel you're not in, join it"),
			key("e x", "share or leave the channel that's open; :channel rename renames it"),
			key("c", "play chess with the person whose conversation is open; each move is a message to them"), ""}
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
