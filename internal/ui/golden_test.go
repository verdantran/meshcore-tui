package ui

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/golden"
	"github.com/verdantran/meshcore-tui/internal/mapview"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

// The app's animations, each recorded at its start, midpoint and end under the fake clock.
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

func TestGoldenComposeNearLimit(t *testing.T) {
	m, c, _ := newChatModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	startConvo(t, m, "Alice")
	key(m, tea.KeyEnter)
	_, limit := m.composeLoad()
	m.chats.input.set(strings.Repeat("a", limit-3))
	if !m.pulsing() {
		t.Fatal("near the limit, the counter pulses")
	}
	t0 := pulseStart(c.t)
	near := frames(t, m, c, t0, 0, pulsePeriod/2)
	m.chats.input.set(strings.Repeat("a", limit+2))
	golden.Check(t, "compose-near-limit", append(near, frames(t, m, c, t0, pulsePeriod, pulsePeriod*3/2)...))
	m.chats.input.set("hi")
	if m.pulsing() {
		t.Error("well inside the limit, nothing pulses")
	}
}

// pulseStart is the next moment a pulse is at its brightest, for frames that line up with it.
func pulseStart(t time.Time) time.Time {
	return time.Unix(0, (t.UnixNano()/int64(pulsePeriod)+1)*int64(pulsePeriod))
}

func TestGoldenDangerConfirm(t *testing.T) {
	m, c, _ := newChatModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	typeKeys(m, "2x")
	if !m.dangerWaiting(1) || !m.pulsing() {
		t.Fatal("removing a contact asks first, with a pulsing border")
	}
	golden.Check(t, "danger-confirm", frames(t, m, c, pulseStart(c.t), 0, pulsePeriod/2))
	typeKeys(m, "n")
	if m.dangerWaiting(1) {
		t.Error("answered, it stops")
	}
}

func TestGoldenFailureGlitch(t *testing.T) {
	m, c := mainScreen(t)
	t0 := c.t
	m.fail("LOGIN FAILED  Dundry: wrong password, or not allowed")
	golden.Check(t, "failure-glitch", frames(t, m, c, t0, 0, glitchDur/2, glitchDur))
}

func TestGoldenUnreadPops(t *testing.T) {
	m, c, _ := newChatModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	selectConvo(t, m, "Public")
	tick(m, c, time.Second)
	t0 := c.t
	var from [6]byte
	copy(from[:], aliceKey[:6])
	send(m, c, 0, radio.Incoming{At: t0, From: from, Hops: -1, SentAt: t0, Text: "are you about?"})
	if !m.chatsAnimating() {
		t.Fatal("the new badge pops")
	}
	golden.Check(t, "unread-pops", frames(t, m, c, t0, 0, popDur/2, popDur*5/6, popDur))
}

func TestGoldenContactArrives(t *testing.T) {
	m, c, _ := newChatModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	typeKeys(m, "2j") // the new row lands above the selection, where it isn't drawn as selected
	tick(m, c, time.Second)
	t0 := c.t
	send(m, c, 0, radio.ContactUpdated{Contact: radio.Contact{Key: [32]byte{0xB0, 0xB1}, Name: "Zed", Type: radio.TypeChat, PathLen: -1, LastAdvert: t0.Add(-time.Hour)}})
	if !m.contactsAnimating() {
		t.Fatal("the new row comes in")
	}
	golden.Check(t, "contact-arrives", frames(t, m, c, t0, 0, arriveRowDur/5, arriveRowDur/2, arriveRowDur))
}

func TestGoldenPacketTypesIn(t *testing.T) {
	m, c := mainScreen(t)
	typeKeys(m, "4")
	tick(m, c, time.Second)
	t0 := c.t
	for i, hop := range []byte{0xA3, 0x5F} {
		payload := make([]byte, 101)
		payload[0] = byte(i + 1)
		send(m, c, 0, radio.Packet{At: t0, SNR: 6.5 - float32(4*i), RSSI: -90, Raw: rawPacket(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, []byte{hop}, payload)})
	}
	golden.Check(t, "packet-types-in", frames(t, m, c, t0, 0, 120*time.Millisecond, rowArrival))
}

