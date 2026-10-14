package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"meshcore-tui/internal/admin"
	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/repeaterserial"
	"meshcore-tui/internal/sim"
	"meshcore-tui/internal/store"
	"meshcore-tui/internal/theme"
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
	h := newHarness(t)
	h.keys("5")
	scr := h.screen()
	for _, want := range []string{"ADMIN · 11 NODES · 0 SIGNED IN", "Dundry", "Clifton Room", "signed out"} {
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
	for _, want := range []string{"ADMIN › Dundry (admin) · fw v1.17.1", "[ OK ] ADMIN", "Uptime", "Battery 4.", "Noise floor", "Packets", "Errors", "polled"} {
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
	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Dundry", "password")
	h.idle(sess)
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
	h.keys("G")
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
	h.keys("u")
	h.keys("s")
	scr = h.screen()
	if !strings.Contains(scr, "REVIEW CHANGES TO DUNDRY") || !strings.Contains(scr, "Repeat packets") || strings.Contains(scr, "Flood loop") {
		t.Fatalf("diff:\n%s", scr)
	}
	h.keys("y")
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
	if !strings.Contains(h.screen(), "Y applies and reboots") {
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
	h.keys("a")
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
	h.keys("x")
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
	h.keys("x")
	h.idle(sess)
	h.keys("S")
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
	h.keys("e")
	h.typeLine("Dundry")
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
	if !strings.Contains(scr, "▸ ver") || !strings.Contains(scr, "v1.17.1 (Build") || !strings.Contains(scr, "Unknown command") {
		t.Fatalf("console:\n%s", scr)
	}
	if sess.Settings["name"].Value != "Dundry" {
		t.Error("a typed get fills in the form")
	}
	h.typeLine("region")
	h.idle(sess)
	h.key(tea.KeyUp)
	h.key(tea.KeyUp)
	if h.m.adm.console != "dance" {
		t.Errorf("history: %q", h.m.adm.console)
	}
	h.key(tea.KeyDown)
	h.key(tea.KeyDown)
	h.key(tea.KeyDown)
	if h.m.adm.console != "" {
		t.Errorf("history end: %q", h.m.adm.console)
	}
	h.keys("get flood.ad")
	h.key(tea.KeyTab)
	if h.m.adm.console != "get flood.advert.interval " {
		t.Errorf("completion: %q", h.m.adm.console)
	}
	h.key(tea.KeyEscape)
	h.keys("adv")
	h.key(tea.KeyTab)
	if h.m.adm.console != "advert" {
		t.Errorf("shared prefix: %q", h.m.adm.console)
	}
	h.key(tea.KeyEscape)
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
	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Dundry", "")
	h.idle(sess)
	if sess.Admin || !strings.Contains(h.screen(), "[ OK ] GUEST") {
		t.Fatalf("guest:\n%s", h.screen())
	}
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

	// Results for nodes nobody asked about change nothing.
	h.m.Update(eventMsg{radio.LoginResult{Prefix: [6]byte{0xEE}}})
	unread := h.m.chats.unread
	h.m.Update(eventMsg{radio.Incoming{From: [6]byte{0xEE}, CLI: true, Text: "x"}})
	if h.m.chats.unread != unread {
		t.Error("console text never lands in chats")
	}
}

func TestAdminRendersSmall(t *testing.T) {
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
	if !strings.Contains(h.screen(), "switch between status, settings") {
		t.Errorf("help:\n%s", h.screen())
	}
}

func TestConsoleHelpers(t *testing.T) {
	for cmd, risky := range map[string]bool{"reboot": true, "start ota": true, "password x": true, "log erase": true, "set prv.key ab": true,
		"set radio 1,2,3,4": true, "set tx 20": false, "ver": false, "": false, "erase": true, "poweroff": true} {
		if (dangerous(cmd) != "") != risky {
			t.Errorf("dangerous(%q)", cmd)
		}
	}
	if completeConsole("zz", radio.TypeRepeater) != "zz" || completeConsole("get nope", radio.TypeRepeater) != "get nope" {
		t.Error("no matches leave the line alone")
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
	h.m.chats.sel = 0
	h.m.refreshChats()
	h.keys("L") // not a room: L does nothing here
	if h.m.tab != 0 {
		t.Error("L only logs in to rooms")
	}
}

func TestAdminWaitingAndEmptyStates(t *testing.T) {
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
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	s := sim.New(6).Fast()
	c := &clock{t: time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)}
	rep := repeaterserial.NewRepeater("/dev/cu.test", repeaterserial.NewConsole(s.Console("Dundry")), nil)
	m := New(Options{Theme: theme.Neon, Motion: motion.Full, Clock: c.now, Store: st, ExportDir: t.TempDir(),
		Dial: func(Target) radio.Radio { return rep }, Start: &Target{Kind: "USB", Addr: "/dev/cu.test"}, Env: func(string) string { return "" }})
	t.Cleanup(func() { rep.Close() })
	h := &harness{t: t, m: m, s: s, clk: c, results: make(chan tea.Msg, 1024)}
	h.start(m.Init())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	h.until("ready", func() bool { return m.ready.Self.Name != "" })
	h.advance(time.Second)
	ct := h.contact("Dundry")
	sess := h.session(ct)
	h.until("signed in", func() bool { return sess.State == admin.LoggedIn })
	h.idle(sess)
	scr := h.screen()
	for _, want := range []string{"ADMIN › Dundry (admin) · fw v1.17.1 · local console", "Battery 4.11 V", "Noise floor -113", "Errors   rx 14"} {
		if !strings.Contains(scr, want) {
			t.Errorf("local status lacks %q:\n%s", want, scr)
		}
	}
	for h.m.adm.pane != paneNeighbours {
		h.key(tea.KeyRight)
	}
	h.idle(sess)
	if len(sess.Neighbours) != 3 {
		t.Fatalf("neighbours: %+v", sess.Neighbours)
	}
	h.keys("jd")
	h.idle(sess)
	h.keys("x")
	h.idle(sess)
	h.key(tea.KeyRight)
	h.idle(sess)
	if len(sess.ACL) != 2 {
		t.Errorf("acl from get acl: %+v", sess.ACL)
	}
	for h.m.adm.pane != paneLog {
		h.key(tea.KeyRight)
	}
	if !strings.Contains(h.screen(), "r reads the whole log") {
		t.Fatalf("log pane:\n%s", h.screen())
	}
	h.keys("r")
	h.idle(sess)
	if h.m.adm.pane != paneConsole || !strings.Contains(h.screen(), "RX len=42") {
		t.Errorf("log read into the console:\n%s", h.screen())
	}
}

func TestLogNeedsUSB(t *testing.T) {
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
	h := newHarness(t)
	kc := h.m.opt.Keychain
	h.keys("5")
	h.keys("P")
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
	h.keys("F")
	if _, ok := kc.Get(pur.Key); ok || !strings.Contains(h.m.log[len(h.m.log)-1].text, "FORGOTTEN") {
		t.Error("forget")
	}

	// Polling logs in with saved passwords, and the fleet flags what needs attention.
	kel := h.contact("Kelston")
	kc.Set(kel.Key, "password")
	h.keys("P")
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
	sess.Status = append(sess.Status, admin.StatusPoint{At: h.m.now, Stats: sess.Status[0].Stats})
	sess.Status[len(sess.Status)-1].Stats.BattMilliVolts = 3400
	scr := h.screen()
	for _, want := range []string{"2 ALERTS", "FIRMWARE", "v1.17.1", "OLD FIRMWARE", "LOW BATTERY", "●"} {
		if !strings.Contains(scr, want) {
			t.Errorf("fleet lacks %q:\n%s", want, scr)
		}
	}
	h.m.Update(fleetPollMsg{[32]byte{0xEE}}) // gone from the contacts: nothing happens
}
