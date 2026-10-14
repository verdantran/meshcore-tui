package sim

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"

	"meshcore-tui/internal/radio"
)

// Console is a repeater's serial console as the firmware drives it over USB: each command ends in \r,
// is echoed back, and its reply follows on a line starting "  -> ". Close it to unplug the node.
type Console struct {
	s       *Sim
	srv     *server
	key     [32]byte
	toDev   *io.PipeWriter
	fromDev *io.PipeReader
	devOut  *io.PipeWriter
	logged  []string
}

// Console plugs in one of the simulated repeaters by USB.
func (s *Sim) Console(name string) *Console {
	var k [32]byte
	for _, r := range repeaters {
		if r.name == name {
			k = key(r.hash, r.name)
		}
	}
	srv, ok := s.serverFor(k)
	if !ok {
		panic("sim: no repeater called " + name)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &Console{s: s, srv: srv, key: k, toDev: inW, fromDev: outR, devOut: outW,
		logged: []string{"12:01:07 RX len=42 SNR=9.5 RSSI=-88", "12:01:09 TX len=42 flood"}}
	go c.serve(inR)
	return c
}

func (c *Console) Read(p []byte) (int, error)  { return c.fromDev.Read(p) }
func (c *Console) Write(p []byte) (int, error) { return c.toDev.Write(p) }

func (c *Console) Close() error {
	c.toDev.Close()
	return c.devOut.Close()
}

func (c *Console) serve(in io.Reader) {
	r := bufio.NewReader(in)
	for {
		line, err := r.ReadString('\r')
		if err != nil {
			c.devOut.Close()
			return
		}
		cmd := strings.TrimSpace(strings.TrimRight(line, "\r"))
		fmt.Fprintf(c.devOut, "%s\r\n", cmd)
		out, raw := c.answer(cmd)
		if raw {
			io.WriteString(c.devOut, out)
		} else if out != "" {
			fmt.Fprintf(c.devOut, "  -> %s\r\n", out)
		}
		if cmd == "reboot" {
			c.devOut.Close()
			return
		}
	}
}

// answer runs a command; raw output is printed straight to the port, without the reply marker.
func (c *Console) answer(cmd string) (string, bool) {
	s := c.s
	switch cmd {
	case "stats-core":
		return fmt.Sprintf(`{"battery_mv":4105,"uptime_secs":%d,"errors":0,"queue_len":0}`, s.uptime()), false
	case "stats-radio":
		return `{"noise_floor":-113,"last_rssi":-91,"last_snr":7.25,"tx_air_secs":3200,"rx_air_secs":11000}`, false
	case "stats-packets":
		return `{"recv":48210,"sent":12004,"flood_tx":9881,"direct_tx":2123,"flood_rx":36000,"direct_rx":12000,"recv_errors":14}`, false
	case "neighbors":
		var lines []string
		for i, r := range repeaters[:4] {
			k := key(r.hash, r.name)
			if k == c.key {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s:%d:%d", radio.KeyHex(k[:4]), 60*(i+1), 30-8*i))
		}
		return strings.Join(lines, "\n"), false
	case "get acl":
		s.mu.Lock()
		defer s.mu.Unlock()
		var keys []string
		for k := range c.srv.acl {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("ACL:\r\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "%02X %s%s\r\n", c.srv.acl[k], k, strings.Repeat("0", 64-len(k)))
		}
		return b.String(), true
	case "log":
		return strings.Join(c.logged, "\r\n") + "\r\n  -> EOF\r\n", true
	case "discover.neighbors":
		return "OK", false
	case "reboot", "":
		return "", false
	}
	if strings.HasPrefix(cmd, "neighbor.remove ") {
		return "OK", false
	}
	reply, _ := s.console(c.srv, cmd)
	return reply, false
}