func TestGoldenSettingsApplied(t *testing.T) {
	m, c := newTestModel(t, motion.Full, 100, 30)
	ct := radio.Contact{Key: [32]byte{7}, Name: "Dundry", Type: radio.TypeRepeater}
	sess := m.admin.Session(ct)
	t0 := c.t
	name := admin.Setting{Key: "name", Label: "Name"}
	power := admin.Setting{Key: "tx", Label: "Transmit power"}
	m.adm.applied, m.adm.appliedAt = []admin.Change{{Setting: name, From: "Dundry", To: "Dundry Hill"}, {Setting: power, From: "22", To: "20"}}, t0
	sess.Settings = map[string]admin.Value{"name": {Value: "Dundry", At: t0.Add(-time.Minute)}, "tx": {Value: "22", At: t0.Add(-time.Minute)}}
	var out []golden.Frame
	snap := func(d time.Duration) {
		c.t = t0.Add(d)
		m.Update(frameMsg(c.t))
		out = append(out, golden.Frame{Label: fmt.Sprint(d), Lines: m.appliedLines(sess, 90)})
	}
	snap(0) // both sent, waiting
	sess.Settings["name"] = admin.Value{Value: "Dundry Hill", At: t0.Add(200 * time.Millisecond)}
	snap(300 * time.Millisecond) // the first confirmed, typing in
	sess.Settings["tx"] = admin.Value{Value: "20", At: t0.Add(400 * time.Millisecond)}
	snap(time.Second) // both in
	golden.Check(t, "settings-applied", out)
}

func TestGoldenTraceDraws(t *testing.T) {
	h := newHarness(t)
	h.m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.keys("2")
	far, longest := "", 0
	for _, ct := range h.m.chat.Contacts() {
		if ct.PathLen > longest {
			far, longest = ct.Name, ct.PathLen
		}
	}
	if longest < 2 {
		t.Fatal("no contact two hops away in the simulator")
	}
	h.pick(far)
	h.keys("t")
	h.until("trace result", func() bool {
		run := h.m.contacts.traces[h.m.contacts.lastTrace[h.contact(far).Key]]
		return run != nil && run.result != nil
	})
	h.until("the world map", func() bool { h.keys("3"); return h.m.mapv.world != nil })
	h.keys("2")
	h.keys("M")
	h.screen()
	p := h.m.mapv.path
	if p == nil || !h.m.tracePlays() {
		t.Fatal("M opens the trace on the map, drawing itself in")
	}
	t0 := h.clk.t.Add(time.Second)
	p.playAt = t0
	m, c := h.m, h.clk
	hops := len(p.routes[p.sel].hops) + 1
	golden.Check(t, "trace-draws", frames(t, m, c, t0, 0, mapview.HopDur*3/2, time.Duration(hops)*mapview.HopDur+countDur))
}

func TestGoldenToast(t *testing.T) {
	m, c, _ := newChatModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	selectConvo(t, m, "Public")
	tick(m, c, time.Second)
	t0 := c.t
	var from [6]byte
	copy(from[:], aliceKey[:6])
	send(m, c, 0, radio.Incoming{At: t0, From: from, Hops: -1, SentAt: t0, Text: "are you about tonight?"})
	m.Update(notifyFlushMsg{})
	if len(m.toasts) != 1 || m.toasts[0].title != "Alice" || !m.toasting() {
		t.Fatalf("a notification shows as a toast: %+v", m.toasts)
	}
	golden.Check(t, "toast", frames(t, m, c, t0, 0, toastSlide/2, toastSlide, toastFor/2, toastFor-toastSlide/2, toastFor))
	if m.toasting() {
		t.Error("it's gone when its time is up")
	}
}
