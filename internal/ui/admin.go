package ui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

type adminPane int

const (
	paneStatus adminPane = iota
	paneHealth
	paneSettings
	paneNeighbours
	paneACL
	paneRegions
	paneTelemetry
	paneLog
	paneConsole
)

var paneNames = []string{"Status", "Health", "Settings", "Neighbours", "Access", "Regions", "Telemetry", "Log", "Console"}

type adminPrompt int

const (
	noPrompt adminPrompt = iota
	promptPassword
	promptEdit
	promptDiff
	promptConfirm
	promptPick
	promptRegion
)

const (
	authFlash  = 1200 * time.Millisecond
	applyCPS   = 40.0
	logTimeout = 65 * time.Second
)

// confirmAction is a dangerous change waiting for the node's name to be typed.
type confirmAction struct {
	title, warning string
	commands       []string
	apply          bool // send the staged settings first
}

type adminModel struct {
	sel    int
	open   [32]byte
	opened bool
	pane   adminPane
	paneAt time.Time

	prompt  adminPrompt
	input   lineEdit
	confirm confirmAction
	pickSel int

	setSel, aclSel, regSel, nbSel int

	console lineEdit
	history []string
	histAt  int

	applied   []admin.Change
	appliedAt time.Time

	returnTo    *[32]byte // a login started from Chats goes back there when it succeeds
	local       bool      // connected to a repeater's own console over USB
	keysChecked bool
	favOnly     bool
	nodes       []radio.Contact // adminNodes as of nodesFor; shared, so never changed in place
	nodesFor    nodesKey
	remember    bool   // save the password being typed once it works
	loginNote   string // why a saved password wasn't sent, shown at the login
	saving      map[[32]byte]string

	auto     map[[32]byte]bool
	resultAt map[[32]byte]time.Time
	visited  map[[32]byte]map[adminPane]bool

	kept       map[[32]byte][]admin.StatusPoint // watched nodes' status, from the store
	measured   map[healthKey]healthMemo         // kept status measured for the charts, while it stays the same
	week       bool                             // the health charts show seven days instead of one
	picking    bool                             // a cursor is on the health charts, at bucket pick
	pick       int                              // -1 until placed, on the newest poll
	lastTry    map[[32]byte]time.Time           // the last time the watch polled each node
	noPassword map[[32]byte]string              // watched, but there's no way to log in to poll it, and why
	background map[int]bool                     // jobs the watch started, which fail quietly
	watchGen   int
	storeErr   bool
}

func newAdmin() adminModel {
	return adminModel{auto: map[[32]byte]bool{}, resultAt: map[[32]byte]time.Time{}, visited: map[[32]byte]map[adminPane]bool{},
		saving: map[[32]byte]string{}, kept: map[[32]byte][]admin.StatusPoint{}, measured: map[healthKey]healthMemo{}, lastTry: map[[32]byte]time.Time{},
		noPassword: map[[32]byte]string{}, background: map[int]bool{}}
}

type jobSentMsg struct {
	id  int
	tag uint32
	err error
}
type jobTimeoutMsg struct{ id int }
type cmdSentMsg struct {
	c   *admin.Command
	err error
}
type cmdTimeoutMsg struct {
	key [32]byte
	id  int
}
type cmdLateMsg struct {
	key [32]byte
	id  int
}
type autoStatusMsg struct{ key [32]byte }
type watchTickMsg struct{ gen int }
type fleetPollMsg struct{ key [32]byte }
type keysCheckedMsg struct{}

// checkKeys asks the keychain about every node once, in the background, so drawing never waits on it.
func (m *Model) checkKeys() tea.Cmd {
	m.adm.keysChecked = true
	keys, nodes := m.keys, m.adminNodes()
	return func() tea.Msg {
		for _, c := range nodes {
			keys.Get(c.Key)
		}
		return keysCheckedMsg{}
	}
}

func (m *Model) adminConfig() *config.Config {
	if m.opt.Config != nil {
		return m.opt.Config
	}
	if m.cfgFallback == nil {
		m.cfgFallback = config.Default()
	}
	return m.cfgFallback
}

func (m *Model) autoEvery() time.Duration {
	return time.Duration(max(m.adminConfig().Admin.StatusPoll, 10)) * time.Second
}

func (m *Model) thresholds() admin.Thresholds {
	c := m.adminConfig().Admin
	return admin.Thresholds{BatteryVolts: c.BatteryAlert, Stale: time.Duration(c.StaleAfter) * time.Hour, Chemistry: m.chemistry}
}

func (m *Model) alerts(c radio.Contact) admin.Alerts {
	sess, ok := m.admin.Find(c.Key)
	if !ok {
		return admin.Alerts{}
	}
	return admin.Check(sess, c.LastAdvert, m.admin.Newest(), m.thresholds(), m.now)
}

func (m *Model) adminNames(p []byte) string {
	if m.chat != nil {
		for _, c := range m.chat.Contacts() {
			if strings.HasPrefix(string(c.Key[:]), string(p)) {
				return c.Name
			}
		}
	}
	return radio.KeyHex(p)
}

// adminNodes are the repeaters and room servers in the contact table: favourites first, then by name,
// and only favourites while that filter is on.
func (m *Model) adminNodes() []radio.Contact {
	if m.chat == nil {
		return nil
	}
	k := nodesKey{m.chat, m.chat.Rev(), m.adm.favOnly}
	if k == m.adm.nodesFor {
		return m.adm.nodes
	}
	type node struct {
		c     radio.Contact
		fav   bool
		lower string
	}
	var list []node
	for _, c := range m.allNodes() {
		if !m.adm.favOnly || favourite(c) {
			list = append(list, node{c, favourite(c), strings.ToLower(c.Name)})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].fav != list[j].fav {
			return list[i].fav
		}
		return list[i].lower < list[j].lower
	})
	var out []radio.Contact
	for _, n := range list {
		out = append(out, n.c)
	}
	m.adm.nodes, m.adm.nodesFor = out, k
	return out
}

