package radio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/OwlShack/meshcore-go/companion"
	"go.bug.st/serial"
)

const (
	staleAfter   = time.Second // a frame still incomplete after this was a false start
	redialMax    = 30 * time.Second
	readInterval = 200 * time.Millisecond
)

// redialFirst is the first pause before redialling, doubling up to redialMax; tests shorten it.
var redialFirst = time.Second

var errNotConnected = errors.New("not connected to the radio")

// stream is the companion link over a serial port or TCP. It replaces the SDK's transports because their
// frame reader trusts any length, so a few dropped bytes can leave it waiting for 64 KB that never come.
type stream struct {
	dial func(context.Context) (io.ReadWriteCloser, error)

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	conn      io.ReadWriteCloser
	onResp    func(companion.Response)
	onErr     func(error)
	onDrop    func()
	onRestore func()

	write   sync.Mutex
	inbound chan companion.Response
}

func newStream(dial func(context.Context) (io.ReadWriteCloser, error)) *stream {
	ctx, cancel := context.WithCancel(context.Background())
	return &stream{dial: dial, ctx: ctx, cancel: cancel, inbound: make(chan companion.Response, 256)}
}

func serialStream(port string) *stream {
	return newStream(func(context.Context) (io.ReadWriteCloser, error) {
		p, err := serial.Open(port, &serial.Mode{BaudRate: 115200})
		if err != nil {
			return nil, err
		}
		if err := p.SetReadTimeout(readInterval); err != nil {
			p.Close()
			return nil, err
		}
		return p, nil
	})
}

func tcpStream(addr string) *stream {
	return newStream(func(ctx context.Context) (io.ReadWriteCloser, error) {
		var d net.Dialer
		c, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		return pacedConn{c}, nil
	})
}

// pacedConn makes TCP reads return every readInterval, as the serial port does, so stale frames are noticed.
type pacedConn struct{ net.Conn }

func (c pacedConn) Read(b []byte) (int, error) {
	c.SetReadDeadline(time.Now().Add(readInterval))
	n, err := c.Conn.Read(b)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		err = nil
	}
	return n, err
}

func (s *stream) SetResponseHandler(h func(companion.Response)) { s.set(func() { s.onResp = h }) }
func (s *stream) SetErrorHandler(h func(error))                 { s.set(func() { s.onErr = h }) }
func (s *stream) SetDisconnectHandler(h func())                 { s.set(func() { s.onDrop = h }) }
func (s *stream) SetReconnectHandler(h func())                  { s.set(func() { s.onRestore = h }) }

func (s *stream) set(f func()) {
	s.mu.Lock()
	f()
	s.mu.Unlock()
}

func (s *stream) Connect(ctx context.Context) error {
	c, err := s.dial(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.conn = c
	s.mu.Unlock()
	go s.dispatch()
	go s.readLoop(c)
	return nil
}

func (s *stream) Close() error {
	s.cancel()
	s.mu.Lock()
	c := s.conn
	s.conn = nil
	s.mu.Unlock()
	if c != nil {
		return c.Close()
	}
	return nil
}

// Reset drops the connection so the read loop dials again, for when the radio has gone silent.
func (s *stream) Reset() {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

func (s *stream) Send(cmd []byte) error {
	frame, err := companion.FrameEncode(companion.FrameTypeOutgoing, cmd)
	if err != nil {
		return err
	}
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	if c == nil {
		return errNotConnected
	}
	s.write.Lock()
	defer s.write.Unlock()
	if _, err := c.Write(frame); err != nil {
		c.Close() // the read loop sees it and redials
		return err
	}
	return nil
}

func (s *stream) dispatch() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case r := <-s.inbound:
			s.mu.Lock()
			h := s.onResp
			s.mu.Unlock()
			if h != nil {
				h(r)
			}
		}
	}
}

func (s *stream) readLoop(c io.ReadWriteCloser) {
	for {
		err := s.read(c)
		if s.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		if s.conn == c {
			s.conn = nil
		}
		drop, report := s.onDrop, s.onErr
		s.mu.Unlock()
		c.Close()
		if report != nil && err != nil {
			report(err)
		}
		if drop != nil {
			drop()
		}
		if c = s.redial(); c == nil {
			return
		}
	}
}

func (s *stream) read(c io.Reader) error {
	var fr frameReader
	buf := make([]byte, 512)
	for {
		n, err := c.Read(buf)
		for _, r := range fr.feed(buf[:n], time.Now()) {
			select {
			case s.inbound <- r:
			case <-s.ctx.Done():
				return nil
			}
		}
		if err != nil {
			return err
		}
		if s.ctx.Err() != nil {
			return nil
		}
	}
}

func (s *stream) redial() io.ReadWriteCloser {
	wait := redialFirst
	for {
		select {
		case <-s.ctx.Done():
			return nil
		case <-time.After(wait):
		}
		c, err := s.dial(s.ctx)
		if err != nil {
			wait = min(wait*2, redialMax)
			continue
		}
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			c.Close()
			return nil
		}
		s.conn = c
		restore := s.onRestore
		s.mu.Unlock()
		if restore != nil {
			restore()
		}
		return c
	}
}

// frameReader finds the radio's frames in a byte stream: '>', a little-endian length, then that many bytes.
// A header is believed only if its length is one the firmware can send and the frame parses or ends where
// another begins; otherwise the reader moves on a byte, so a garbled stretch costs a frame or two at most.
type frameReader struct {
	buf     []byte
	partial time.Time
}

func (f *frameReader) feed(b []byte, now time.Time) []companion.Response {
	f.buf = append(f.buf, b...)
	var out []companion.Response
	for {
		i := bytes.IndexByte(f.buf, companion.FrameTypeIncoming)
		if i < 0 {
			f.buf = f.buf[:0]
			break
		}
		f.buf = f.buf[i:]
		if len(f.buf) < companion.FrameHeaderSize {
			break
		}
		n := int(binary.LittleEndian.Uint16(f.buf[1:3]))
		if n == 0 || n > companion.MaxFrameSize {
			f.buf = f.buf[1:]
			continue
		}
		if len(f.buf) < companion.FrameHeaderSize+n {
			if f.partial.IsZero() {
				f.partial = now
			}
			if now.Sub(f.partial) < staleAfter {
				break
			}
			f.buf, f.partial = f.buf[1:], time.Time{}
			continue
		}
		end := companion.FrameHeaderSize + n
		r, err := companion.ParseResponse(bytes.Clone(f.buf[3:end]))
		if err != nil {
			// A real frame the SDK can't parse still ends where the next one starts.
			if len(f.buf) > end && f.buf[end] != companion.FrameTypeIncoming {
				f.buf = f.buf[1:]
				continue
			}
		} else {
			out = append(out, r)
		}
		f.buf, f.partial = f.buf[end:], time.Time{}
	}
	if len(f.buf) == 0 {
		f.partial = time.Time{}
		f.buf = nil
	}
	return out
}
