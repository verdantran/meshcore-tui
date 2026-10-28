package radio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/OwlShack/meshcore-go/companion"
)

// BLEDevice is a radio found by a Bluetooth scan.
type BLEDevice struct {
	Addr, Name string
	RSSI       int16
}

// bleAdapter is the system's Bluetooth, behind an interface so tests can stand in for it.
type bleAdapter interface {
	// connect reaches the radio at addr and subscribes to it: notify gets each frame it sends,
	// and dropped is called if the connection goes.
	connect(ctx context.Context, addr string, notify func([]byte), dropped func()) (blePeer, error)
	scan(ctx context.Context, found func(BLEDevice)) error
}

// blePeer is one connected radio.
type blePeer interface {
	write(frame []byte) error
	close() error
}

// NewBLE is a radio over Bluetooth. The link carries one companion frame per write and per
// notification; bleConn turns that into the byte stream the serial and TCP links use, so it
// shares their framing, redialling and watchdog.
func NewBLE(addr string) *Link {
	return newLink(addr, "BLE", bleStream(systemBLE(), addr))
}

// ScanBLE reports MeshCore radios nearby until ctx ends.
func ScanBLE(ctx context.Context, found func(BLEDevice)) error {
	return bleHint(systemBLE().scan(ctx, found))
}

func bleStream(a bleAdapter, addr string) *stream {
	return newStream(func(ctx context.Context) (io.ReadWriteCloser, error) {
		c := newBLEConn()
		p, err := a.connect(ctx, addr, c.notified, c.dropped)
		if err != nil {
			return nil, bleHint(err)
		}
		c.peer = p
		return c, nil
	})
}

type bleConn struct {
	peer    blePeer
	in      chan []byte
	gone    chan struct{}
	once    sync.Once
	pending []byte
}

func newBLEConn() *bleConn { return &bleConn{in: make(chan []byte, 64), gone: make(chan struct{})} }

// notified frames each notification as the radio would over USB.
func (c *bleConn) notified(b []byte) {
	f, err := companion.FrameEncode(companion.FrameTypeIncoming, b)
	if err != nil {
		return
	}
	select {
	case c.in <- f:
	case <-c.gone:
	}
}

func (c *bleConn) dropped() { c.once.Do(func() { close(c.gone) }) }

// Read returns every readInterval even when nothing came, as the serial port does.
func (c *bleConn) Read(b []byte) (int, error) {
	if len(c.pending) == 0 {
		select {
		case f := <-c.in:
			c.pending = f
		case <-c.gone:
			return 0, io.EOF
		case <-time.After(readInterval):
			return 0, nil
		}
	}
	n := copy(b, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

// Write takes one framed command, as the stream sends them, and writes its payload alone.
func (c *bleConn) Write(b []byte) (int, error) {
	if len(b) <= companion.FrameHeaderSize || b[0] != companion.FrameTypeOutgoing {
		return 0, errors.New("not a companion frame")
	}
	if err := c.peer.write(b[companion.FrameHeaderSize:]); err != nil {
		return 0, bleHint(err)
	}
	return len(b), nil
}

func (c *bleConn) Close() error {
	c.dropped()
	return c.peer.close()
}

var bleOS = runtime.GOOS

// bleMTUWait is how long a write waits for the system to agree a larger MTU, as Windows does soon after connecting.
var bleMTUWait = 2 * time.Second

// bleRoomFor is whether an n-byte frame fits one write, waiting up to bleMTUWait for the link to
// carry more. It returns the most a write carries, for saying why when it doesn't.
func bleRoomFor(n int, carries func() int, sleep func(time.Duration)) (int, bool) {
	const step = 100 * time.Millisecond
	most := 0
	for waited := time.Duration(0); ; waited += step {
		if most = carries(); most == 0 || n <= most {
			return most, true
		}
		if waited >= bleMTUWait {
			return most, false
		}
		sleep(step)
	}
}

// attCode finds the ATT error code Windows gives on its own, as in "Bluetooth ATT error (code 0x0005)".
var attCode = regexp.MustCompile(`att error \(code 0x([0-9a-f]{4})\)`)

// attPairing is whether msg is an ATT error that means the link isn't paired: insufficient
// authentication, authorization, key size or encryption.
func attPairing(msg string) bool {
	m := attCode.FindStringSubmatch(msg)
	return m != nil && (m[1] == "0005" || m[1] == "0008" || m[1] == "000c" || m[1] == "000f")
}

// bleHint says what to do about the errors a first connection usually meets.
func bleHint(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(msg, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("not authorized", "unauthorized"):
		return fmt.Errorf("%w: allow your terminal to use Bluetooth in System Settings → Privacy & Security → Bluetooth", err)
	case has("powered off", "not powered", "adapter not found", "no bluetooth adapter"):
		return fmt.Errorf("%w: turn Bluetooth on and try again", err)
	case has("auth", "encrypt", "notpermitted", "not permitted", "insufficient", "not paired", "accessdenied", "access denied") || attPairing(msg):
		pin := "the PIN on the radio's screen, or 123456 if it has none"
		switch bleOS {
		case "linux":
			return fmt.Errorf("%w: pair it once first: run bluetoothctl, then pair and trust its address, with %s", err, pin)
		case "windows":
			return fmt.Errorf("%w: pair it once first in Windows Settings (Win+I) → Bluetooth & devices → Add device, with %s", err, pin)
		}
		return fmt.Errorf("%w: macOS asks for %s when it first connects; if it didn't, remove the radio in System Settings → Bluetooth and connect again", err, pin)
	}
	return err
}
