package ui

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/console"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/repeaterserial"
	"github.com/verdantran/meshcore-tui/internal/sim"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

// choose moves the node list to a repeater or room server.
func (h *harness) choose(name string) radio.Contact {
	h.t.Helper()
	for i, c := range h.m.adminNodes() {
		if c.Name == name {
			h.m.adm.sel = i
			return c
		}
	}
	h.t.Fatalf("%s isn't on the admin list", name)
	return radio.Contact{}
}

func (h *harness) session(c radio.Contact) *admin.Session {
	sess, ok := h.m.admin.Find(c.Key)
	if !ok {
		h.t.Fatalf("no session for %s", c.Name)
	}
	return sess
}

func (h *harness) typeLine(s string) {
	h.keys(s)
	h.key(tea.KeyEnter)
}

func (h *harness) login(name, password string) (radio.Contact, *admin.Session) {
	h.t.Helper()
	ct := h.choose(name)
	h.key(tea.KeyEnter)
	h.typeLine(password)
	sess := h.session(ct)
	h.until("login", func() bool { return sess.State == admin.LoggedIn || sess.State == admin.LoginFailed })
	return ct, sess
}

func (h *harness) idle(sess *admin.Session) {
	h.t.Helper()
	h.until("the radio to go quiet", func() bool {
		_, running := h.m.admin.Running()
		return !running && !sess.Busy()
	})
}

func TestAdminLoginAndStatus(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("5")
	scr := h.screen()
	for _, want := range []string{"ADMIN · 12 NODES · 0 SIGNED IN", "Dundry", "Clifton Room", "signed out"} {
		if !strings.Contains(scr, want) {
			t.Fatalf("node list lacks %q:\n%s", want, scr)
		}
	}
	h.key(tea.KeyDown)
	h.key(tea.KeyUp)

	ct, sess := h.login("Dundry", "wrong")
	if sess.State != admin.LoginFailed || !strings.Contains(h.screen(), "[FAIL]") {
		t.Fatalf("wrong password: %v\n%s", sess.State, h.screen())
	}
	h.key(tea.KeyEnter) // the dashboard offers another go
	if !strings.Contains(h.screen(), "LOG IN TO DUNDRY") {
		t.Fatalf("retry prompt:\n%s", h.screen())
	}
	h.keys("passwordx")
	h.key(tea.KeyBackspace)
	if strings.Contains(h.screen(), "password") && !strings.Contains(h.screen(), "••••••••") {
		t.Fatal("the password is masked")
	}
	h.key(tea.KeyEnter)
	h.until("admin login", func() bool { return sess.State == admin.LoggedIn })
	h.idle(sess)
	scr = h.screen()
	for _, want := range []string{"ADMIN › Dundry (admin) · fw v1.18.0", "[ OK ] ADMIN", "Uptime", "Battery 4.", "Noise floor", "Packets", "Errors", "polled"} {
		if !strings.Contains(scr, want) {
			t.Errorf("status lacks %q:\n%s", want, scr)
		}
	}
	h.keys("u")
	h.until("a second poll", func() bool { return len(sess.Status) == 2 })
	h.keys("a")
	if !h.m.adm.auto[ct.Key] || !strings.Contains(h.screen(), "auto-refresh every 1m0s") {
		t.Error("auto-refresh on")
	}
	_, cmd := h.m.Update(autoStatusMsg{ct.Key})
	h.start(cmd)
	h.until("an automatic poll", func() bool { return len(sess.Status) == 3 })
	h.keys("a")
	h.m.Update(autoStatusMsg{ct.Key}) // a tick after switching off does nothing
	h.keys("Ac")
	h.idle(sess)
	if !strings.Contains(plainConsole(sess), "Advert sent") || !strings.Contains(plainConsole(sess), "clock set") {
		t.Errorf("advert and clock sync go through the console: %+v", sess.Console)
	}
	h.keys("R")
	if !strings.Contains(h.screen(), "REBOOT DUNDRY") {
		t.Fatalf("reboot asks first:\n%s", h.screen())
	}
	h.typeLine("dundy")
	if !strings.Contains(h.m.log[len(h.m.log)-1].text, "NOT CONFIRMED") {
		t.Error("a misspelt name doesn't confirm")
	}
	for range 5 {
		h.key(tea.KeyBackspace)
	}
	h.typeLine("dundry")
	h.idle(sess)
	if last := sess.Console[len(sess.Console)-1]; !strings.Contains(last.Text, "doesn't reply") {
		t.Errorf("reboot sent: %+v", last)
	}

	// The list shows the session, and esc goes back to it.
	h.key(tea.KeyEscape)
	scr = h.screen()
	if !strings.Contains(scr, "1 SIGNED IN") || !strings.Contains(scr, "4.") {
		t.Errorf("list after login:\n%s", scr)
	}
	h.keys("L")
	h.settle()
	if sess.State != admin.LoggedOut || !strings.Contains(h.screen(), "LOGGED OUT") {
		t.Errorf("logout: %v", sess.State)
	}
	h.key(tea.KeyEnter)
	h.key(tea.KeyEscape) // cancelling the login prompt goes back to the list
	if h.m.adm.opened {
		t.Error("esc at the prompt returns to the list")
	}
}