type nodesKey struct {
	chat    *chat.Service
	rev     int
	favOnly bool
}

// allNodes are every repeater and room server in the contact table.
func (m *Model) allNodes() []radio.Contact {
	if m.chat == nil {
		return nil
	}
	var out []radio.Contact
	for _, c := range m.chat.Contacts() {
		if c.Type == radio.TypeRepeater || c.Type == radio.TypeRoom {
			out = append(out, c)
		}
	}
	return out
}

func favourite(c radio.Contact) bool { return c.Flags&radio.FlagFavourite != 0 }

func (m *Model) selectedNode() (radio.Contact, bool) {
	nodes := m.adminNodes()
	if len(nodes) == 0 {
		return radio.Contact{}, false
	}
	m.adm.sel = min(max(m.adm.sel, 0), len(nodes)-1)
	return nodes[m.adm.sel], true
}

// openNode is the node whose dashboard is showing.
func (m *Model) openNode() (radio.Contact, *admin.Session, bool) {
	if !m.adm.opened {
		return radio.Contact{}, nil, false
	}
	for _, c := range m.adminNodes() {
		if c.Key == m.adm.open {
			return c, m.admin.Session(c), true
		}
	}
	return radio.Contact{}, nil, false
}

func (m *Model) startJob(j *admin.Job) tea.Cmd {
	if j == nil {
		return nil
	}
	r, id := m.radio, j.ID
	call := m.radioCall(func(ctx context.Context) tea.Msg {
		var tag uint32
		var err error
		switch j.Kind {
		case admin.JobLogin:
			err = r.Login(ctx, j.Key, j.Password)
		case admin.JobStatus:
			err = r.RequestStatus(ctx, j.Key)
		case admin.JobTelemetry:
			err = r.RequestTelemetry(ctx, j.Key)
		default:
			tag, err = r.BinaryRequest(ctx, j.Key, j.Data)
		}
		return jobSentMsg{id, tag, err}
	})
	return tea.Batch(call, tea.Tick(admin.JobTimeout, func(time.Time) tea.Msg { return jobTimeoutMsg{id} }))
}

func (m *Model) sendCommand(c *admin.Command) tea.Cmd {
	if c == nil {
		return nil
	}
	r := m.radio
	call := m.radioCall(func(ctx context.Context) tea.Msg { return cmdSentMsg{c, r.SendCLI(ctx, c.Key, c.Text)} })
	key, id := c.Key, c.ID
	wait := admin.CommandTimeout
	if c.Purpose == "log" {
		wait = logTimeout // the whole log comes over USB, which takes a while when it's long
	}
	return tea.Batch(call, tea.Tick(wait, func(time.Time) tea.Msg { return cmdTimeoutMsg{key, id} }))
}

// adminEvent handles radio events for admin sessions.
func (m *Model) adminEvent(e radio.Event) tea.Cmd {
	now := m.now
	switch e := e.(type) {
	case radio.Ready:
		m.adm.local = e.Console
		watch := m.startWatch()
		if !e.Console || len(e.ContactList) == 0 {
			return watch
		}
		self := e.ContactList[0]
		m.lay, m.tab, m.tabAt = newLayout(4), 4, now
		m.adm.open, m.adm.opened, m.adm.pane = self.Key, true, paneStatus
		return tea.Batch(watch, m.startJob(m.admin.Login(self, "", now)))
	case radio.LoginResult:
		sess, next := m.admin.OnLogin(e, now)
		if sess == nil {
			return nil
		}
		m.adm.resultAt[sess.Key] = now
		cmds := []tea.Cmd{m.startJob(next)}
		pw, saving := m.adm.saving[sess.Key]
		delete(m.adm.saving, sess.Key)
		if !e.OK {
			m.fail("LOGIN FAILED  " + sess.Name + ": wrong password, or not allowed")
			return tea.Batch(cmds...)
		}
		if saving {
			if err := m.opt.Keychain.Set(sess.Key, pw); err != nil {
				m.addLog(now, "PASSWORD NOT SAVED  "+err.Error(), radio.ToneWarn)
			} else {
				m.addLog(now, "PASSWORD SAVED  for "+sess.Name+" in "+m.opt.Keychain.Where(), radio.ToneOK)
			}
		}
		m.addLog(now, fmt.Sprintf("[ OK ] %s  %s", strings.ToUpper(admin.LevelName(sess.Level)), sess.Name), radio.ToneOK)
		if r := m.adm.returnTo; r != nil && *r == sess.Key {
			m.adm.returnTo = nil
			m.setTab(0)
		}
		c := radio.Contact{Key: sess.Key, Name: sess.Name, Type: sess.Type}
		cmds = append(cmds, m.startJob(m.admin.RequestStatus(c, now)), m.startJob(m.admin.RequestOwner(c, now)))
		return tea.Batch(cmds...)
	case radio.StatusReport:
		sess, next := m.admin.OnStatus(e, now)
		if sess != nil && m.watched(sess.Key) {
			m.keepStatus(sess, e.Data)
		}
		return m.startJob(next)
	case radio.Telemetry:
		_, next := m.admin.OnTelemetry(e, now)
		return m.startJob(next)
	case radio.BinaryReply:
		_, next := m.admin.OnBinary(e, now)
		return m.startJob(next)
	case radio.Incoming:
		if !e.CLI {
			return nil
		}
		sess, done, next := m.admin.OnReply(e.From, e.Text, now)
		if sess == nil {
			return nil
		}
		cmds := []tea.Cmd{m.sendCommand(next)}
		if done != nil && done.Purpose == "acl" && strings.HasPrefix(e.Text, "OK") {
			cmds = append(cmds, m.startJob(m.admin.RequestACL(radio.Contact{Key: sess.Key, Name: sess.Name, Type: sess.Type}, now)))
		}
		return tea.Batch(cmds...)
	}
	return nil
}

