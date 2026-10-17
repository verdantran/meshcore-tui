package ui

import (
	"fmt"
	"math"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	meshcore "github.com/OwlShack/meshcore-go"

	"meshcore-tui/internal/admin"
	"meshcore-tui/internal/golden"
	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
)

// The animations in SPEC §6.2, each recorded at its start, midpoint and end under the fake clock.
// Effects that draw on their own seed it from the time or the message, so every run draws the same.

// frames renders the screen at each moment after t0.
func frames(t *testing.T, m *Model, c *clock, t0 time.Time, at ...time.Duration) []golden.Frame {
	t.Helper()
	var out []golden.Frame
	for _, d := range at {
		c.t = t0.Add(d)
		m.Update(frameMsg(c.t))
		out = append(out, golden.Frame{Label: fmt.Sprint(d), Lines: screen(t, m)})
	}
	return out
}

// startMiddleEnd are the three moments of an effect lasting d.
func startMiddleEnd(d time.Duration) []time.Duration { return []time.Duration{0, d / 2, d} }

func mainScreen(t *testing.T) (*Model, *clock) {
	t.Helper()
	m, c := newTestModel(t, motion.Full, 100, 30)
	boot(m, c)
	bootDone(t, m, c)
	return m, c
}

func TestGoldenBoot(t *testing.T) {
	m, c := newTestModel(t, motion.Full, 100, 30)
	t0 := c.t
	send(m, c, 0, radio.BootStep{Key: "link", Label: "LINK", State: radio.StepWait})
	send(m, c, 0, radio.BootStep{Key: "link", Label: "LINK", Detail: "/dev/cu.test", State: radio.StepOK})
	send(m, c, 0, radio.BootStep{Key: "hello", Label: "HELLO", Detail: "MeshCore-k4", State: radio.StepWait})
	golden.Check(t, "boot", frames(t, m, c, t0, 0, 150*time.Millisecond, 400*time.Millisecond))
}

func TestGoldenBootFailure(t *testing.T) {
	m, c := newTestModel(t, motion.Full, 100, 30)
	send(m, c, 0, radio.BootStep{Key: "link", Label: "LINK", State: radio.StepWait})
	c.t = c.t.Add(time.Second)
	t0 := c.t
	send(m, c, 0, radio.BootStep{Key: "link", Label: "LINK", Detail: "no such port", State: radio.StepFail})
	send(m, c, 0, radio.Fatal{Err: fmt.Errorf("no such port")})
	golden.Check(t, "boot-failure", frames(t, m, c, t0, 0, glitchDur, 3*glitchDur, time.Second))
}

func TestGoldenScreenWipe(t *testing.T) {
	m, c := mainScreen(t)
	t0 := c.t
	typeKeys(m, "2")
	golden.Check(t, "screen-wipe", frames(t, m, c, t0, startMiddleEnd(wipeDur)...))
}

func TestGoldenPacketHeard(t *testing.T) {
	m, c := mainScreen(t)
	t0 := c.t
	raw := rawPacket(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, []byte{0xA3}, make([]byte, 101))
	send(m, c, 0, radio.Packet{At: t0, SNR: 7.25, RSSI: -92, Raw: raw})
	send(m, c, 0, radio.Transmit{At: t0, What: "SEND_SELF_ADVERT"})
	send(m, c, 0, radio.Battery{MilliVolts: 3900})
	// Lamps hold and fade over 600 ms, numbers count for 500 ms, and the ticker fades for 1.8 s.
	golden.Check(t, "packet-heard", frames(t, m, c, t0, 0, headerLampDur/2, headerLampDur, tickerFade))
}

func TestGoldenThemeFade(t *testing.T) {
	m, c := mainScreen(t)
	t0 := c.t
	typeKeys(m, "T")
	golden.Check(t, "theme-fade", frames(t, m, c, t0, startMiddleEnd(themeFadeDur)...))
}

func TestGoldenChatArrives(t *testing.T) {
	m, c, _ := newChatModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	selectConvo(t, m, "Public")
	tick(m, c, time.Second)
	t0 := c.t
	send(m, c, 0, radio.Incoming{At: t0, Channel: true, ChannelIdx: 0, Hops: 1, SentAt: t0, Text: "Zed: anyone on the hill tonight?", SNR: 6, HasSNR: true})
	// Decoding for 320 ms, and the bar's flash settling by 700 ms.
	golden.Check(t, "chat-arrives", frames(t, m, c, t0, 0, decodeDur/2, decodeDur, arriveDur))
}

func TestGoldenChatDelivered(t *testing.T) {
	m, c, _ := newChatModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	selectConvo(t, m, "Public")
	startConvo(t, m, "Alice")
	key(m, tea.KeyEnter)
	typeKeys(m, "on my way")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	follow(t, m, c, cmd, false)
	tick(m, c, 300*time.Millisecond) // the pending ramp
	sending := golden.Frame{Label: "sending", Lines: screen(t, m)}
	t0 := c.t
	send(m, c, 0, radio.Acked{Ack: 1, RTT: 1400 * time.Millisecond})
	golden.Check(t, "chat-delivered", append([]golden.Frame{sending}, frames(t, m, c, t0, startMiddleEnd(popDur)...)...))
}

func TestGoldenHealthReveal(t *testing.T) {
	m, c := newTestModel(t, motion.Full, 100, 30)
	ct := radio.Contact{Key: [32]byte{7}, Name: "Dundry", Type: radio.TypeRepeater}
	var hist []admin.StatusPoint
	var recv uint32
	for i := range 96 {
		recv += uint32(40 + 30*math.Sin(float64(i)/8))
		hist = append(hist, admin.StatusPoint{At: c.t.Add(time.Duration(i-96) * 15 * time.Minute), Stats: meshcore.RepeaterStats{
			CommonStats: meshcore.CommonStats{BattMilliVolts: uint16(4150 - 6*i), NoiseFloor: int16(-112 + i%9), PacketsRecv: recv, UpTimeSecs: uint32(i * 900)}}})
	}
	m.adm.kept[ct.Key] = hist
	t0 := c.t
	m.adm.paneAt = t0
	var out []golden.Frame
	for _, d := range startMiddleEnd(healthReveal) {
		c.t = t0.Add(d)
		m.Update(frameMsg(c.t))
		out = append(out, golden.Frame{Label: fmt.Sprint(d), Lines: m.healthLines(ct, m.admin.Session(ct), 96, 26)})
	}
	golden.Check(t, "health-reveal", out)
}