func TestAdminSettings(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Dundry", "password")
	h.idle(sess)
	h.key(tea.KeyRight)
	h.key(tea.KeyRight)
	h.idle(sess)
	if sess.Settings["name"].Value != "Dundry" || !strings.Contains(h.screen(), "IDENTITY") {
		t.Fatalf("the first group is read on arrival: %+v\n%s", sess.Settings["name"], h.screen())
	}
	if !strings.Contains(h.screen(), "Radio") || !strings.Contains(h.screen(), "not read") {
		t.Errorf("other groups wait to be read:\n%s", h.screen())
	}
	moveTo := func(key string) {
		t.Helper()
		for i, st := range h.m.settingRows(sess) {
			if st.Key == key {
				h.m.adm.setSel = i
				return
			}
		}
		t.Fatalf("no setting %s", key)
	}
	h.keys("U")
	h.idle(sess)
	if sess.Settings["tx"].Value != "22" || sess.Settings["bridge.channel"].Value != "1" {
		t.Fatalf("read all: %+v", sess.Settings)
	}

	moveTo("tx")
	h.key(tea.KeyEnter)
	h.key(tea.KeyBackspace)
	h.key(tea.KeyBackspace)
	h.typeLine("30")
	if h.m.adm.prompt != promptEdit || !strings.Contains(h.m.log[len(h.m.log)-1].text, "between 1 and 22 dBm") {
		t.Fatal("an out-of-range value is refused and the editor stays open")
	}
	h.key(tea.KeyBackspace)
	h.key(tea.KeyBackspace)
	h.typeLine("20")
	moveTo("repeat")
	h.keys(" ")
	moveTo("loop.detect")
	h.key(tea.KeyEnter)
	moveTo("role")
	h.key(tea.KeyEnter)
	if !strings.Contains(h.m.log[len(h.m.log)-1].text, "READ ONLY") {
		t.Error("read-only settings can't be edited")
	}
	moveTo("name")
	h.key(tea.KeyEnter)
	h.key(tea.KeyEscape)
	scr := h.screen()
	if !strings.Contains(scr, "3 CHANGES STAGED") || !strings.Contains(scr, "22 dBm → 20 dBm") {
		t.Fatalf("staged:\n%s", scr)
	}
	moveTo("loop.detect")
	h.keys("x")
	h.keys("s")
	scr = h.screen()
	if !strings.Contains(scr, "REVIEW CHANGES TO DUNDRY") || !strings.Contains(scr, "Repeat packets") || strings.Contains(scr, "  Flood loop") || !strings.Contains(scr, "DISCARDED") {
		t.Fatalf("diff:\n%s", scr)
	}
	h.keys("y")
	if !strings.Contains(h.screen(), "Less power shortens its reach") {
		t.Fatalf("lowering the power asks for the node's name:\n%s", h.screen())
	}
	h.typeLine("Dundry")
	h.idle(sess)
	if sess.Settings["tx"].Value != "20" || sess.Settings["repeat"].Value != "off" || len(sess.Pending) != 0 {
		t.Fatalf("applied: %+v %+v", sess.Settings["tx"], sess.Pending)
	}
	h.advance(5 * time.Second)
	if scr := h.screen(); !strings.Contains(scr, "22 → 20") {
		t.Errorf("the applied list shows each change:\n%s", scr)
	}
	h.keys("s")
	if !strings.Contains(h.m.log[len(h.m.log)-1].text, "NOTHING TO APPLY") {
		t.Error("nothing staged")
	}

	// Changing the radio is dangerous: it needs a reboot and the node's name typed.
	moveTo("radio")
	h.key(tea.KeyEnter)
	for range 20 {
		h.key(tea.KeyBackspace)
	}
	h.typeLine("869.525,250,11,5")
	h.keys("s")
	if !strings.Contains(h.screen(), "Y apply and reboot") {
		t.Fatalf("reboot offered:\n%s", h.screen())
	}
	h.keys("Y")
	if !strings.Contains(h.screen(), "strands the node off-frequency") {
		t.Fatalf("radio warning:\n%s", h.screen())
	}
	h.typeLine("Dundry")
	h.idle(sess)
	if sess.Settings["radio"].Value != "869.525,250,11,5" || !strings.Contains(sess.Console[len(sess.Console)-1].Text, "doesn't reply") {
		t.Errorf("radio applied then rebooted: %+v", sess.Console[len(sess.Console)-1])
	}
	moveTo("guest.password")
	h.key(tea.KeyEnter)
	h.typeLine("guest")
	h.keys("s")
	h.keys("n") // any other key backs out of the review
	if h.m.adm.prompt != noPrompt || len(sess.Pending) != 1 {
		t.Error("backing out keeps the edit staged")
	}
	h.keys("y") // y outside the review does nothing
	h.keys("s")
	h.keys("y")
	h.idle(sess)
	if !strings.Contains(h.screen(), "••••••") {
		t.Errorf("passwords are masked:\n%s", h.screen())
	}
}