// adminMsg handles the results of admin radio calls and timers.
func (m *Model) adminMsg(msg tea.Msg) tea.Cmd {
	now := m.now
	switch msg := msg.(type) {
	case jobSentMsg:
		if msg.err != nil && m.adm.background[msg.id] {
			m.watchMissed(msg.id, radio.Explain(msg.err))
			return m.startJob(m.admin.Failed(msg.id, msg.err, now))
		}
		if msg.err != nil {
			j, _ := m.admin.Running()
			what := "REQUEST"
			if j != nil && j.ID == msg.id {
				what = strings.ToUpper(j.Kind.String())
			}
			m.fail(what + "  " + radio.Explain(msg.err))
			return m.startJob(m.admin.Failed(msg.id, msg.err, now))
		}
		_, next := m.admin.Sent(msg.id, msg.tag, now)
		return m.startJob(next)
	case jobTimeoutMsg:
		j, _ := m.admin.Running()
		next, waiting := m.admin.TimedOut(msg.id, now)
		if waiting && m.adm.background[msg.id] {
			m.watchMissed(msg.id, "no answer")
		} else if waiting {
			m.fail(fmt.Sprintf("NO REPLY  %s from %s after %s; try again, or reset the route on the Contacts screen so it floods",
				j.Kind, m.contactName(j.Key), admin.JobTimeout))
		}
		return m.startJob(next)
	case cmdSentMsg:
		return m.sendCommand(m.admin.CommandSent(msg.c, msg.err, now))
	case cmdTimeoutMsg:
		if !m.admin.CommandTimedOut(msg.key, msg.id, now) {
			return nil
		}
		key, id := msg.key, msg.id
		return tea.Tick(admin.LateWindow, func(time.Time) tea.Msg { return cmdLateMsg{key, id} })
	case cmdLateMsg:
		return m.sendCommand(m.admin.LateOver(msg.key, msg.id, now))
	case keysCheckedMsg:
		// nothing to do: the redraw that follows shows which nodes have a saved password
	case watchTickMsg:
		if msg.gen != m.adm.watchGen {
			return nil
		}
		return tea.Batch(m.pollWatched(), m.watchTick())
	case autoStatusMsg:
		sess, ok := m.admin.Find(msg.key)
		if !ok || !m.adm.auto[msg.key] || sess.State != admin.LoggedIn {
			delete(m.adm.auto, msg.key)
			return nil
		}
		return tea.Batch(m.startJob(m.admin.RequestStatus(radio.Contact{Key: sess.Key, Name: sess.Name, Type: sess.Type}, now)), m.autoStatusTick(msg.key))
	case fleetPollMsg:
		for _, c := range m.adminNodes() {
			if c.Key != msg.key {
				continue
			}
			if sess, ok := m.admin.Find(c.Key); ok && sess.State == admin.LoggedIn {
				return m.startJob(m.admin.RequestStatus(c, now))
			}
			if pw, ok := m.opt.Keychain.Get(c.Key); ok {
				if safe, _ := m.savedPasswordsOK(); safe {
					return m.startJob(m.admin.Login(c, pw, now))
				}
			}
		}
	}
	return nil
}

func (m *Model) autoStatusTick(k [32]byte) tea.Cmd {
	return tea.Tick(m.autoEvery(), func(time.Time) tea.Msg { return autoStatusMsg{k} })
}

// pollFleet polls every node it can reach, spaced out to spare airtime.
func (m *Model) pollFleet() tea.Cmd {
	safe, why := m.savedPasswordsOK()
	var keys [][32]byte
	for _, c := range m.adminNodes() {
		sess, ok := m.admin.Find(c.Key)
		_, saved := m.opt.Keychain.Get(c.Key)
		if saved && safe || ok && sess.State == admin.LoggedIn {
			keys = append(keys, c.Key)
		}
	}
	if len(keys) == 0 && !safe {
		m.fail("POLL  saved passwords aren't sent: " + why + "; log in to a node yourself to poll it")
		return nil
	}
	if len(keys) == 0 {
		m.fail("POLL  no nodes to poll: log in to one, or save its password with ctrl+s at the login")
		return nil
	}
	spacing := time.Duration(max(m.adminConfig().Admin.FleetSpacing, 1)) * time.Second
	m.addLog(m.now, fmt.Sprintf("POLLING  %d nodes, one every %s", len(keys), spacing), radio.ToneInfo)
	var cmds []tea.Cmd
	for i, k := range keys {
		cmds = append(cmds, tea.Tick(time.Duration(i)*spacing+time.Millisecond, func(time.Time) tea.Msg { return fleetPollMsg{k} }))
	}
	return tea.Batch(cmds...)
}

// adminKey handles keys on the Admin screen; unhandled keys fall through to the app.
func (m *Model) adminKey(s string) (tea.Cmd, bool) {
	a := &m.adm
	if a.prompt != noPrompt {
		return m.adminPromptKey(s), true
	}
	if !a.opened {
		return m.nodeListKey(s)
	}
	ct, sess, ok := m.openNode()
	if !ok {
		a.opened = false
		return nil, false
	}
	if a.pane == paneHealth && a.picking && m.healthPickKey(s) {
		return nil, true
	}
	switch s {
	case "left":
		return m.setPane((a.pane + adminPane(len(paneNames)) - 1) % adminPane(len(paneNames))), true
	case "right":
		return m.setPane((a.pane + 1) % adminPane(len(paneNames))), true
	}
	if a.pane == paneConsole {
		return m.consoleKey(s, ct, sess)
	}
	switch s {
	case "esc":
		m.closeNode()
		return nil, true
	case "L":
		if a.local {
			return nil, true
		}
		if sess.State != admin.LoggedIn {
			m.openLogin()
			return nil, true
		}
		return m.logout(ct), true
	case "w":
		if !a.local {
			m.toggleWatch(ct)
		}
		return nil, true
	case "B":
		m.cycleBattery(ct.Key, ct.Name)
		return nil, true
	}
	if a.pane == paneHealth {
		if cmd, ok := m.healthKey(s, ct, sess); ok {
			return cmd, true
		}
	}
	if sess.State != admin.LoggedIn {
		if s == "enter" {
			m.openLogin()
			return nil, true
		}
		return nil, false
	}
	switch a.pane {
	case paneStatus:
		return m.statusKey(s, ct, sess)
	case paneSettings:
		return m.settingsKey(s, ct, sess)
	case paneNeighbours:
		return m.neighboursKey(s, ct, sess)
	case paneTelemetry:
		if s == "u" {
			return m.refreshPane(ct, sess), true
		}
	case paneACL:
		return m.aclKey(s, ct, sess)
	case paneRegions:
		return m.regionsKey(s, ct, sess)
	case paneLog:
		return m.logKey(s, ct, sess)
	}
	return nil, false
}

