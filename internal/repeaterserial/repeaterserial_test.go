package repeaterserial

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/sim"
)

func next[T radio.Event](t *testing.T, ch <-chan radio.Event) T {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case e := <-ch:
			if v, ok := e.(T); ok {
				return v
			}
		case <-timeout:
			var zero T
			t.Fatalf("no %T", zero)
			return zero
		}
	}
}

func TestMain(m *testing.M) {
	echoWait, quietGap, retryEvery = 300*time.Millisecond, 30*time.Millisecond, 10*time.Millisecond
	m.Run()
}

func TestConsoleRun(t *testing.T) {
	s := sim.New(1).Fast()
	c := NewConsole(s.Console("Dundry"))
	defer c.Close()
	ctx := context.Background()
	if ver, ok := Probe(ctx, c); !ok || !strings.HasPrefix(ver, "v1.17.1 (Build") {
		t.Fatalf("probe = %q %v", ver, ok)
	}
	out, err := c.Run(ctx, "get name", nil)
	if err != nil || Reply(out) != "> Dundry" {
		t.Fatalf("get name = %q %v", out, err)
	}
	lines, _ := c.Run(ctx, "get acl", nil)
	if lines[0] != "ACL:" || len(ParseACL(lines)) != 2 {
		t.Errorf("acl = %q", lines)
	}
	lines, _ = c.Run(ctx, "log", func(l string) bool { return strings.HasSuffix(l, "EOF") })
	if len(lines) < 100 || !strings.HasSuffix(lines[len(lines)-1], "EOF") || !strings.Contains(lines[0], " U: RX, len=") {
		t.Errorf("log = %d lines, starting %q", len(lines), lines[0])
	}
	if out, _ := c.Run(ctx, "", nil); len(out) != 0 {
		t.Errorf("a blank line prints nothing: %q", out)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Run(cctx, "ver", nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	c.Close()
	if _, err := c.Run(ctx, "ver", nil); err == nil {
		t.Error("a closed port errors")
	}
}

// silent is a port where nothing answers, like a companion that doesn't speak text.
type silent struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func newSilent() *silent {
	r, w := io.Pipe()
	return &silent{r, w}
}
func (s *silent) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *silent) Write(p []byte) (int, error) { return len(p), nil }
func (s *silent) Close() error                { return s.w.Close() }

func TestProbeSilentPort(t *testing.T) {
	c := NewConsole(newSilent())
	if _, ok := Probe(context.Background(), c); ok {
		t.Error("nothing answered")
	}
	c.Close()
	if _, ok := Probe(context.Background(), c); ok {
		t.Error("closed port")
	}
}

func TestParsers(t *testing.T) {
	nr := ParseNeighbours("A3010203:60:30\n1E0A0B0C:600:-8\nbad\nZZ:1:2\n-none-")
	if nr.Total != 2 || nr.Neighbours[1].SNR != -8 || nr.Neighbours[0].HeardSecsAgo != 60 {
		t.Errorf("neighbours = %+v", nr)
	}
	acl := ParseACL([]string{"ACL:", "03 " + strings.Repeat("AB", 32), "zz nothex", "01 AB"})
	if len(acl) != 1 || acl[0].Permissions != 3 || acl[0].Prefix[0] != 0xAB {
		t.Errorf("acl = %+v", acl)
	}
	if Reply([]string{"  -> OK", "more"}) != "OK\nmore" {
		t.Error("reply")
	}
}

func startRepeater(t *testing.T, s *sim.Sim, name string) *Repeater {
	t.Helper()
	var mu sync.Mutex
	reopens := 0
	r := NewRepeater("/dev/cu.test", NewConsole(s.Console(name)), func() (io.ReadWriteCloser, error) {
		mu.Lock()
		defer mu.Unlock()
		reopens++
		if reopens == 1 {
			return nil, errors.New("still rebooting")
		}
		return s.Console(name), nil
	})
	t.Cleanup(func() { r.Close() })
	r.Start(context.Background())
	return r
}

func TestRepeater(t *testing.T) {
	s := sim.New(2).Fast()
	r := startRepeater(t, s, "Dundry")
	ready := next[radio.Ready](t, r.Events())
	if !ready.Console || ready.Self.Name != "Dundry" || ready.Device.Firmware != "v1.17.1" || ready.Device.Model != "Heltec V4" ||
		ready.Self.FreqKHz != 869618 || ready.Self.SF != 8 || ready.Self.TxPower != 22 || ready.Battery.MilliVolts != 4105 {
		t.Fatalf("ready = %+v", ready)
	}
	self := ready.ContactList[0]
	if self.Type != radio.TypeRepeater || self.Key[0] != 0xA3 || r.Describe() != "/dev/cu.test · repeater console" || r.Transport() != "USB" {
		t.Errorf("self = %+v", self)
	}
	ctx := context.Background()

	r.Login(ctx, self.Key, "")
	if lr := next[radio.LoginResult](t, r.Events()); !lr.OK || !lr.Admin || lr.Prefix != self.Prefix() {
		t.Errorf("login = %+v", lr)
	}
	r.Logout(ctx, self.Key)
	if err := r.RequestStatus(ctx, self.Key); err != nil {
		t.Fatal(err)
	}
	st, err := meshcore.ParseRepeaterStats(next[radio.StatusReport](t, r.Events()).Data)
	if err != nil || st.BattMilliVolts != 4105 || st.NoiseFloor != -113 || st.LastSNR != 29 || st.RecvErrors != 14 || st.SentFlood != 9881 {
		t.Errorf("status = %+v %v", st, err)
	}
	r.RequestTelemetry(ctx, self.Key)
	if tel, _ := meshcore.LPPDecode(next[radio.Telemetry](t, r.Events()).LPP); len(tel) != 1 {
		t.Errorf("telemetry = %+v", tel)
	}

	binary := func(req []byte) []byte {
		t.Helper()
		tag, err := r.BinaryRequest(ctx, self.Key, req)
		if err != nil {
			t.Fatal(err)
		}
		br := next[radio.BinaryReply](t, r.Events())
		if br.Tag != tag {
			t.Fatalf("tag %d, want %d", br.Tag, tag)
		}
		return br.Data
	}
	nb, _ := meshcore.ParseNeighboursReply(binary(meshcore.BuildNeighboursRequest(meshcore.NeighboursRequest{Count: 8, PrefixLen: 4}, [4]byte{})), 4)
	if nb.Total != 3 {
		t.Errorf("neighbours = %+v", nb)
	}
	acl, _ := meshcore.ParseAccessListReply(binary(meshcore.BuildRequest(meshcore.ReqTypeGetAccessList, [4]byte{})))
	if len(acl) != 2 {
		t.Errorf("acl = %+v", acl)
	}
	owner, _ := meshcore.ParseOwnerInfoReply(binary(meshcore.BuildRequest(meshcore.ReqTypeGetOwnerInfo, [4]byte{})))
	if owner.FirmwareVersion != "v1.17.1" || owner.Name != "Dundry" {
		t.Errorf("owner = %+v", owner)
	}
	if _, err := r.BinaryRequest(ctx, self.Key, []byte{0x03}); err == nil {
		t.Error("telemetry isn't a console request")
	}
	if _, err := r.BinaryRequest(ctx, self.Key, nil); err == nil {
		t.Error("empty request")
	}

	cli := func(cmd string) string {
		t.Helper()
		r.SendCLI(ctx, self.Key, cmd)
		in := next[radio.Incoming](t, r.Events())
		if !in.CLI || in.From != self.Prefix() {
			t.Fatalf("%s: %+v", cmd, in)
		}
		return in.Text
	}
	if got := cli("get tx"); got != "> 22" {
		t.Errorf("get tx = %q", got)
	}
	if got := cli("log"); !strings.Contains(got, " U: RX, len=") || !strings.HasSuffix(got, "EOF") {
		t.Errorf("log = %q", got)
	}
	if got := cli(""); got != "(no output)" {
		t.Errorf("blank = %q", got)
	}
	if got, _ := r.SyncContacts(ctx); len(got) != 1 {
		t.Error("its only contact is itself")
	}

	// Rebooting closes the port; the console comes back when the node does.
	r.SendCLI(ctx, self.Key, "reboot")
	next[radio.Lost](t, r.Events())
	if got := cli("ver"); !strings.Contains(got, "not connected") && !strings.Contains(got, "port closed") {
		t.Errorf("while away: %q", got)
	}
	next[radio.Restored](t, r.Events())
	if got := cli("get name"); got != "> Dundry" {
		t.Errorf("after reboot: %q", got)
	}

	for _, err := range []error{
		r.SendChannel(ctx, 0, "x"), r.ResetPath(ctx, self.Key), r.SaveContact(ctx, self), r.RemoveContact(ctx, self.Key),
		r.DiscoverPath(ctx, self.Key), r.ImportContact(ctx, "x"), r.SetAutoAdd(ctx, radio.AutoAdd{}),
	} {
		if !errors.Is(err, ErrConsoleOnly) {
			t.Errorf("err = %v", err)
		}
	}
	r.SendText(ctx, self.Key, "x")
	r.Trace(ctx, nil)
	r.ExportContact(ctx, self.Key)
	r.AutoAdd(ctx)
	r.AdvertPath(ctx, self.Key)
}

func TestRepeaterThatNeverAnswers(t *testing.T) {
	r := NewRepeater("/dev/cu.test", NewConsole(newSilent()), nil)
	r.Start(context.Background())
	defer r.Close()
	if f := next[radio.Fatal](t, r.Events()); !errors.Is(f.Err, ErrNoAnswer) || !strings.Contains(f.Err.Error(), "companion mode") {
		t.Errorf("fatal = %v", f.Err)
	}
}

func TestDialOpensThePort(t *testing.T) {
	s := sim.New(4).Fast()
	r := NewRepeater("/dev/cu.test", nil, func() (io.ReadWriteCloser, error) { return s.Console("Dundry"), nil })
	defer r.Close()
	r.Start(context.Background())
	if step := next[radio.BootStep](t, r.Events()); step.Key != "port" || step.State != radio.StepWait {
		t.Errorf("first step = %+v", step)
	}
	if ready := next[radio.Ready](t, r.Events()); ready.Self.Name != "Dundry" {
		t.Errorf("ready = %+v", ready)
	}

	missing := Dial("/dev/cu.nothing-here")
	defer missing.Close()
	missing.Start(context.Background())
	if f := next[radio.Fatal](t, missing.Events()); f.Err == nil {
		t.Error("a port that isn't there fails")
	}
}

// fakeCompanion is a companion link that either connects or gives up, for USB detection.
type fakeCompanion struct {
	events chan radio.Event
	fail   bool
	closed bool
	radio.Radio
}

func (f *fakeCompanion) Start(context.Context) {
	f.events <- radio.BootStep{Key: "link", Label: "LINK", State: radio.StepOK}
	f.events <- radio.BootStep{Key: "device", Label: "DEVICE", State: radio.StepFail} // not fatal
	f.events <- radio.BootStep{Key: "hello", Label: "HELLO", State: radio.StepFail}
	if f.fail {
		f.events <- radio.Fatal{Err: errors.New("no APP_START reply")}
		return
	}
	f.events <- radio.Ready{}
}
func (f *fakeCompanion) Events() <-chan radio.Event { return f.events }
func (f *fakeCompanion) Close() error               { f.closed = true; return nil }
func (f *fakeCompanion) Describe() string           { return "/dev/cu.test" }

func TestUSBPrefersACompanion(t *testing.T) {
	comp := &fakeCompanion{events: make(chan radio.Event, 16)}
	u := &USB{port: "/dev/cu.test", events: make(chan radio.Event, 64), newCompanion: func() radio.Radio { return comp },
		open: func() (io.ReadWriteCloser, error) { t.Fatal("no probe when a companion answers"); return nil, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if u.Describe() != "/dev/cu.test" || u.Close() != nil {
		t.Error("before start")
	}
	u.Start(ctx)
	var steps []string
	for {
		e := <-u.Events()
		if b, ok := e.(radio.BootStep); ok {
			steps = append(steps, b.Key)
		}
		if _, ok := e.(radio.Ready); ok {
			break
		}
	}
	if strings.Join(steps, ",") != "link,device,hello" || u.Describe() != "/dev/cu.test" || u.Transport() != "USB" {
		t.Errorf("held failures are passed on once the connect carries on: %v", steps)
	}
}

func TestUSBFindsARepeater(t *testing.T) {
	s := sim.New(3).Fast()
	comp := &fakeCompanion{events: make(chan radio.Event, 16), fail: true}
	u := &USB{port: "/dev/cu.test", events: make(chan radio.Event, 64), newCompanion: func() radio.Radio { return comp },
		open: func() (io.ReadWriteCloser, error) { return s.Console("Purdown"), nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u.Start(ctx)
	ready := next[radio.Ready](t, u.Events())
	if !ready.Console || ready.Self.Name != "Purdown" || !comp.closed || !strings.Contains(u.Describe(), "repeater console") {
		t.Fatalf("ready = %+v", ready)
	}
	self := ready.ContactList[0]
	u.Login(ctx, self.Key, "")
	next[radio.LoginResult](t, u.Events())
	u.SendCLI(ctx, self.Key, "get name")
	if in := next[radio.Incoming](t, u.Events()); in.Text != "> Purdown" {
		t.Errorf("cli = %+v", in)
	}
	u.RequestStatus(ctx, self.Key)
	u.RequestTelemetry(ctx, self.Key)
	u.BinaryRequest(ctx, self.Key, meshcore.BuildRequest(meshcore.ReqTypeGetOwnerInfo, [4]byte{}))
	u.Logout(ctx, self.Key)
	u.SyncContacts(ctx)
	u.SendText(ctx, self.Key, "x")
	u.SendChannel(ctx, 0, "x")
	u.ResetPath(ctx, self.Key)
	u.SaveContact(ctx, self)
	u.RemoveContact(ctx, self.Key)
	u.Trace(ctx, nil)
	u.DiscoverPath(ctx, self.Key)
	u.AdvertPath(ctx, self.Key)
	u.ExportContact(ctx, self.Key)
	u.ImportContact(ctx, "")
	u.AutoAdd(ctx)
	u.SetAutoAdd(ctx, radio.AutoAdd{})
	u.Close()
}

func TestUSBReportsTheFailureWhenNothingAnswers(t *testing.T) {
	for _, open := range []func() (io.ReadWriteCloser, error){
		func() (io.ReadWriteCloser, error) { return nil, errors.New("busy") },
		func() (io.ReadWriteCloser, error) { return newSilent(), nil },
	} {
		comp := &fakeCompanion{events: make(chan radio.Event, 16), fail: true}
		u := &USB{port: "/dev/cu.test", events: make(chan radio.Event, 64), newCompanion: func() radio.Radio { return comp }, open: open}
		ctx, cancel := context.WithCancel(context.Background())
		u.Start(ctx)
		var failed []string
		for {
			e := <-u.Events()
			if b, ok := e.(radio.BootStep); ok && b.State == radio.StepFail {
				failed = append(failed, b.Key)
			}
			if _, ok := e.(radio.Fatal); ok {
				break
			}
		}
		if strings.Join(failed, ",") != "device,hello" {
			t.Errorf("failures = %v", failed)
		}
		cancel()
	}
	if NewUSB("/dev/cu.nothing").Describe() != "/dev/cu.nothing" {
		t.Error("describe")
	}
}