func TestAdminPanes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("5")
	ct, sess := h.login("Dundry", "password")
	h.idle(sess)
	pane := func(p adminPane) {
		t.Helper()
		for h.m.adm.pane != p {
			h.key(tea.KeyRight)
		}
		h.idle(sess)
	}

	pane(paneNeighbours)
	if scr := h.screen(); !strings.Contains(scr, "NEIGHBOUR") || !strings.Contains(scr, "fetched") || len(sess.Neighbours) == 0 {
		t.Fatalf("neighbours:\n%s", scr)
	}
	h.keys("u")
	h.idle(sess)

	pane(paneACL)
	if scr := h.screen(); !strings.Contains(scr, "Alice") || !strings.Contains(scr, "read-only") || !strings.Contains(scr, "admin") {
		t.Fatalf("acl:\n%s", scr)
	}
	h.keys("n")
	if !strings.Contains(h.screen(), "GIVE A CONTACT ACCESS") || strings.Contains(h.screen(), " Alice ") {
		t.Fatalf("picker lists contacts not already on the list:\n%s", h.screen())
	}
	h.keys("jk")
	h.key(tea.KeyEnter)
	h.idle(sess)
	if len(sess.ACL) != 3 {
		t.Fatalf("added: %+v", sess.ACL)
	}
	for i, r := range sess.ACL {
		if r.Name == "Bob" {
			h.m.adm.aclSel = i
		}
	}
	h.keys("+")
	h.idle(sess)
	bob := func() admin.ACLRow {
		for _, r := range sess.ACL {
			if r.Name == "Bob" {
				return r
			}
		}
		return admin.ACLRow{}
	}
	if bob().Level != 2 {
		t.Fatalf("raised: %+v", sess.ACL)
	}
	h.keys("-")
	h.idle(sess)
	if bob().Level != 1 {
		t.Fatalf("lowered: %+v", sess.ACL)
	}
	h.keys("xy")
	h.idle(sess)
	if len(sess.ACL) != 2 {
		t.Fatalf("removed: %+v", sess.ACL)
	}
	sess.ACL = append(sess.ACL, admin.ACLRow{Prefix: [6]byte{0xEE}, Name: "EE0000000000", Level: 1})
	h.m.adm.aclSel = 2
	h.keys("+")
	if !strings.Contains(h.m.log[len(h.m.log)-1].text, "full key is unknown") {
		t.Error("a stranger's level can't be changed")
	}
	h.keys("jjjjk")

	pane(paneRegions)
	if scr := h.screen(); !strings.Contains(scr, "bristol") || !strings.Contains(scr, "⌂ home") || !strings.Contains(scr, "flood denied") {
		t.Fatalf("regions:\n%s", scr)
	}
	h.keys("x")
	if !strings.Contains(h.m.log[len(h.m.log)-1].text, "can't be removed") {
		t.Error("* stays")
	}
	h.keys("jjj") // wales
	h.keys("h")
	h.idle(sess)
	h.keys("f")
	h.idle(sess)
	if r := sess.Regions[3]; r.Name != "wales" || !r.Home || !r.Flood {
		t.Fatalf("home and flood: %+v", sess.Regions)
	}
	h.keys("n")
	h.typeLine("cardiff")
	h.idle(sess)
	if len(sess.Regions) != 5 || sess.Regions[4].Name != "cardiff" || sess.Regions[4].Depth != 2 {
		t.Fatalf("put: %+v", sess.Regions)
	}
	h.keys("n")
	h.typeLine("two words")
	if !strings.Contains(h.m.log[len(h.m.log)-1].text, "one word") {
		t.Error("region names are one word")
	}
	h.keys("j")
	h.keys("xy")
	h.idle(sess)
	h.keys("s")
	h.keys("u")
	h.idle(sess)
	if len(sess.Regions) != 4 {
		t.Fatalf("removed: %+v", sess.Regions)
	}

	pane(paneTelemetry)
	if scr := h.screen(); !strings.Contains(scr, "voltage (ch 1)") || !strings.Contains(scr, "°C") {
		t.Fatalf("telemetry:\n%s", scr)
	}
	h.keys("u")
	h.idle(sess)

	pane(paneLog)
	h.keys("st")
	h.idle(sess)
	h.keys("Cy")
	h.idle(sess)
	if scr := h.screen(); !strings.Contains(scr, "logging started") || !strings.Contains(scr, "log erased") {
		t.Fatalf("log:\n%s", scr)
	}

	pane(paneConsole)
	if !strings.Contains(h.screen(), "dundry▸") {
		t.Fatalf("console prompt:\n%s", h.screen())
	}
	h.typeLine("ver")
	h.idle(sess)
	h.typeLine("get name")
	h.idle(sess)
	h.typeLine("dance")
	h.idle(sess)
	scr := h.screen()
	if !strings.Contains(scr, "▸ ver") || !strings.Contains(scr, "v1.18.0 (Build") || !strings.Contains(scr, "Unknown command") {
		t.Fatalf("console:\n%s", scr)
	}
	if sess.Settings["name"].Value != "Dundry" {
		t.Error("a typed get fills in the form")
	}
	h.typeLine("region")
	h.idle(sess)
	h.key(tea.KeyUp)
	h.key(tea.KeyUp)
	if h.m.adm.console.text != "dance" {
		t.Errorf("history: %q", h.m.adm.console.text)
	}
	h.key(tea.KeyDown)
	h.key(tea.KeyDown)
	h.key(tea.KeyDown)
	if h.m.adm.console.text != "" {
		t.Errorf("history end: %q", h.m.adm.console.text)
	}
	h.keys("get flood.ad")
	h.key(tea.KeyTab)
	if h.m.adm.console.text != "get flood.advert.interval" {
		t.Errorf("completion: %q", h.m.adm.console.text)
	}
	h.key(tea.KeyEscape)
	h.keys("adv")
	h.key(tea.KeyTab)
	if h.m.adm.console.text != "advert" {
		t.Errorf("shared prefix: %q", h.m.adm.console.text)
	}
	h.key(tea.KeyEscape)
	h.typeLine("Log  Erase")
	if h.m.asking == nil || !strings.Contains(h.m.asking.what, "deletes data") {
		t.Fatalf("erasing the log asks first, however it's spaced: %+v", h.m.asking)
	}
	h.keys("n")
	h.typeLine("password hunter2")
	if !strings.Contains(h.screen(), "no recovery over the air") {
		t.Fatalf("password asks first:\n%s", h.screen())
	}
	h.key(tea.KeyEscape)
	h.key(tea.KeyEnter) // an empty line does nothing
	h.key(tea.KeyEscape)
	if h.m.adm.opened {
		t.Error("esc on an empty console goes back to the list")
	}
	_ = ct
}