func (m *Model) nodeListKey(s string) (tea.Cmd, bool) {
	a := &m.adm
	switch s {
	case "up", "k":
		a.sel = max(a.sel-1, 0)
	case "down", "j":
		a.sel = min(a.sel+1, max(len(m.adminNodes())-1, 0))
	case "enter":
		ct, ok := m.selectedNode()
		if !ok {
			return nil, false
		}
		if ct.Key != a.open {
			a.pane = paneStatus
		}
		a.open, a.opened = ct.Key, true
		if sess := m.admin.Session(ct); sess.State != admin.LoggedIn && sess.State != admin.LoggingIn {
			if pw, ok := m.opt.Keychain.Get(ct.Key); ok && sess.State != admin.LoginFailed {
				return m.savedLogin(ct, pw), true
			}
			m.openLogin()
			return nil, true
		}
		return m.setPane(a.pane), true
	case "L":
		ct, ok := m.selectedNode()
		if !ok {
			return nil, false
		}
		if sess, ok := m.admin.Find(ct.Key); ok && sess.State == admin.LoggedIn {
			return m.logout(ct), true
		}
		return m.nodeListKey("enter")
	case "x":
		if ct, ok := m.selectedNode(); ok {
			if _, saved := m.opt.Keychain.Get(ct.Key); !saved {
				m.fail("PASSWORD  none saved for " + ct.Name)
				return nil, true
			}
			m.ask("forget the saved password for "+ct.Name+"?", "forget", func() tea.Cmd {
				m.opt.Keychain.Forget(ct.Key)
				m.addLog(m.now, "PASSWORD FORGOTTEN  for "+ct.Name, radio.ToneInfo)
				return nil
			})
		}
	case "u":
		ct, ok := m.selectedNode()
		if !ok {
			return nil, false
		}
		if sess, ok := m.admin.Find(ct.Key); !ok || sess.State != admin.LoggedIn {
			m.fail("POLL  log in to " + ct.Name + " first, with enter")
			return nil, true
		}
		return m.startJob(m.admin.RequestStatus(ct, m.now)), true
	case "U":
		return m.pollFleet(), true
	case "w":
		if ct, ok := m.selectedNode(); ok {
			m.toggleWatch(ct)
		}
	case "B":
		if ct, ok := m.selectedNode(); ok {
			m.cycleBattery(ct.Key, ct.Name)
		}
	case "H":
		if ct, ok := m.selectedNode(); ok {
			m.admin.Session(ct)
			a.open, a.opened, a.pane, a.paneAt = ct.Key, true, paneHealth, m.now
		}
	case "F":
		a.favOnly, a.sel = !a.favOnly, 0
	case "f":
		if ct, ok := m.selectedNode(); ok {
			updated := ct
			updated.Flags ^= radio.FlagFavourite
			what := "FAVOURITE  " + ct.Name
			if !favourite(updated) {
				what = "UNFAVOURITED  " + ct.Name
			}
			return m.contactOp(what, func(ctx context.Context, r radio.Radio) error { return r.SaveContact(ctx, updated) }), true
		}
	default:
		return nil, false
	}
	return nil, true
}

// openLogin asks for a password, with remember-it off so a password is only saved for the node it was ticked for.
func (m *Model) openLogin() {
	m.adm.prompt, m.adm.input, m.adm.remember, m.adm.loginNote = promptPassword, lineEdit{}, false, ""
}

// savedLogin logs in with a saved password when the radio is one it's safe to hand it to. When the radio is
// verified but the link may not be encrypted it asks first; otherwise the password is typed.
func (m *Model) savedLogin(ct radio.Contact, pw string) tea.Cmd {
	safe, why := m.savedPasswordsOK()
	switch {
	case safe:
		return m.startJob(m.admin.Login(ct, pw, m.now))
	case m.trust == trustVerified:
		m.ask("send the saved password for "+ct.Name+"? "+why, "send", func() tea.Cmd { return m.startJob(m.admin.Login(ct, pw, m.now)) })
		return nil
	}
	m.openLogin()
	m.adm.loginNote = "The saved password isn't sent: " + why + "."
	return nil
}

func (m *Model) logout(ct radio.Contact) tea.Cmd {
	sess, ok := m.admin.Find(ct.Key)
	if !ok || sess.State != admin.LoggedIn {
		return nil
	}
	m.admin.Logout(ct.Key)
	delete(m.adm.auto, ct.Key)
	return m.contactOp("LOGGED OUT  "+ct.Name, func(ctx context.Context, r radio.Radio) error { return r.Logout(ctx, ct.Key) })
}

// setPane switches pane, fetching its data the first time it's shown for a node.
func (m *Model) setPane(p adminPane) tea.Cmd {
	a := &m.adm
	if p != a.pane {
		a.pane, a.paneAt, a.picking = p, m.now, false
	}
	ct, sess, ok := m.openNode()
	if !ok || sess.State != admin.LoggedIn {
		return nil
	}
	if a.visited[ct.Key] == nil {
		a.visited[ct.Key] = map[adminPane]bool{}
	}
	if a.visited[ct.Key][p] {
		return nil
	}
	a.visited[ct.Key][p] = true
	if (p == paneStatus || p == paneHealth) && len(sess.Status) > 0 {
		return nil
	}
	return m.refreshPane(ct, sess)
}

