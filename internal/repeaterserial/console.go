// Package repeaterserial talks to a repeater or room server plugged in by USB, through its plain-text
// serial console, and presents it as a radio so the Admin screen works the same as over the air.
package repeaterserial

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

var (
	ErrClosed   = errors.New("the serial port closed")
	ErrNoAnswer = errors.New("no answer on the console")
)

var (
	echoWait = 1500 * time.Millisecond // how long to wait for the echo
	quietGap = 300 * time.Millisecond  // a reply is over once the port is quiet this long
	replyGap = 60 * time.Millisecond   // or this long after the "  -> " line, which is printed in one go
)

// Console runs commands on the firmware's serial CLI. A command ends in \r; the node echoes it,
// then replies on a line starting "  -> ". A few commands print straight to the port instead.
type Console struct {
	rw    io.ReadWriteCloser
	lines chan string
	mu    sync.Mutex
}

func NewConsole(rw io.ReadWriteCloser) *Console {
	c := &Console{rw: rw, lines: make(chan string, 1024)}
	go c.read()
	return c
}

// A node can print faster than the baud rate suggests, so what's kept of its output is capped.
const (
	maxLine  = 4096
	maxReply = 1 << 20
)

// read splits the port into lines, cutting each at maxLine and dropping the rest of it.
func (c *Console) read() {
	defer close(c.lines)
	br := bufio.NewReaderSize(c.rw, maxLine)
	for {
		b, more, err := br.ReadLine()
		if err != nil {
			return
		}
		l := strings.TrimRight(string(b), "\r")
		for more {
			if _, more, err = br.ReadLine(); err != nil {
				return
			}
		}
		c.lines <- l
	}
}

func (c *Console) Close() error { return c.rw.Close() }

// Run sends a command and returns what the node printed after echoing it. until, if given, ends
// the reply early at a line it accepts, for long output such as the log.
func (c *Console) Run(ctx context.Context, cmd string, until func(string) bool) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for drained := false; !drained; {
		select {
		case _, ok := <-c.lines:
			if !ok {
				return nil, ErrClosed
			}
		default:
			drained = true
		}
	}
	if _, err := io.WriteString(c.rw, cmd+"\r"); err != nil {
		return nil, err
	}
	var out []string
	heard, size, full := false, 0, false
	timer := time.NewTimer(echoWait)
	defer timer.Stop()
	for {
		select {
		case l, ok := <-c.lines:
			if !ok {
				return out, ErrClosed
			}
			if heard || strings.TrimSpace(l) != strings.TrimSpace(cmd) { // the first line is normally the echo
				if size += len(l); size > maxReply && !full {
					out, full = append(out, "… output cut at 1 MB"), true
				}
				if !full {
					out = append(out, l)
				}
				if until != nil && until(l) {
					return out, nil
				}
			}
			heard = true
			if strings.HasPrefix(strings.TrimLeft(l, " "), "-> ") {
				timer.Reset(replyGap)
			} else {
				timer.Reset(quietGap)
			}
		case <-timer.C:
			if !heard {
				return nil, ErrNoAnswer
			}
			if until != nil && len(out) == 0 {
				timer.Reset(quietGap) // still waiting for the first line of long output
				continue
			}
			return out, nil
		case <-ctx.Done():
			return out, ctx.Err()
		}
	}
}

// Reply joins a command's output, dropping the "  -> " that marks the firmware's reply line.
func Reply(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		if r, ok := strings.CutPrefix(strings.TrimLeft(l, " "), "-> "); ok {
			l = r
		}
		out[i] = l
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// Probe reports whether a repeater's console answers on this port, and what version it runs.
func Probe(ctx context.Context, c *Console) (string, bool) {
	lines, err := c.Run(ctx, "ver", nil)
	if err != nil {
		return "", false
	}
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimLeft(l, " "), "-> ") {
			return Reply(lines), true
		}
	}
	return "", false
}