func TestAdminGuestAndRoom(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Dundry", "")
	h.idle(sess)
	if sess.Admin || !strings.Contains(h.screen(), "[ OK ] GUEST") {
		t.Fatalf("guest:\n%s", h.screen())
	}
	h.key(tea.KeyRight)
	h.key(tea.KeyRight)
	if !strings.Contains(h.screen(), "Settings needs an admin login") {
		t.Fatalf("guests can't change settings:\n%s", h.screen())
	}
	for range 6 {
		h.key(tea.KeyRight)
	}
	h.typeLine("ver")
	if !strings.Contains(h.m.log[len(h.m.log)-1].text, "needs an admin login") {
		t.Error("guests have no console")
	}
	h.key(tea.KeyEscape)
	h.key(tea.KeyEscape)

	// A room server sends its posts after login; they're filed under the room with each author's name.
	room, rs := h.login("Clifton Room", "room")
	h.idle(rs)
	h.until("room posts", func() bool {
		msgs, _ := h.m.opt.Store.Messages(h.m.chat.Device(), store.ContactConvo(room.Key), 10)
		return len(msgs) >= 3
	})
	msgs, _ := h.m.opt.Store.Messages(h.m.chat.Device(), store.ContactConvo(room.Key), 10)
	authors := map[string]bool{}
	for _, msg := range msgs {
		authors[msg.Sender] = true
	}
	if !authors["Alice"] || !authors["Bob"] || !authors["Carol"] {
		t.Errorf("authors = %v", authors)
	}
	if latest, _ := rs.Latest(); latest.Room == nil || !strings.Contains(h.screen(), "posted") {
		t.Errorf("room status:\n%s", h.screen())
	}
}

func TestAdminTimeoutsAndFailures(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("5")
	ct := h.choose("Kelston")
	sess := h.m.admin.Session(ct)
	j := h.m.admin.RequestStatus(ct, h.m.now)
	h.m.Update(jobTimeoutMsg{j.ID})
	if !strings.Contains(h.m.log[len(h.m.log)-1].text, "NO REPLY  status from Kelston") {
		t.Errorf("timeout: %s", h.m.log[len(h.m.log)-1].text)
	}
	h.m.Update(jobTimeoutMsg{j.ID}) // a second timeout for the same job is ignored
	j = h.m.admin.Login(ct, "x", h.m.now)
	h.m.Update(jobSentMsg{id: j.ID, err: errors.New("radio busy")})
	if sess.State != admin.LoginFailed || !strings.Contains(h.m.log[len(h.m.log)-1].text, "LOGIN  radio busy") {
		t.Errorf("refused: %v %s", sess.State, h.m.log[len(h.m.log)-1].text)
	}
	h.m.Update(jobSentMsg{id: 999, err: errors.New("stale")})

	sess.State = admin.LoggedIn
	c := h.m.admin.Command(ct.Key, "ver", "", h.m.now)
	h.m.Update(cmdSentMsg{c, errors.New("no route")})
	c = h.m.admin.Command(ct.Key, "board", "", h.m.now)
	h.m.Update(cmdTimeoutMsg{ct.Key, c.ID})
	if n := len(sess.Console); !sess.Console[n-1].Error || !strings.Contains(sess.Console[n-3].Text, "couldn't send") {
		t.Errorf("console failures: %+v", sess.Console)
	}
	h.m.admin.Command(ct.Key, "clock", "", h.m.now)
	h.m.Update(cmdLateMsg{ct.Key, c.ID})
	if n := len(sess.Console); !strings.Contains(sess.Console[n-2].Text, "overdue") || sess.Console[n-1].Text != "ver" {
		t.Errorf("once the late window shuts, the queue goes on behind a ver: %+v", sess.Console)
	}

	// Results for nodes nobody asked about change nothing.
	h.m.Update(eventMsg{radio.LoginResult{Prefix: [6]byte{0xEE}}})
	unread := h.m.chats.unread
	h.m.Update(eventMsg{radio.Incoming{From: [6]byte{0xEE}, CLI: true, Text: "x"}})
	if h.m.chats.unread != unread {
		t.Error("console text never lands in chats")
	}
}

func TestAdminRendersSmall(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Dundry", "password")
	h.idle(sess)
	for _, size := range [][2]int{{80, 24}, {200, 60}} {
		h.m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for p := range len(paneNames) {
			h.m.adm.pane = adminPane(p)
			h.screen()
		}
		for _, pr := range []adminPrompt{promptPassword, promptDiff, promptConfirm, promptPick} {
			h.m.adm.prompt = pr
			h.screen()
		}
		h.m.adm.prompt = noPrompt
	}
	h.m.adm.pane = paneStatus
	sess.State = admin.LoggingIn
	if !strings.Contains(h.screen(), "AUTH") {
		t.Errorf("waiting shows AUTH:\n%s", h.screen())
	}
	sess.State = admin.LoggedIn
	h.keys("?")
	if !strings.Contains(h.screen(), "switch between status, health, settings") {
		t.Errorf("help:\n%s", h.screen())
	}
}