func (m *Model) refreshPane(ct radio.Contact, sess *admin.Session) tea.Cmd {
	now := m.now
	switch m.adm.pane {
	case paneStatus, paneHealth:
		return m.startJob(m.admin.RequestStatus(ct, now))
	case paneSettings:
		if sess.Admin {
			rows := m.settingRows(sess)
			return m.sendCommand(m.admin.ReadSettings(ct.Key, rows[min(m.adm.setSel, len(rows)-1)].Group, now))
		}
	case paneNeighbours:
		return m.startJob(m.admin.RequestNeighbours(ct, now))
	case paneACL:
		if sess.Admin {
			return m.startJob(m.admin.RequestACL(ct, now))
		}
	case paneRegions:
		if sess.Admin {
			return m.sendCommand(m.admin.Command(ct.Key, "region", "region", now))
		}
	case paneTelemetry:
		return m.startJob(m.admin.RequestTelemetry(ct, now))
	}
	return nil
}

func (m *Model) cli(key [32]byte, text, purpose string) tea.Cmd {
	return m.sendCommand(m.admin.Command(key, text, purpose, m.now))
}

func (m *Model) statusKey(s string, ct radio.Contact, sess *admin.Session) (tea.Cmd, bool) {
	a := &m.adm
	switch s {
	case "u":
		return m.startJob(m.admin.RequestStatus(ct, m.now)), true
	case "a":
		a.auto[ct.Key] = !a.auto[ct.Key]
		if a.auto[ct.Key] {
			m.addLog(m.now, fmt.Sprintf("AUTO-REFRESH  %s every %s", ct.Name, m.autoEvery()), radio.ToneInfo)
			return m.autoStatusTick(ct.Key), true
		}
		delete(a.auto, ct.Key)
		return nil, true
	}
	if !sess.Admin {
		return nil, false
	}
	switch s {
	case "A":
		return m.cli(ct.Key, "advert", ""), true
	case "c":
		return m.cli(ct.Key, "clock sync", ""), true
	case "R":
		m.askConfirm(confirmAction{title: "REBOOT " + strings.ToUpper(ct.Name), warning: "It drops off the mesh for a minute and you'll need to log in again.", commands: []string{"reboot"}})
		return nil, true
	}
	return nil, false
}

func (m *Model) askConfirm(c confirmAction) {
	m.adm.prompt, m.adm.input, m.adm.confirm = promptConfirm, lineEdit{}, c
}

// settingRows is the schema for the open node, minus keys it has said it doesn't have.
func (m *Model) settingRows(sess *admin.Session) []admin.Setting {
	var out []admin.Setting
	for _, st := range admin.Settings(sess.Type) {
		if v, ok := sess.Settings[st.Key]; ok && strings.HasPrefix(v.Err, "Unknown") {
			continue
		}
		out = append(out, st)
	}
	return out
}

