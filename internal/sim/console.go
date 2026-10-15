package sim

import (
	"bufio"
	"fmt"
	"io"
	"math/rand"
	"sort"
	"strings"
	"time"

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
		logged: packetLog(time.Now(), int64(k[0]))}
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

// packetLog is a few hours of a repeater's packet log, in the firmware's format.
func packetLog(now time.Time, seed int64) []string {
	rng := rand.New(rand.NewSource(seed))
	types := []int{4, 4, 4, 5, 5, 5, 5, 2, 2, 3, 3, 1, 0, 8, 9}
	var out []string
	at := now.Add(-6 * time.Hour).UTC()
	for at.Before(now) {
		at = at.Add(time.Duration(20+rng.Intn(100)) * time.Second)
		t := types[rng.Intn(len(types))]
		route := "F"
		if t == 1 || t == 2 || t == 3 || t == 8 {
			if rng.Intn(3) > 0 {
				route = "D"
			}
		}
		plen := 10 + rng.Intn(100)
		pair := ""
		if t <= 2 || t == 8 {
			pair = fmt.Sprintf(" [%02X -> %02X]", []byte{0x3A, 0x7C, 0xB4, 0x1E, 0x52}[rng.Intn(5)], rng.Intn(256))
		}
		stamp := fmt.Sprintf("%02d:%02d:%02d - %d/%d/%d U: ", at.Hour(), at.Minute(), at.Second(), at.Day(), int(at.Month()), at.Year())
		snr := 12 - rng.Intn(26)
		out = append(out, stamp+fmt.Sprintf("RX, len=%d (type=%d, route=%s, payload_len=%d) SNR=%d RSSI=%d score=%d%s",
			plen+2+rng.Intn(8), t, route, plen, snr, -70-rng.Intn(50), min(1000, 600+snr*40), pair))
		if rng.Intn(10) < 6 {
			out = append(out, stamp+fmt.Sprintf("TX, len=%d (type=%d, route=%s, payload_len=%d)%s", plen+3+rng.Intn(8), t, route, plen, pair))
		} else if rng.Intn(40) == 0 {
			out = append(out, stamp+fmt.Sprintf("TX FAIL!, len=%d (type=%d, route=%s, payload_len=%d)", plen+3, t, route, plen))
		}
	}
	return out
}

// answer runs a command; raw output is printed straight to the port, without the reply marker.
func (c *Console) answer(cmd string) (string, bool) {
	s := c.s
	switch cmd {
	case "stats-core":
		st := s.counters(0)
		return fmt.Sprintf(`{"battery_mv":%d,"uptime_secs":%d,"errors":0,"queue_len":0}`, st.BattMilliVolts, st.UpTimeSecs), false
	case "stats-radio":
		st := s.counters(0)
		return fmt.Sprintf(`{"noise_floor":%d,"last_rssi":-91,"last_snr":7.25,"tx_air_secs":%d,"rx_air_secs":%d}`, st.NoiseFloor, st.AirTimeSecs, st.RxAirTimeSecs), false
	case "stats-packets":
		st := s.counters(0)
		return fmt.Sprintf(`{"recv":%d,"sent":%d,"flood_tx":%d,"direct_tx":%d,"flood_rx":%d,"direct_rx":%d,"recv_errors":%d}`,
			st.PacketsRecv, st.PacketsSent, st.SentFlood, st.SentDirect, st.RecvFlood, st.RecvDirect, st.RecvErrors), false
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