func TestConsoleHelpers(t *testing.T) {
	t.Parallel()
	for cmd, risky := range map[string]bool{"reboot": true, "start ota": true, "password x": true, "log erase": false, "set prv.key ab": true,
		"set radio 1,2,3,4": true, "set tx 20": true, "ver": false, "": false, "erase": true, "poweroff": true,
		// The firmware matches on prefixes, so near misses are caught too.
		"rebootnow": true, "REBOOT": true, "  reboot ": true, "eraseall": true, "start  ota": true, "start ota now": true,
		"set freq 433": true, "set bw 125": true, "set sf 12": true, "set cr 5": true, "Set Radio 1,2,3,4": true,
		"set guest.password x": true, "set password x": true, "set nonsense 1": true, "set repeat off": true, "set radio.rxgain off": true, "set radio.fem.txgain off": true,
		"set path.hash.mode 2": true, "set flood.max 0": true, "set name Dundry": false, "set dutycycle 50": false,
		"tempradio 869.5,62.5,8,8,5": false, "setperm ab 1": false, "get freq": false} {
		if (dangerous(cmd) != "") != risky {
			t.Errorf("dangerous(%q)", cmd)
		}
	}
	for _, in := range []string{"zz", "get nope"} {
		var c completer
		e := newLine(in)
		if c.tab(console.Node(radio.TypeRepeater), &e, false); e.text != in {
			t.Errorf("no matches leave %q alone, not %q", in, e.text)
		}
	}
	if purposeOf("get tx") != "get tx" || purposeOf("get nope") != "" || purposeOf("set tx 1") != "" || purposeOf("region") != "region" {
		t.Error("purposes")
	}
	if uptime(59) != "0m" || uptime(3700) != "1h 1m" || uptime(90000) != "1d 1h" {
		t.Error("uptime")
	}
	if commas(-1234567) != "-1,234,567" || commas(12) != "12" {
		t.Error("commas")
	}
	if sparkRune(5, 5, 5) != '▄' {
		t.Error("flat spark")
	}
}