func (m *Model) settingsKey(s string, ct radio.Contact, sess *admin.Session) (tea.Cmd, bool) {
	a := &m.adm
	if !sess.Admin {
		return nil, false
	}
	rows := m.settingRows(sess)
	a.setSel = min(a.setSel, len(rows)-1)
	st := rows[a.setSel]
	switch s {
	case "up", "k":
		a.setSel = max(a.setSel-1, 0)
	case "down", "j":
		a.setSel = min(a.setSel+1, len(rows)-1)
	case "g", "home":
		a.setSel = 0
	case "G", "end":
		a.setSel = len(rows) - 1
	case "u":
		return m.sendCommand(m.admin.ReadSettings(ct.Key, st.Group, m.now)), true
	case "U":
		return m.sendCommand(m.admin.ReadSettings(ct.Key, "", m.now)), true
	case "x":
		if _, ok := sess.Pending[st.Key]; ok {
			delete(sess.Pending, st.Key)
			m.addLog(m.now, "DISCARDED  your edit to "+st.Label, radio.ToneInfo)
		}
	case "s":
		if len(m.admin.Changes(ct.Key)) == 0 {
			m.fail("NOTHING TO APPLY  edit a setting with enter first")
			return nil, true
		}
		a.prompt = promptDiff
	case "enter", "space":
		if st.ReadOnly {
			m.fail("READ ONLY  " + st.Label + " can't be changed")
			return nil, true
		}
		cur := sess.Settings[st.Key].Value
		if p, ok := sess.Pending[st.Key]; ok {
			cur = p
		}
		switch st.Kind {
		case admin.KindBool:
			m.stage(ct, st, map[bool]string{true: "off", false: "on"}[cur == "on"])
		case admin.KindChoice:
			i := slices.Index(st.Options, cur)
			m.stage(ct, st, st.Options[(i+1)%len(st.Options)])
		default:
			a.prompt, a.input = promptEdit, newLine(cur)
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) stage(ct radio.Contact, st admin.Setting, v string) bool {
	if err := m.admin.Stage(ct.Key, st.Key, v); err != nil {
		m.fail("INVALID  " + err.Error())
		return false
	}
	return true
}

func (m *Model) applySettings(ct radio.Contact, reboot bool) tea.Cmd {
	a := &m.adm
	a.applied, a.appliedAt = m.admin.Changes(ct.Key), m.now
	first, needsReboot := m.admin.Apply(ct.Key, m.now)
	cmds := []tea.Cmd{m.sendCommand(first)}
	switch {
	case reboot:
		cmds = append(cmds, m.cli(ct.Key, "reboot", ""))
	case needsReboot:
		m.addLog(m.now, "SETTINGS  "+ct.Name+": some changes apply after a reboot (R on Status)", radio.ToneWarn)
	}
	return tea.Batch(cmds...)
}

func (m *Model) neighboursKey(s string, ct radio.Contact, sess *admin.Session) (tea.Cmd, bool) {
	a := &m.adm
	switch s {
	case "u":
		return m.refreshPane(ct, sess), true
	case "up", "k":
		a.nbSel = max(a.nbSel-1, 0)
		return nil, true
	case "down", "j":
		a.nbSel = min(a.nbSel+1, max(len(sess.Neighbours)-1, 0))
		return nil, true
	}
	if !sess.Admin {
		return nil, false
	}
	switch s {
	case "d":
		m.addLog(m.now, "DISCOVERING NEIGHBOURS  "+ct.Name+" asks nearby repeaters to answer; refresh with u in a minute", radio.ToneInfo)
		return m.cli(ct.Key, "discover.neighbors", ""), true
	case "x":
		if a.nbSel < len(sess.Neighbours) {
			n := sess.Neighbours[a.nbSel]
			m.ask("remove neighbour "+radio.KeyHex(n.Prefix)+" from "+ct.Name+"?", "remove", func() tea.Cmd {
				return tea.Batch(m.cli(ct.Key, "neighbor.remove "+radio.KeyHex(n.Prefix), ""), m.startJob(m.admin.RequestNeighbours(ct, m.now)))
			})
			return nil, true
		}
	}
	return nil, false
}

func (m *Model) aclKey(s string, ct radio.Contact, sess *admin.Session) (tea.Cmd, bool) {
	a := &m.adm
	if !sess.Admin {
		return nil, false
	}
	switch s {
	case "u":
		return m.startJob(m.admin.RequestACL(ct, m.now)), true
	case "up", "k":
		a.aclSel = max(a.aclSel-1, 0)
		return nil, true
	case "down", "j":
		a.aclSel = min(a.aclSel+1, max(len(sess.ACL)-1, 0))
		return nil, true
	case "n":
		a.prompt, a.pickSel = promptPick, 0
		return nil, true
	}
	if a.aclSel >= len(sess.ACL) {
		return nil, false
	}
	row := sess.ACL[a.aclSel]
	switch s {
	case "x":
		m.ask("take "+row.Name+"'s access to "+ct.Name+" away?", "remove", func() tea.Cmd {
			return m.cli(ct.Key, "setperm "+radio.KeyHex(row.Prefix[:])+" 0", "acl")
		})
		return nil, true
	case "+", "=", "-":
		level := min(row.Level+1, 3)
		if s == "-" {
			level = max(row.Level, 2) - 1 // lowering past read-only would remove them; that's x
		}
		full, ok := m.fullKey(row.Prefix)
		if !ok {
			m.fail("ACCESS  " + row.Name + " isn't a contact, so its full key is unknown; the node needs it to change a level")
			return nil, true
		}
		return m.cli(ct.Key, fmt.Sprintf("setperm %s %d", radio.KeyHex(full[:]), level), "acl"), true
	}
	return nil, false
}

func (m *Model) fullKey(p [6]byte) ([32]byte, bool) {
	if m.chat != nil {
		if c, ok := m.chat.ContactByPrefix(p); ok {
			return c.Key, true
		}
	}
	return [32]byte{}, false
}

func (m *Model) regionsKey(s string, ct radio.Contact, sess *admin.Session) (tea.Cmd, bool) {
	a := &m.adm
	if !sess.Admin {
		return nil, false
	}
	refresh := func(cmd string) tea.Cmd {
		return tea.Batch(m.cli(ct.Key, cmd, ""), m.cli(ct.Key, "region", "region"))
	}
	switch s {
	case "u":
		return m.cli(ct.Key, "region", "region"), true
	case "up", "k":
		a.regSel = max(a.regSel-1, 0)
		return nil, true
	case "down", "j":
		a.regSel = min(a.regSel+1, max(len(sess.Regions)-1, 0))
		return nil, true
	case "s":
		return m.cli(ct.Key, "region save", ""), true
	}
	if a.regSel >= len(sess.Regions) {
		return nil, false
	}
	r := sess.Regions[a.regSel]
	switch s {
	case "n":
		a.prompt, a.input = promptRegion, lineEdit{}
		return nil, true
	case "h":
		return refresh("region home " + r.Name), true
	case "f":
		verb := "denyf"
		if !r.Flood {
			verb = "allowf"
		}
		return refresh("region " + verb + " " + r.Name), true
	case "x":
		if r.Name == "*" {
			m.fail("REGIONS  the * region can't be removed")
			return nil, true
		}
		m.ask("remove region "+r.Name+" from "+ct.Name+"?", "remove", func() tea.Cmd { return refresh("region remove " + r.Name) })
		return nil, true
	}
	return nil, false
}

func (m *Model) logKey(s string, ct radio.Contact, sess *admin.Session) (tea.Cmd, bool) {
	if !sess.Admin {
		return nil, false
	}
	switch s {
	case "s":
		return m.cli(ct.Key, "log start", ""), true
	case "t":
		return m.cli(ct.Key, "log stop", ""), true
	case "r":
		if !m.adm.local {
			m.fail("LOG  reading the log needs a USB cable to the node")
			return nil, true
		}
		return m.cli(ct.Key, "log", "log"), true
	case "C":
		m.ask("erase the packet log on "+ct.Name+"?", "erase", func() tea.Cmd { return m.cli(ct.Key, "log erase", "") })
		return nil, true
	}
	return nil, false
}

// dangerous reports why a console command needs the node's name typed before it's sent: it takes the node
// off the air, strands it, wipes it, or replaces its identity or password. Erasing the log asks for y instead.
// Commands are matched by prefix, ignoring case and extra spaces, since the firmware matches on prefixes too.
func dangerous(cmd string) string {
	line := consoleLine(cmd)
	f := strings.Fields(line)
	if len(f) == 0 {
		return ""
	}
	has := func(p string) bool { return strings.HasPrefix(line, p) }
	switch {
	case has("reboot"), has("clkreboot"), has("poweroff"), has("shutdown"):
		return "The node goes off the mesh until it restarts."
	case has("start ota"):
		return "The node stops routing and waits for a firmware upload."
	case has("erase"):
		return "This erases data on the node, and it can't be undone."
	case has("password"):
		return "This changes the admin password. Note it down: there's no recovery over the air."
	case f[0] != "set" || len(f) < 2:
		return ""
	}
	switch key := f[1]; key {
	case "prv.key":
		return "This replaces the node's identity. Every contact will see it as a new node."
	case "radio", "freq", "bw", "sf", "cr":
		return "A wrong value strands the node off-frequency, out of reach. Try `tempradio` first: it reverts by itself."
	case "password", "guest.password":
		return "This changes a password on the node. Note it down: there's no recovery over the air."
	default:
		if st, ok := admin.Lookup(key); !ok {
			return "This app doesn't know what " + key + " does, so check the value before it goes to the node."
		} else if st.Dangerous {
			return st.Help
		}
	}
	return ""
}

func consoleLine(cmd string) string { return strings.Join(strings.Fields(strings.ToLower(cmd)), " ") }

func (m *Model) consoleKey(s string, ct radio.Contact, sess *admin.Session) (tea.Cmd, bool) {
	a := &m.adm
	switch s {
	case "esc":
		if a.console.text == "" && a.local {
			return m.setPane(paneStatus), true
		}
		if a.console.text == "" {
			a.opened = false
		}
		a.console = lineEdit{}
	case "enter":
		cmd := strings.TrimSpace(a.console.text)
		if cmd == "" {
			return nil, true
		}
		if !sess.Admin {
			m.fail("CONSOLE  needs an admin login")
			return nil, true
		}
		if !admin.Secret(cmd) {
			a.history = append(a.history, cmd)
		}
		a.histAt, a.console = len(a.history), lineEdit{}
		if why := dangerous(cmd); why != "" {
			m.askConfirm(confirmAction{title: strings.ToUpper(cmd) + " ON " + strings.ToUpper(ct.Name), warning: why, commands: []string{cmd}})
			return nil, true
		}
		if strings.HasPrefix(consoleLine(cmd), "log erase") {
			m.ask(cmd+" on "+ct.Name+"? it deletes data on the node", "send", func() tea.Cmd { return m.cli(ct.Key, cmd, purposeOf(cmd)) })
			return nil, true
		}
		return m.cli(ct.Key, cmd, purposeOf(cmd)), true
	case "up":
		if a.histAt > 0 {
			a.histAt--
			a.console.set(a.history[a.histAt])
		}
	case "down":
		if a.histAt < len(a.history)-1 {
			a.histAt++
			a.console.set(a.history[a.histAt])
		} else {
			a.histAt, a.console = len(a.history), lineEdit{}
		}
	case "tab":
		a.console.set(completeConsole(a.console.text, sess.Type))
	default:
		a.console.key(s)
	}
	return nil, true
}

// purposeOf lets a typed get or set fill in the settings form too.
func purposeOf(cmd string) string {
	f := strings.Fields(cmd)
	if len(f) >= 2 && (f[0] == "get" || f[0] == "set") {
		if _, ok := admin.Lookup(f[1]); ok && f[0] == "get" {
			return "get " + f[1]
		}
	}
	if cmd == "region" {
		return "region"
	}
	return ""
}

var consoleWords = []string{"get", "set", "ver", "board", "clock", "clock sync", "advert", "advert.zerohop", "neighbors", "reboot",
	"log start", "log stop", "log erase", "clear stats", "region", "region home", "region put", "region remove", "region allowf",
	"region denyf", "region save", "setperm", "password", "tempradio", "start ota", "powersaving", "powersaving on",
	"powersaving off", "gps", "gps on", "gps off", "gps sync", "gps setloc", "gps advert", "neighbors", "neighbor.remove",
	"discover.neighbors", "stats-core", "stats-radio", "stats-packets", "get acl", "log"}

// completeConsole extends the line to the longest prefix its matches share: commands, then setting keys after get/set.
func completeConsole(in string, t radio.ContactType) string {
	var cands []string
	if verb, rest, ok := strings.Cut(in, " "); ok && (verb == "get" || verb == "set") && !strings.Contains(rest, " ") {
		for _, st := range admin.Settings(t) {
			if strings.HasPrefix(st.Key, rest) {
				cands = append(cands, verb+" "+st.Key)
			}
		}
	} else {
		for _, w := range consoleWords {
			if strings.HasPrefix(w, in) {
				cands = append(cands, w)
			}
		}
	}
	if len(cands) == 0 {
		return in
	}
	common := cands[0]
	for _, c := range cands[1:] {
		for !strings.HasPrefix(c, common) {
			common = common[:len(common)-1]
		}
	}
	if len(cands) == 1 {
		common += " "
	}
	return common
}

func (m *Model) adminPromptKey(s string) tea.Cmd {
	a := &m.adm
	ct, sess, ok := m.openNode()
	if !ok {
		a.prompt = noPrompt
		return nil
	}
	if s == "esc" {
		a.prompt = noPrompt
		if sess.State != admin.LoggedIn && sess.State != admin.LoggingIn {
			m.closeNode()
		}
		return nil
	}
	typing := func() {
		a.input.key(s)
	}
	switch a.prompt {
	case promptPassword:
		switch s {
		case "ctrl+s":
			a.remember = !a.remember
			return nil
		case "enter":
		default:
			typing()
			return nil
		}
		a.prompt = noPrompt
		delete(a.visited, ct.Key)
		if a.remember {
			a.saving[ct.Key] = a.input.text
		}
		return m.startJob(m.admin.Login(ct, a.input.text, m.now))
	case promptEdit:
		if s != "enter" {
			typing()
			return nil
		}
		st := m.settingRows(sess)[a.setSel]
		if m.stage(ct, st, strings.TrimSpace(a.input.text)) {
			a.prompt = noPrompt
		}
	case promptRegion:
		if s != "enter" {
			typing()
			return nil
		}
		a.prompt = noPrompt
		name := strings.TrimSpace(a.input.text)
		if name == "" || strings.Contains(name, " ") {
			m.fail("REGIONS  a region name is one word")
			return nil
		}
		parent := sess.Regions[a.regSel].Name
		cmd := "region put " + name
		if parent != "*" {
			cmd += " " + parent
		}
		return tea.Batch(m.cli(ct.Key, cmd, ""), m.cli(ct.Key, "region", "region"))
	case promptDiff:
		switch s {
		case "y", "Y":
			changes := m.admin.Changes(ct.Key)
			if s == "Y" && slices.ContainsFunc(changes, func(ch admin.Change) bool { return ch.Setting.Reboot }) {
				warning := "It drops off the mesh for a minute and you'll need to log in again."
				for _, ch := range changes {
					if ch.Setting.Dangerous {
						warning = ch.Setting.Help + " " + warning
					}
				}
				m.askConfirm(confirmAction{title: "APPLY SETTINGS TO " + strings.ToUpper(ct.Name) + " AND REBOOT IT", apply: true, commands: []string{"reboot"}, warning: warning})
				return nil
			}
			if s == "Y" {
				a.prompt = noPrompt
				return nil
			}
			for _, ch := range changes {
				if ch.Setting.Dangerous {
					m.askConfirm(confirmAction{title: "APPLY SETTINGS TO " + strings.ToUpper(ct.Name), warning: ch.Setting.Help, apply: true})
					return nil
				}
			}
			a.prompt = noPrompt
			return m.applySettings(ct, false)
		default:
			a.prompt = noPrompt
		}
	case promptConfirm:
		if s != "enter" {
			typing()
			return nil
		}
		if !strings.EqualFold(strings.TrimSpace(a.input.text), ct.Name) {
			m.fail("NOT CONFIRMED  type the node's name exactly to go ahead")
			return nil
		}
		a.prompt = noPrompt
		c := a.confirm
		if c.apply {
			return m.applySettings(ct, slices.Contains(c.commands, "reboot"))
		}
		var cmds []tea.Cmd
		for _, cmd := range c.commands {
			cmds = append(cmds, m.cli(ct.Key, cmd, ""))
		}
		return tea.Batch(cmds...)
	case promptPick:
		people := m.aclCandidates(sess)
		switch s {
		case "up", "k":
			a.pickSel = max(a.pickSel-1, 0)
		case "down", "j":
			a.pickSel = min(a.pickSel+1, max(len(people)-1, 0))
		case "enter":
			a.prompt = noPrompt
			if a.pickSel < len(people) {
				return m.cli(ct.Key, "setperm "+radio.KeyHex(people[a.pickSel].Key[:])+" 1", "acl")
			}
		}
	}
	return nil
}

// aclCandidates are chat contacts not already on the node's access list.
func (m *Model) aclCandidates(sess *admin.Session) []radio.Contact {
	var out []radio.Contact
	if m.chat == nil {
		return nil
	}
	for _, c := range m.chat.Contacts() {
		if c.Type != radio.TypeChat {
			continue
		}
		listed := slices.ContainsFunc(sess.ACL, func(r admin.ACLRow) bool { return r.Prefix == c.Prefix() })
		if !listed {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (m *Model) adminAnimating() bool {
	now := m.now
	if j, ok := m.admin.Running(); ok && j != nil {
		return true
	}
	for k, at := range m.adm.resultAt {
		if motion.Running(at, authFlash, now) {
			return true
		}
		if sess, ok := m.admin.Find(k); ok && (sess.Busy() || motion.Running(lastStatus(sess), countDur, now)) {
			return true
		}
	}
	if _, sess, ok := m.openNode(); ok {
		for _, ch := range m.adm.applied {
			if v := sess.Settings[ch.Setting.Key]; motion.Running(v.At, motion.TypeDur(ch.To, applyCPS), now) {
				return true
			}
		}
	}
	return m.adm.opened && motion.Running(m.adm.paneAt, healthReveal, now)
}

func lastStatus(sess *admin.Session) time.Time {
	if p, ok := sess.Latest(); ok {
		return p.At
	}
	return time.Time{}
}

func statsOf(p admin.StatusPoint) meshcore.CommonStats { return p.Stats.CommonStats }

// selectedRoom is the room server whose conversation is open in Chats.
func (m *Model) selectedRoom() (radio.Contact, bool) {
	cur, ok := m.selectedConvo()
	if !ok || m.chat == nil {
		return radio.Contact{}, false
	}
	ct, ok := m.chat.Contact(cur.Convo)
	return ct, ok && ct.Type == radio.TypeRoom
}

// openInAdmin opens a repeater on the Admin screen, logging in as enter there would.
func (m *Model) openInAdmin(ct radio.Contact) tea.Cmd {
	m.setTab(4)
	if !slices.ContainsFunc(m.adminNodes(), func(n radio.Contact) bool { return n.Key == ct.Key }) {
		m.adm.favOnly = false
	}
	for i, n := range m.adminNodes() {
		if n.Key == ct.Key {
			m.adm.sel = i
		}
	}
	m.adm.opened, m.adm.prompt = false, noPrompt
	cmd, _ := m.nodeListKey("enter")
	return cmd
}

func (m *Model) loginFromChats(ct radio.Contact) {
	m.setTab(4)
	for i, n := range m.adminNodes() {
		if n.Key == ct.Key {
			m.adm.sel = i
		}
	}
	m.adm.open, m.adm.opened, m.adm.pane = ct.Key, true, paneStatus
	m.openLogin()
	key := ct.Key
	m.adm.returnTo = &key
}
