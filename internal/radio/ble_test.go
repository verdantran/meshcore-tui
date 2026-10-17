package radio

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OwlShack/meshcore-go/companion"
)

type fakeBLE struct {
	mu      sync.Mutex
	dials   int
	fail    error
	writes  [][]byte
	notify  func([]byte)
	dropped func()
	closed  int
}

func (f *fakeBLE) connect(_ context.Context, addr string, notify func([]byte), dropped func()) (blePeer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dials++
	if f.fail != nil {
		return nil, f.fail
	}
	f.notify, f.dropped = notify, dropped
	return fakePeer{f}, nil
}

func (f *fakeBLE) scan(ctx context.Context, found func(BLEDevice)) error {
	found(BLEDevice{Addr: "AA:BB:CC:DD:EE:01", Name: "MeshCore-k4", RSSI: -60})
	<-ctx.Done()
	return nil
}

func (f *fakeBLE) state() (int, [][]byte, func([]byte), func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials, f.writes, f.notify, f.dropped
}

type fakePeer struct{ f *fakeBLE }

func (p fakePeer) write(b []byte) error {
	p.f.mu.Lock()
	defer p.f.mu.Unlock()
	p.f.writes = append(p.f.writes, append([]byte(nil), b...))
	return nil
}

func (p fakePeer) close() error {
	p.f.mu.Lock()
	defer p.f.mu.Unlock()
	p.f.closed++
	return nil
}

func TestBLEStream(t *testing.T) {
	saved := redialFirst
	redialFirst = 10 * time.Millisecond
	t.Cleanup(func() { redialFirst = saved })
	f := &fakeBLE{}
	s := bleStream(f, "AA:BB:CC:DD:EE:01")
	got := make(chan received, 4)
	restored := make(chan struct{}, 1)
	s.SetFrameHandler(func(r companion.Response, raw []byte) { got <- received{r, raw} })
	s.SetErrorHandler(func(error) {})
	s.SetDisconnectHandler(func() {})
	s.SetReconnectHandler(func() { restored <- struct{}{} })
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// A command goes out as its payload alone, one write per frame.
	if err := s.Send([]byte{companion.CmdAppStart, 1, 2}); err != nil {
		t.Fatal(err)
	}
	if _, w, _, _ := f.state(); len(w) != 1 || !bytes.Equal(w[0], []byte{companion.CmdAppStart, 1, 2}) {
		t.Errorf("writes = %x", w)
	}

	// Each notification is one response, with its bytes for the frames panel.
	_, _, notify, dropped := f.state()
	ok := companion.OkResponse{}.ToBytes()
	notify(ok)
	notify(companion.PushMsgWaitingResponse{}.ToBytes())
	for _, want := range []byte{companion.RespOk, companion.PushMsgWaiting} {
		select {
		case r := <-got:
			if r.resp.Code != want || r.resp.Code == companion.RespOk && !bytes.Equal(r.raw, ok) {
				t.Errorf("got %x %x, want %x", r.resp.Code, r.raw, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no response %x", want)
		}
	}

	// A dropped connection is dialled again.
	dropped()
	select {
	case <-restored:
	case <-time.After(2 * time.Second):
		t.Fatal("no redial")
	}
	if dials, _, _, _ := f.state(); dials != 2 {
		t.Errorf("dials = %d", dials)
	}
}

func TestBLEConnWritesOnlyFrames(t *testing.T) {
	c := newBLEConn()
	c.peer = fakePeer{&fakeBLE{}}
	if _, err := c.Write([]byte("hello")); err == nil {
		t.Error("only framed commands are written")
	}
	if n, err := c.Read(make([]byte, 8)); n != 0 || err != nil {
		t.Errorf("a quiet link reads nothing for a while: %d %v", n, err)
	}
	c.Close()
	if _, err := c.Read(make([]byte, 8)); err == nil {
		t.Error("a closed link ends")
	}
}

func TestBLEHints(t *testing.T) {
	saved := bleOS
	t.Cleanup(func() { bleOS = saved })
	for _, c := range []struct {
		goos, err, want string
	}{
		{"linux", "org.bluez.Error.NotPermitted: Not paired", "bluetoothctl"},
		{"windows", "access denied", "Settings → Bluetooth & devices"},
		{"darwin", "insufficient encryption", "System Settings → Bluetooth"},
		{"darwin", "bluetooth is not authorized for this app", "Privacy & Security"},
		{"linux", "bluetooth adapter not powered", "turn Bluetooth on"},
	} {
		bleOS = c.goos
		if got := bleHint(errors.New(c.err)).Error(); !strings.Contains(got, c.want) {
			t.Errorf("%s %q: %s", c.goos, c.err, got)
		}
	}
	if bleHint(nil) != nil || bleHint(errors.New("timeout")).Error() != "timeout" {
		t.Error("other errors pass through")
	}

	f := &fakeBLE{fail: errors.New("not paired")}
	if err := bleStream(f, "x").Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "pair it once") {
		t.Errorf("a first connection that needs pairing says how: %v", err)
	}
}