func plainConsole(sess *admin.Session) string {
	var b strings.Builder
	for _, l := range sess.Console {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

func TestRoomLoginFromChats(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	room := h.contact("Clifton Room")
	h.m.jumpToConvo(store.ContactConvo(room.Key))
	if !strings.Contains(h.screen(), "Not logged in") {
		t.Fatalf("room banner:\n%s", h.screen())
	}
	h.keys("L")
	if h.m.tab != 4 || !strings.Contains(h.screen(), "LOG IN TO CLIFTON ROOM") {
		t.Fatalf("L opens the login:\n%s", h.screen())
	}
	h.typeLine("room")
	sess := h.session(room)
	h.until("room login", func() bool { return sess.State == admin.LoggedIn })
	if h.m.tab != 0 || !strings.Contains(h.screen(), "[ OK ] ADMIN") {
		t.Fatalf("back in Chats after login:\n%s", h.screen())
	}
	h.until("posts", func() bool { return strings.Contains(h.screen(), "Cabot Tower for a range test") })
	if !strings.Contains(h.screen(), "Carol") {
		t.Errorf("posts show their author:\n%s", h.screen())
	}
	h.keys("i")
	h.keys("see you there")
	h.key(tea.KeyEnter)
	h.until("post sent", func() bool { return strings.Contains(h.screen(), "see you there") })
	h.key(tea.KeyEscape)
	h.keys("L")
	h.settle()
	if sess.State != admin.LoggedOut || h.m.tab != 0 {
		t.Errorf("L again logs out, staying in Chats: %v", sess.State)
	}
	h.m.chats.sel = 0
	h.m.refreshChats()
	h.keys("L") // not a room: L does nothing here
	if h.m.tab != 0 {
		t.Error("L only logs in to rooms")
	}
}

func TestAdminWaitingAndEmptyStates(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("5")
	ct, sess := h.login("Dundry", "password")
	h.idle(sess)
	press := func(s string) { h.m.Update(tea.KeyPressMsg{Code: rune(s[0]), Text: s}) } // the request is queued but never sent
	cases := []struct {
		pane         adminPane
		reset        func()
		empty, ready string
	}{
		{paneStatus, func() { sess.Status = nil }, "No status yet", "asking Dundry for its status"},
		{paneNeighbours, func() { sess.NeighbourAt = time.Time{} }, "Press u to ask which repeaters", "asking for its neighbours"},
		{paneACL, func() { sess.ACLAt = time.Time{} }, "Press u to read who has access", "reading the access list"},
		{paneRegions, func() { sess.RegionsAt = time.Time{} }, "Press u to read the region tree", "reading regions"},
		{paneTelemetry, func() { sess.TelemetryAt = time.Time{} }, "Press u to ask for its sensor readings", "asking for telemetry"},
	}
	for _, c := range cases {
		h.m.adm.pane = c.pane
		c.reset()
		if !strings.Contains(h.screen(), c.empty) {
			t.Errorf("%s empty:\n%s", paneNames[c.pane], h.screen())
		}
		press("u")
		if !strings.Contains(h.screen(), c.ready) {
			t.Errorf("%s waiting:\n%s", paneNames[c.pane], h.screen())
		}
		if j, ok := h.m.admin.Running(); ok {
			h.m.admin.TimedOut(j.ID, h.m.now)
		}
		h.m.admin.CommandTimedOut(ct.Key, 0, h.m.now)
		for sess.Busy() {
			_, _, _ = h.m.admin.OnReply(ct.Prefix(), "x", h.m.now)
		}
	}

	// Empty results say so.
	h.m.adm.pane = paneNeighbours
	sess.Neighbours, sess.NeighbourAt = nil, h.m.now
	h.m.adm.pane = paneTelemetry
	sess.Telemetry, sess.TelemetryAt = nil, h.m.now
	if !strings.Contains(h.screen(), "It sent no readings") {
		t.Errorf("no telemetry:\n%s", h.screen())
	}
	h.m.adm.pane = paneNeighbours
	if !strings.Contains(h.screen(), "hasn't heard any other repeaters") {
		t.Errorf("no neighbours:\n%s", h.screen())
	}

	// Every prompt renders, and logging out of a node that isn't signed in does nothing.
	h.m.adm.pane = paneSettings
	for _, p := range []adminPrompt{promptEdit, promptRegion, promptPick} {
		sess.Regions = []admin.Region{{Name: "*", Flood: true}}
		h.m.adm.prompt = p
		h.screen()
	}
	h.m.adm.prompt = noPrompt
	if h.m.logout(h.contact("Kelston")) != nil {
		t.Error("nothing to log out of")
	}
	h.m.adm.pane = paneLog
	h.keys("z")
	if spark := h.m.spark(nil, 4, h.m.now); spark != "" {
		t.Error("an empty spark is blank")
	}
}

func TestLocalConsole(t *testing.T) {
	t.Parallel()
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	s := sim.New(6).Fast()
	c := &clock{t: time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)}
	rep := repeaterserial.NewRepeater("/dev/cu.test", repeaterserial.NewConsole(s.Console("Dundry")), nil)
	m := New(Options{Theme: theme.Neon, Motion: motion.Full, Clock: c.now, Store: st, ExportDir: t.TempDir(),
		Dial: func(Target) radio.Radio { return rep }, Start: &Target{Kind: "USB", Addr: "/dev/cu.test"}, Env: func(string) string { return "" }})
	t.Cleanup(func() { rep.Close() })
	// A real console waits for the port to go quiet, so this test needs longer when the machine is busy.
	h := &harness{t: t, m: m, s: s, clk: c, results: make(chan tea.Msg, 1024), wait: 10 * time.Second}
	h.start(m.Init())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	h.until("ready", func() bool { return m.ready.Self.Name != "" })
	h.toMain()
	ct := h.contact("Dundry")
	sess := h.session(ct)
	h.until("signed in", func() bool { return sess.State == admin.LoggedIn })
	h.idle(sess)
	scr := h.screen()
	for _, want := range []string{"STATUS › Dundry (admin) · fw v1.18.0 · local console", "1 STATUS ╱ 2 HEALTH", "9 CONSOLE", "Battery 4.11 V",
		"Noise floor -113", "Errors   rx 14", "1-9 screens", "polled 1s ago, every 1m"} {
		if !strings.Contains(scr, want) {
			t.Errorf("local status lacks %q:\n%s", want, scr)
		}
	}
	if strings.Contains(scr, "contacts ·") || strings.Contains(scr, "CHATS") {
		t.Errorf("no companion screens on a repeater:\n%s", scr)
	}
	h.keys("4")
	h.idle(sess)
	if h.m.adm.pane != paneNeighbours || len(sess.Neighbours) != 3 {
		t.Fatalf("neighbours: %+v", sess.Neighbours)
	}
	h.keys("jd")
	h.idle(sess)
	h.keys("xy")
	h.idle(sess)
	h.key(tea.KeyTab)
	h.idle(sess)
	if h.m.adm.pane != paneACL || len(sess.ACL) != 2 {
		t.Errorf("acl from get acl: %+v", sess.ACL)
	}
	h.keys("8")
	if h.m.adm.pane != paneLog || !strings.Contains(h.screen(), "r reads the log and sums it up") {
		t.Fatalf("log pane:\n%s", h.screen())
	}
	h.keys("r")
	h.idle(sess)
	scr = h.screen()
	for _, want := range []string{"LOG", "entries", "RECEIVED", "BY TYPE", "GRP_TXT", "SIGNAL", "SENDERS"} {
		if !strings.Contains(scr, want) {
			t.Errorf("log summary lacks %q:\n%s", want, scr)
		}
	}
	if !strings.Contains(plainConsole(sess), "log entries; the Log screen sums them up") {
		t.Error("the console notes the read instead of printing the whole log")
	}

	// The node on USB is watched: each poll is kept, and two make a rate.
	h.keys("2")
	if scr := h.screen(); !strings.Contains(scr, "HEALTH › Dundry") || !strings.Contains(scr, "◉ WATCHED") || !strings.Contains(scr, "BATTERY") {
		t.Fatalf("health after one poll:\n%s", scr)
	}
	h.clk.t = h.clk.t.Add(2 * time.Minute)
	_, cmd := h.m.Update(watchTickMsg{h.m.adm.watchGen})
	h.start(cmd)
	h.until("a second poll", func() bool { return len(h.m.historyOf(ct.Key)) == 2 })
	scr = h.screen()
	if !strings.Contains(scr, "RECEIVED") || !strings.Contains(scr, "TRANSMITTING") || strings.Contains(scr, "DUPLICATES") {
		t.Errorf("rates after two polls, but no duplicate count over USB:\n%s", scr)
	}
	if rows, _ := h.m.opt.Store.Status(ct.Key, time.Time{}); len(rows) != 2 {
		t.Errorf("stored polls = %d", len(rows))
	}
	h.keys("r")
	if !h.m.adm.week || !strings.Contains(h.screen(), "-7d") {
		t.Error("a week")
	}

	h.m.Update(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if !strings.Contains(h.lastLog(), "split panes are for companion mode") || h.m.paneKey {
		t.Error("no split panes on a repeater")
	}
	h.keys("9")
	h.key(tea.KeyEscape)
	if h.m.adm.pane != paneStatus || !h.m.adm.opened {
		t.Error("esc on an empty console line goes to Status")
	}
	h.key(tea.KeyLeft)
	if h.m.adm.pane != paneConsole {
		t.Error("left from the first screen wraps to the last")
	}

	// A repeater's console has no companion settings or counters of its own.
	if box := plain(h.m.screenBox(5, 100, 12, true)); !strings.Contains(box, "has no companion settings") || h.m.loadDevice() != nil || h.m.runOp("x", "x", nil, false) != nil {
		t.Errorf("device on a console:\n%s", box)
	}
	if box := plain(h.m.screenBox(6, 100, 12, true)); !strings.Contains(box, "Status pane") || h.m.pollStats() != nil {
		t.Errorf("stats on a console:\n%s", box)
	}
	h.m.radio = nil
	if h.m.dev() != nil {
		t.Error("no radio, no device")
	}
}

func TestLogNeedsUSB(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Dundry", "password")
	h.idle(sess)
	for h.m.adm.pane != paneLog {
		h.key(tea.KeyRight)
	}
	h.keys("r")
	if !strings.Contains(h.m.log[len(h.m.log)-1].text, "needs a USB cable") {
		t.Error("reading the log over the air")
	}
	h.m.adm.pane = paneNeighbours
	h.keys("u")
	h.idle(sess)
	h.keys("jkd")
	h.idle(sess)
	if !strings.Contains(plainConsole(sess), "discover.neighbors") {
		t.Error("discover over the air")
	}
}

func TestKeychainAndFleet(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	kc := h.m.opt.Keychain
	h.keys("5")
	h.keys("U")
	if !strings.Contains(h.m.log[len(h.m.log)-1].text, "no nodes to poll") {
		t.Error("nothing to poll yet")
	}

	ct := h.choose("Dundry")
	h.key(tea.KeyEnter)
	h.m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !strings.Contains(h.screen(), "[x] remember it in the system keychain") {
		t.Fatalf("remember box:\n%s", h.screen())
	}
	h.typeLine("password")
	sess := h.session(ct)
	h.until("login", func() bool { return sess.State == admin.LoggedIn })
	if pw, ok := kc.Get(ct.Key); !ok || pw != "password" {
		t.Fatalf("saved = %q %v", pw, ok)
	}
	h.idle(sess)
	h.key(tea.KeyEscape)
	h.keys("L")
	h.settle()
	h.key(tea.KeyEnter) // a saved password logs straight in
	if h.m.adm.prompt != noPrompt {
		t.Fatal("no prompt with a saved password")
	}
	h.until("saved login", func() bool { return sess.State == admin.LoggedIn })
	h.idle(sess)
	h.key(tea.KeyEscape)

	// A wrong saved password fails once, then asks.
	pur := h.choose("Purdown")
	kc.Set(pur.Key, "nope")
	h.key(tea.KeyEnter)
	ps := h.session(pur)
	h.until("failed", func() bool { return ps.State == admin.LoginFailed })
	h.key(tea.KeyEscape)
	h.key(tea.KeyEnter)
	if h.m.adm.prompt != promptPassword {
		t.Fatal("after a failure it asks")
	}
	h.key(tea.KeyEscape)
	h.keys("xy")
	if _, ok := kc.Get(pur.Key); ok || !strings.Contains(h.m.log[len(h.m.log)-1].text, "FORGOTTEN") {
		t.Error("forget")
	}

	// Polling logs in with saved passwords, and the fleet flags what needs attention.
	kel := h.contact("Kelston")
	kc.Set(kel.Key, "password")
	h.keys("U")
	if !strings.Contains(h.m.log[len(h.m.log)-1].text, "POLLING  2 nodes, one every 20s") {
		t.Errorf("poll: %s", h.m.log[len(h.m.log)-1].text)
	}
	for _, k := range []radio.Contact{kel, ct} {
		_, cmd := h.m.Update(fleetPollMsg{k.Key})
		h.start(cmd)
	}
	ks := h.session(kel)
	h.until("kelston polled", func() bool { return ks.State == admin.LoggedIn && len(ks.Status) > 0 && ks.Owner != nil })
	h.until("dundry polled", func() bool { return len(sess.Status) > 1 })
	ks.Owner.FirmwareVersion = "v1.15.0"
	ps.Owner = &meshcore.OwnerInfo{FirmwareVersion: "v1.18.0"} // the bar is set by two nodes, not one
	sess.Status = append(sess.Status, admin.StatusPoint{At: h.m.now, Stats: sess.Status[0].Stats})
	sess.Status[len(sess.Status)-1].Stats.BattMilliVolts = 3400
	scr := h.screen()
	for _, want := range []string{"2 ALERTS", "FIRMWARE", "v1.18.0", "OLD FIRMWARE", "LOW BATTERY", "●"} {
		if !strings.Contains(scr, want) {
			t.Errorf("fleet lacks %q:\n%s", want, scr)
		}
	}
	h.m.Update(fleetPollMsg{[32]byte{0xEE}}) // gone from the contacts: nothing happens
}

// slowKeychain counts lookups; on macOS each one starts a process, so drawing must never ask.
type slowKeychain struct {
	admin.MemoryKeychain
	gets atomic.Int32
}

func (k *slowKeychain) Get(node [32]byte) (string, bool) {
	k.gets.Add(1)
	return k.MemoryKeychain.Get(node)
}

func TestDrawingNeverAsksTheKeychain(t *testing.T) {
	t.Parallel()
	kc := &slowKeychain{}
	h := newHarness(t)
	h.m.keys = admin.NewCached(kc)
	h.m.opt.Keychain = h.m.keys
	kc.Set(h.contact("Dundry").Key, "password")
	h.keys("5")
	h.until("the background check", func() bool { return kc.gets.Load() == int32(len(h.m.adminNodes())) })
	if !strings.Contains(h.screen(), "●") {
		t.Fatalf("saved password shown:\n%s", h.screen())
	}
	before := kc.gets.Load()
	for range 50 {
		h.m.View()
	}
	if got := kc.gets.Load(); got != before {
		t.Errorf("drawing asked the keychain %d times", got-before)
	}
}

func TestFavouriteFilters(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("5")
	h.keys("F")
	if !strings.Contains(h.screen(), "No favourite repeaters or room servers") || !strings.Contains(h.screen(), "0 FAVOURITES") {
		t.Fatalf("no favourites yet:\n%s", h.screen())
	}
	h.keys("F")
	h.choose("Kelston")
	h.keys("f")
	h.until("favourite saved", func() bool { return favourite(h.contact("Kelston")) })
	if h.m.adminNodes()[0].Name != "Kelston" || !strings.Contains(h.screen(), "★ Kelston") {
		t.Errorf("favourites come first:\n%s", h.screen())
	}
	h.keys("F")
	if nodes := h.m.adminNodes(); len(nodes) != 1 || !strings.Contains(h.screen(), "1 FAVOURITES") {
		t.Errorf("favourites only: %v\n%s", nodes, h.screen())
	}
	h.m.opt.Keychain.Set(h.contact("Kelston").Key, "password")
	h.m.opt.Keychain.Set(h.contact("Dundry").Key, "password")
	h.keys("U")
	if !strings.Contains(plainLog(h), "POLLING  1 nodes") {
		t.Errorf("U polls only the favourites:\n%s", plainLog(h))
	}
	h.choose("Kelston")
	h.keys("f")
	h.until("unfavourited", func() bool { return !favourite(h.contact("Kelston")) })
	if !strings.Contains(plainLog(h), "UNFAVOURITED  Kelston") {
		t.Error("log")
	}

	// Contacts: F cycles through favourites too.
	h.keys("2")
	h.m.contacts.typeFilter = 1
	if rows := h.m.contactRows(); len(rows) != 0 || !strings.Contains(h.screen(), "type favourites") {
		t.Errorf("no favourite contacts: %v", rows)
	}
	h.m.contacts.typeFilter = 0
	h.pick("Alice")
	h.keys("f")
	h.until("alice", func() bool { return favourite(h.contact("Alice")) })
	h.m.contacts.typeFilter = 1
	if rows := h.m.contactRows(); len(rows) != 1 || rows[0].Name != "Alice" {
		t.Errorf("favourite contacts = %v", rows)
	}
}

// The hint stays on screen when there are more nodes than rows, and the selection stays in view.
func TestAdminHintWithAFullList(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cs := h.m.chat.Contacts()
	for i := range 12 {
		cs = append(cs, radio.Contact{Key: [32]byte{0xF0, byte(i)}, Name: fmt.Sprint("Relay ", i), Type: radio.TypeRepeater, PathLen: -1})
	}
	h.m.chat.ReplaceContacts(cs)
	h.m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.keys("5")
	for range 30 {
		h.keys("j")
		if got := h.screen(); !strings.Contains(got, "enter open · L log in") || !strings.Contains(got, "▌") {
			t.Fatalf("hint or selection gone:\n%s", got)
		}
	}
}

// A node's name is confirmed as it would be typed: without its emoji, in any case, with or without accents.
func TestConfirmTypesNameWithoutEmoji(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{"Aust 🌉": "Aust", "⛰️ Mendip Mast": "Mendip Mast", "🍺": "🍺", "Zoë ★ 2": "Zoë 2"} {
		if got := typeable(name); got != want {
			t.Errorf("typeable(%q) = %q, want %q", name, got, want)
		}
	}
	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Aust 🌉", "password")
	h.idle(sess)
	h.keys("R")
	if !strings.Contains(h.screen(), "Type Aust to go ahead") {
		t.Fatalf("confirm prompt:\n%s", h.screen())
	}
	h.typeLine("aust")
	h.idle(sess)
	if strings.Contains(h.m.log[len(h.m.log)-1].text, "NOT CONFIRMED") {
		t.Error("typing the name without its emoji should confirm")
	}
}

func TestConsoleHistoryStaysWithItsNode(t *testing.T) {
	h := newHarness(t)
	a, b := radio.Contact{Key: [32]byte{1}, Type: radio.TypeRepeater}, radio.Contact{Key: [32]byte{2}, Type: radio.TypeRepeater}
	sess := &admin.Session{Admin: true}
	h.m.adm.history = map[[32]byte][]string{a.Key: {"set name Hilltop"}}
	h.m.consoleKey("up", b, sess)
	if h.m.adm.console.text != "" {
		t.Errorf("another node's command came up: %q", h.m.adm.console.text)
	}
	h.m.consoleKey("up", a, sess)
	if h.m.adm.console.text != "set name Hilltop" {
		t.Errorf("the node's own history: %q", h.m.adm.console.text)
	}
}

func TestConsoleShowsCompletions(t *testing.T) {
	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Dundry", "password")
	h.idle(sess)
	h.start(h.m.setPane(paneConsole))
	h.keys("get flood.ma")
	if scr := h.screen(); !strings.Contains(scr, "get flood.max · get flood.max.unscoped") || !strings.Contains(scr, "get flood.max") {
		t.Fatalf("the matches show under the prompt:\n%s", scr)
	}
	h.key(tea.KeyTab)
	h.key(tea.KeyTab)
	if h.m.adm.console.text != "get flood.max.unscoped" {
		t.Errorf("tab steps to the next match: %q", h.m.adm.console.text)
	}
	h.key(tea.KeyEscape)
	h.keys("set repeat ")
	if scr := h.screen(); !strings.Contains(scr, "on|off") || !strings.Contains(scr, "Repeat packets") {
		t.Errorf("a setting's values and help show:\n%s", scr)
	}
}
