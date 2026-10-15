package radio

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/OwlShack/meshcore-go/companion"
)

func frameOf(payload []byte) []byte {
	f, _ := companion.FrameEncode(companion.FrameTypeIncoming, payload)
	return f
}

func codes(rs []companion.Response) []byte {
	var out []byte
	for _, r := range rs {
		out = append(out, r.Code)
	}
	return out
}

func TestFrameReaderResyncs(t *testing.T) {
	ok := frameOf(companion.OkResponse{}.ToBytes())
	waiting := frameOf(companion.PushMsgWaitingResponse{}.ToBytes())
	now := time.Now()

	var f frameReader
	// Noise, a header claiming 65 KB, and a frame split across reads.
	got := f.feed(append([]byte("boot noise >\xff\xff"), ok[:2]...), now)
	got = append(got, f.feed(append(ok[2:], waiting...), now)...)
	if !bytes.Equal(codes(got), []byte{companion.RespOk, companion.PushMsgWaiting}) {
		t.Errorf("codes = %x", codes(got))
	}

	// A plausible false header waits for its bytes, then is given up once it goes stale.
	f = frameReader{}
	if got := f.feed([]byte{'>', 40, 0, 1, 2}, now); len(got) != 0 {
		t.Fatal("nothing yet")
	}
	if got := f.feed(nil, now.Add(staleAfter/2)); len(got) != 0 {
		t.Fatal("still waiting")
	}
	got = f.feed(ok, now.Add(staleAfter+time.Millisecond))
	if !bytes.Equal(codes(got), []byte{companion.RespOk}) {
		t.Errorf("after a stale header = %x", codes(got))
	}

	// A frame the SDK can't parse is skipped whole when the next frame follows it.
	f = frameReader{}
	bad := frameOf([]byte{companion.RespContactMsgRecvV3, 1})
	got = f.feed(append(bad, ok...), now)
	if !bytes.Equal(codes(got), []byte{companion.RespOk}) || len(f.buf) != 0 {
		t.Errorf("after a bad frame = %x, %d left", codes(got), len(f.buf))
	}
}

// radioServer accepts TCP connections and records what arrives on each.
type radioServer struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
	got   bytes.Buffer
}

func newRadioServer(t *testing.T) *radioServer {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &radioServer{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, c)
			s.mu.Unlock()
			go func() {
				b := make([]byte, 256)
				for {
					n, err := c.Read(b)
					s.mu.Lock()
					s.got.Write(b[:n])
					s.mu.Unlock()
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *radioServer) latest() net.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.conns) == 0 {
		return nil
	}
	return s.conns[len(s.conns)-1]
}

func (s *radioServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestStreamRedials(t *testing.T) {
	saved := redialFirst
	redialFirst = 10 * time.Millisecond
	t.Cleanup(func() { redialFirst = saved })
	srv := newRadioServer(t)
	s := tcpStream(srv.ln.Addr().String())
	var mu sync.Mutex
	var got []byte
	drops, restores := 0, 0
	s.SetResponseHandler(func(r companion.Response) { mu.Lock(); got = append(got, r.Code); mu.Unlock() })
	s.SetErrorHandler(func(error) {})
	s.SetDisconnectHandler(func() { mu.Lock(); drops++; mu.Unlock() })
	s.SetReconnectHandler(func() { mu.Lock(); restores++; mu.Unlock() })
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitFor(t, "a connection", func() bool { return srv.count() == 1 })

	if err := s.Send([]byte{companion.CmdAppStart}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the command", func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return bytes.Equal(srv.got.Bytes(), []byte{'<', 1, 0, companion.CmdAppStart})
	})
	srv.latest().Write(frameOf(companion.OkResponse{}.ToBytes()))
	waitFor(t, "the reply", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 1 })

	// The radio end hangs up: the stream redials and carries on.
	srv.latest().Close()
	waitFor(t, "a redial", func() bool { mu.Lock(); defer mu.Unlock(); return drops == 1 && restores == 1 })
	waitFor(t, "a second connection", func() bool { return srv.count() == 2 })
	srv.latest().Write(frameOf(companion.PushMsgWaitingResponse{}.ToBytes()))
	waitFor(t, "a push after the redial", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 2 })

	// Reset forces the same, for a radio that has gone silent.
	s.Reset()
	waitFor(t, "a third connection", func() bool { return srv.count() == 3 })

	s.Close()
	if err := s.Send([]byte{companion.CmdAppStart}); err != errNotConnected {
		t.Errorf("send after close = %v", err)
	}
}

func TestStreamDialFailure(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if err := tcpStream(addr).Connect(context.Background()); err == nil {
		t.Error("nothing is listening")
	}
}
