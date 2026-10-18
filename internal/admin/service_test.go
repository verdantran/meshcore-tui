package admin

import (
	"errors"
	"strings"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

var (
	t0     = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	dundry = radio.Contact{Key: [32]byte{0xA3, 0x01}, Name: "Dundry", Type: radio.TypeRepeater}
	room   = radio.Contact{Key: [32]byte{0x6C, 0x01}, Name: "Clifton Room", Type: radio.TypeRoom}
)

func prefix(c radio.Contact) [6]byte { return c.Prefix() }

func newService() *Service {
	s := New(func(p []byte) string {
		if p[0] == 0xA3 {
			return "Dundry"
		}
		return radio.KeyHex(p)
	})
	s.random = func() [4]byte { return [4]byte{1, 2, 3, 4} }
	return s
}

func TestJobsRunOneAtATime(t *testing.T) {
	s := newService()
	login := s.Login(dundry, "password", t0)
	if login == nil || login.Kind != JobLogin || login.Password != "password" {
		t.Fatalf("the first job should start straight away: %+v", login)
	}
	if s.RequestStatus(dundry, t0) != nil || s.RequestTelemetry(dundry, t0) != nil || s.Queued() != 2 {
		t.Fatal("later jobs wait while one is in flight")
	}
	if sess, _ := s.Find(dundry.Key); sess.State != LoggingIn {
		t.Errorf("state = %v", sess.State)
	}
	sess, next := s.OnLogin(radio.LoginResult{Prefix: prefix(dundry), OK: true, Admin: true, Permissions: 3}, t0)
	if sess.State != LoggedIn || !sess.Admin || sess.Level != 3 || next == nil || next.Kind != JobStatus {
		t.Fatalf("login done → %+v, next %+v", sess, next)
	}
	stats := meshcore.BuildRepeaterStats(meshcore.RepeaterStats{CommonStats: meshcore.CommonStats{BattMilliVolts: 4110, UpTimeSecs: 3600}, RecvErrors: 2})
	_, next = s.OnStatus(radio.StatusReport{Prefix: prefix(dundry), Data: stats}, t0)
	if latest, ok := sess.Latest(); !ok || latest.Stats.BattMilliVolts != 4110 || latest.Stats.RecvErrors != 2 || next.Kind != JobTelemetry {
		t.Fatalf("status → %+v, next %+v", latest, next)
	}
	lpp := meshcore.NewLPPEncoder()
	lpp.AddVoltage(1, 4.11)
	lpp.AddTemperature(2, 15.5)
	_, next = s.OnTelemetry(radio.Telemetry{Prefix: prefix(dundry), LPP: lpp.Bytes()}, t0)
	if next != nil || len(sess.Telemetry) != 2 || sess.Telemetry[0].Value != "4.11 V" || sess.Telemetry[1].Value != "15.5 °C" {
		t.Fatalf("telemetry → %+v", sess.Telemetry)
	}
	s.RequestTelemetry(dundry, t0)
	s.OnTelemetry(radio.Telemetry{Prefix: prefix(dundry), LPP: lpp.Bytes()}, t0)
	if h := sess.TelemetryLog["voltage (ch 1)"]; len(h) != 2 || h[1] != 4.11 {
		t.Errorf("telemetry history = %v", sess.TelemetryLog)
	}
	if _, running := s.Running(); running {
		t.Error("the queue should be empty")
	}
}

func TestBinaryRepliesMatchByTag(t *testing.T) {
	s := newService()
	j := s.RequestNeighbours(dundry, t0)
	if j.Data[0] != meshcore.ReqTypeGetNeighbours {
		t.Fatalf("body = % X", j.Data)
	}
	s.Sent(j.ID, 77, t0)
	if sess, _ := s.OnBinary(radio.BinaryReply{Tag: 78}, t0); sess != nil {
		t.Fatal("a reply with another tag isn't ours")
	}
	reply := meshcore.BuildNeighboursReply(meshcore.NeighboursReply{Total: 5, Neighbours: []meshcore.Neighbour{
		{Prefix: []byte{0xA3, 0, 0, 1}, HeardSecsAgo: 90, SNR: 26},
		{Prefix: []byte{0x77, 1, 2, 3}, HeardSecsAgo: 600, SNR: -8},
	}})
	sess, _ := s.OnBinary(radio.BinaryReply{Tag: 77, Data: reply}, t0)
	if sess == nil || len(sess.Neighbours) != 2 || sess.NeighbourAll != 5 || sess.Neighbours[0].Name != "Dundry" ||
		sess.Neighbours[0].SNR != 6.5 || sess.Neighbours[1].Heard != 10*time.Minute {
		t.Fatalf("neighbours = %+v", sess)
	}

	j = s.RequestACL(dundry, t0)
	s.Sent(j.ID, 5, t0)
	s.OnBinary(radio.BinaryReply{Tag: 5, Data: meshcore.BuildAccessListReply([]meshcore.ACLEntry{{Prefix: [6]byte{0xA3}, Permissions: 3}, {Prefix: [6]byte{0x3A}, Permissions: 1}})}, t0)
	if len(sess.ACL) != 2 || sess.ACL[0].Level != 3 || LevelName(sess.ACL[1].Level) != "read-only" || LevelName(9) != "level 9" {
		t.Fatalf("acl = %+v", sess.ACL)
	}
	j = s.RequestOwner(dundry, t0)
	s.Sent(j.ID, 6, t0)
	s.OnBinary(radio.BinaryReply{Tag: 6, Data: meshcore.BuildOwnerInfoReply(meshcore.OwnerInfo{FirmwareVersion: "v1.17.1", Name: "Dundry", Owner: "Bristol Mesh"})}, t0)
	if sess.Owner == nil || sess.Owner.Owner != "Bristol Mesh" {
		t.Fatalf("owner = %+v", sess.Owner)
	}
}

func TestTimeoutsAndFailuresFreeTheQueue(t *testing.T) {
	s := newService()
	login := s.Login(dundry, "x", t0)
	s.RequestStatus(room, t0)
	if _, waiting := s.TimedOut(999, t0); waiting {
		t.Fatal("an unknown job can't time out")
	}
	next, waiting := s.TimedOut(login.ID, t0)
	if !waiting || next == nil || next.Kind != JobStatus {
		t.Fatalf("after the login timed out: %+v", next)
	}
	if sess, _ := s.Find(dundry.Key); sess.State != LoginFailed {
		t.Errorf("a login with no answer failed: %v", sess.State)
	}
	if s.Failed(next.ID, errors.New("table full"), t0) != nil {
		t.Error("nothing else queued")
	}
	j := s.Login(room, "room", t0)
	s.Failed(j.ID, errors.New("not found"), t0)
	if sess, _ := s.Find(room.Key); sess.State != LoginFailed {
		t.Error("a refused login failed")
	}
	// Results nobody asked for are ignored.
	if sess, _ := s.OnLogin(radio.LoginResult{Prefix: [6]byte{0xEE}}, t0); sess != nil {
		t.Error("login for an unknown node")
	}
	s.Login(dundry, "wrong", t0)
	if sess, _ := s.OnLogin(radio.LoginResult{Prefix: prefix(dundry)}, t0); sess.State != LoginFailed {
		t.Error("wrong password")
	}
	if sess, _ := s.OnStatus(radio.StatusReport{Prefix: [6]byte{0xEE}}, t0); sess != nil {
		t.Error("status for an unknown node")
	}
	if sess, _ := s.OnTelemetry(radio.Telemetry{Prefix: [6]byte{0xEE}}, t0); sess != nil {
		t.Error("telemetry for an unknown node")
	}
	s.Logout(dundry.Key)
	if sess, _ := s.Find(dundry.Key); sess.State != LoggedOut {
		t.Error("logout")
	}
}

func TestRoomStatus(t *testing.T) {
	s := newService()
	s.RequestStatus(room, t0)
	data := meshcore.BuildRoomStats(meshcore.RoomStats{CommonStats: meshcore.CommonStats{UpTimeSecs: 60}, Posted: 42})
	sess, _ := s.OnStatus(radio.StatusReport{Prefix: prefix(room), Data: data}, t0)
	if latest, _ := sess.Latest(); latest.Room == nil || latest.Room.Posted != 42 {
		t.Fatalf("room stats = %+v", latest)
	}
	for i := 0; i < historyKeep+10; i++ {
		s.OnStatus(radio.StatusReport{Prefix: prefix(room), Data: data}, t0)
	}
	if len(sess.Status) != historyKeep {
		t.Errorf("history kept %d", len(sess.Status))
	}
}

func TestConsoleRepliesInOrder(t *testing.T) {
	s := newService()
	s.Session(dundry)
	if s.Command([32]byte{0xEE}, "ver", "", t0) != nil {
		t.Fatal("commands need a session")
	}
	first := s.Command(dundry.Key, "get tx", "get tx", t0)
	if first == nil || s.Command(dundry.Key, "get name", "get name", t0) != nil {
		t.Fatal("one command in flight per node")
	}
	sess, _ := s.Find(dundry.Key)
	if !sess.Settings["tx"].Loading || !sess.Busy() {
		t.Error("a pending get shows as loading")
	}
	if s.CommandSent(first, nil, t0) != nil {
		t.Error("a normal command waits for its reply")
	}
	_, done, next := s.OnReply(prefix(dundry), "> 22", t0)
	if done != first {
		t.Error("the reply settles the command in flight")
	}
	if sess.Settings["tx"].Value != "22" || next == nil || next.Text != "get name" {
		t.Fatalf("after the first reply: %+v, next %+v", sess.Settings["tx"], next)
	}
	_, _, next = s.OnReply(prefix(dundry), "Unknown config: name", t0)
	if sess.Settings["name"].Err != "Unknown config: name" || next != nil {
		t.Fatalf("an error reply lands on the setting: %+v", sess.Settings["name"])
	}
	s.OnReply(prefix(dundry), "a stray line", t0)
	if last := sess.Console[len(sess.Console)-1]; last.Text != "a stray line" {
		t.Error("unsolicited replies are kept")
	}
	if sess, _, _ := s.OnReply([6]byte{0xEE}, "x", t0); sess != nil {
		t.Error("reply from an unknown node")
	}

	// Silent commands finish once sent; failed sends and timeouts move the queue on.
	reboot := s.Command(dundry.Key, "reboot", "", t0)
	s.Command(dundry.Key, "ver", "", t0)
	next = s.CommandSent(reboot, nil, t0)
	if next == nil || next.Text != "ver" || !strings.Contains(sess.Console[len(sess.Console)-1].Text, "doesn't reply") {
		t.Fatalf("after reboot: %+v", next)
	}
	s.Command(dundry.Key, "board", "", t0)
	if s.CommandTimedOut(dundry.Key, 9999, t0) != nil {
		t.Error("the wrong command can't time out")
	}
	next = s.CommandTimedOut(dundry.Key, next.ID, t0)
	if next == nil || next.Text != "board" || !sess.Console[len(sess.Console)-1].Error {
		t.Fatalf("after a timeout: %+v", next)
	}
	if s.CommandSent(next, errors.New("radio busy"), t0) != nil || !strings.Contains(sess.Console[len(sess.Console)-1].Text, "couldn't send") {
		t.Error("a failed send is logged")
	}
	if s.CommandSent(&Command{Key: dundry.Key}, nil, t0) != nil || s.CommandTimedOut([32]byte{0xEE}, 1, t0) != nil {
		t.Error("stale callbacks are ignored")
	}

	s.OnReply(prefix(dundry), "v1.9", t0)
	if last := sess.Console[len(sess.Console)-1]; !strings.Contains(last.Text, "late reply") {
		t.Errorf("a reply soon after a timeout is taken as the late one: %+v", last)
	}
	s.Command(dundry.Key, "region", "region", t0)
	s.OnReply(prefix(dundry), "* F\n bristol^ F\n", t0)
	if len(sess.Regions) != 2 || !sess.Regions[1].Home {
		t.Errorf("regions = %+v", sess.Regions)
	}

	// A timed-out get marks the setting.
	g := s.Command(dundry.Key, "get af", "get af", t0)
	s.CommandTimedOut(dundry.Key, g.ID, t0)
	if !strings.Contains(sess.Settings["af"].Err, "no reply") {
		t.Errorf("af = %+v", sess.Settings["af"])
	}
}

func TestSettingsReadStageApply(t *testing.T) {
	s := newService()
	s.Session(dundry)
	if first := s.ReadSettings(dundry.Key, "Flood", t0); first == nil || first.Text != "get flood.max" {
		t.Fatalf("read group starts with %+v", first)
	}
	sess, _ := s.Find(dundry.Key)
	for range 5 {
		s.OnReply(prefix(dundry), "> 8", t0)
	}
	if sess.Settings["advert.interval"].Value != "8" || sess.Busy() {
		t.Fatalf("all five flood settings read: %+v busy=%v", sess.Settings, sess.Busy())
	}
	if s.ReadSettings([32]byte{0xEE}, "", t0) != nil || s.Stage([32]byte{0xEE}, "tx", "1") == nil {
		t.Error("no session, no reads or edits")
	}

	sess.Settings["tx"] = Value{Value: "22"}
	if err := s.Stage(dundry.Key, "tx", "40"); err == nil {
		t.Fatal("out-of-range edits are refused before sending")
	}
	if err := s.Stage(dundry.Key, "nope", "1"); err == nil {
		t.Fatal("unknown setting")
	}
	s.Stage(dundry.Key, "tx", "20")
	s.Stage(dundry.Key, "radio", "869.525,250,11,5")
	s.Stage(dundry.Key, "name", "Dundry Hill")
	s.Stage(dundry.Key, "name", "Dundry")
	sess.Settings["name"] = Value{Value: "Dundry"}
	s.Stage(dundry.Key, "name", "Dundry") // back to the current value: no longer a change
	changes := s.Changes(dundry.Key)
	if len(changes) != 2 || changes[0].Setting.Key != "radio" || changes[1].From != "22" || changes[1].To != "20" {
		t.Fatalf("changes = %+v", changes)
	}
	first, reboot := s.Apply(dundry.Key, t0)
	if first == nil || first.Text != "set radio 869.525,250,11,5" || !reboot {
		t.Fatalf("apply = %+v reboot=%v", first, reboot)
	}
	s.OnReply(prefix(dundry), "OK - reboot to apply", t0)
	s.OnReply(prefix(dundry), "Error: tx is locked", t0)
	if sess.Settings["radio"].Value != "869.525,250,11,5" || sess.Settings["tx"].Err != "Error: tx is locked" || len(sess.Pending) != 1 {
		t.Fatalf("after applying: %+v pending=%v", sess.Settings, sess.Pending)
	}
	delete(sess.Pending, "tx")
	sess.Settings["bridge.source"] = Value{Value: "logTx"}
	s.Stage(dundry.Key, "bridge.source", "logRx")
	if c, _ := s.Apply(dundry.Key, t0); c == nil || c.Text != "set bridge.source rx" {
		t.Errorf("sent %+v", c)
	}
	s.OnReply(prefix(dundry), "OK", t0)
	if sess.Settings["bridge.source"].Value != "logRx" {
		t.Errorf("reads back as %+v", sess.Settings["bridge.source"])
	}
	sess.Pending["tx"] = "20"

	// A set that never got a reply leaves the edit staged.
	set := s.Command(dundry.Key, "set tx 20", "set tx", t0)
	s.CommandTimedOut(dundry.Key, set.ID, t0)
	if _, staged := sess.Pending["tx"]; !staged {
		t.Error("an unanswered set stays pending")
	}
	if s.Changes([32]byte{0xEE}) != nil {
		t.Error("changes for an unknown node")
	}
}

func TestDescribeLPP(t *testing.T) {
	if got := DescribeLPP([]byte{1, meshcore.LPPVoltage, 0x01}); len(got) != 1 || !strings.Contains(got[0].Value, "couldn't decode") {
		t.Errorf("junk = %+v", got)
	}
	lpp := meshcore.NewLPPEncoder()
	lpp.AddPresence(3, 1)
	if got := DescribeLPP(lpp.Bytes()); got[0].Label != "type 102 (ch 3)" {
		t.Errorf("unnamed type = %+v", got)
	}
	if JobOwner.String() != "owner info" {
		t.Error("job names")
	}
	s := New(nil)
	if s.random() == ([4]byte{}) && s.random() == ([4]byte{}) {
		t.Error("request randomness")
	}
}

// A repeater's console answers at once, so a reply can arrive before the request's tag is known.
func TestReplyBeforeItsTag(t *testing.T) {
	s := newService()
	j := s.RequestOwner(dundry, t0)
	s.RequestStatus(dundry, t0)
	reply := radio.BinaryReply{Tag: 42, Data: meshcore.BuildOwnerInfoReply(meshcore.OwnerInfo{FirmwareVersion: "v1.17.1", Name: "Dundry", Owner: "me"})}
	if sess, next := s.OnBinary(reply, t0); sess != nil || next != nil {
		t.Fatal("held until the tag is known")
	}
	sess, next := s.Sent(j.ID, 42, t0)
	if sess == nil || sess.Owner == nil || sess.Owner.Owner != "me" || next == nil || next.Kind != JobStatus {
		t.Fatalf("matched once the tag arrived: %+v, next %+v", sess, next)
	}
	if sess, _ := s.Sent(999, 1, t0); sess != nil {
		t.Error("a stale Sent is ignored")
	}
	s.TimedOut(next.ID, t0)
	for i := range 20 {
		j := s.RequestACL(dundry, t0)
		s.OnBinary(radio.BinaryReply{Tag: uint32(100 + i)}, t0)
		s.TimedOut(j.ID, t0)
	}
	if len(s.early) > 17 {
		t.Errorf("held replies are bounded: %d", len(s.early))
	}
}

func TestLateReplyIsNotTakenForTheNextCommand(t *testing.T) {
	s := newService()
	s.Session(dundry)
	sess, _ := s.Find(dundry.Key)
	sess.Settings["repeat"] = Value{Value: "on"}
	sess.Pending["repeat"] = "off"

	slow := s.Command(dundry.Key, "clock sync", "", t0)
	if s.CommandTimedOut(dundry.Key, slow.ID, t0) != nil {
		t.Fatal("nothing else queued")
	}
	set := s.Command(dundry.Key, "set repeat off", "set repeat", t0.Add(time.Second))
	_, done, _ := s.OnReply(prefix(dundry), "OK", t0.Add(2*time.Second)) // the clock sync's answer, late
	if done != nil || sess.Settings["repeat"].Value != "on" || sess.Pending["repeat"] != "off" {
		t.Fatalf("a late OK was taken as the set's: done %v, %+v pending %v", done, sess.Settings["repeat"], sess.Pending)
	}
	if last := sess.Console[len(sess.Console)-1]; !strings.Contains(last.Text, "late reply") {
		t.Errorf("console: %+v", last)
	}
	if _, done, _ = s.OnReply(prefix(dundry), "OK", t0.Add(3*time.Second)); done != set || sess.Settings["repeat"].Value != "off" {
		t.Fatalf("the set's own reply: done %v, %+v", done, sess.Settings["repeat"])
	}

	// Once the window has passed, a reply is the answer again.
	slow = s.Command(dundry.Key, "ver", "", t0)
	s.CommandTimedOut(dundry.Key, slow.ID, t0)
	g := s.Command(dundry.Key, "get tx", "get tx", t0.Add(time.Minute))
	if _, done, _ = s.OnReply(prefix(dundry), "> 20", t0.Add(time.Minute)); done != g || sess.Settings["tx"].Value != "20" {
		t.Fatalf("after the window: done %v, %+v", done, sess.Settings["tx"])
	}
}

func TestSecretsStayOffTheConsole(t *testing.T) {
	s := newService()
	s.Session(dundry)
	sess, _ := s.Find(dundry.Key)
	for _, cmd := range []string{"password hunter2", "set guest.password hunter2", "set bridge.secret hunter2", "set prv.key hunter2"} {
		if !Secret(cmd) {
			t.Errorf("%q isn't secret", cmd)
		}
		s.Command(dundry.Key, cmd, "", t0)
		s.OnReply(prefix(dundry), "password now: hunter2", t0)
	}
	for _, l := range sess.Console {
		if strings.Contains(l.Text, "hunter2") {
			t.Errorf("console shows %q", l.Text)
		}
	}
	if Secret("set tx 20") || Secret("ver") || Secret("") {
		t.Error("ordinary commands aren't secret")
	}
}
