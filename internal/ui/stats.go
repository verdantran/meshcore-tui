package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/packets"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/theme"
)

const (
	statsEvery = 5 * time.Second
	statsKeep  = 120 // ten minutes of samples
)

type statSample struct {
	at time.Time
	s  radio.Stats
}

type radioStatsModel struct {
	hist    []statSample
	polling bool
	err     error
}

type statsMsg struct {
	s   radio.Stats
	err error
}
type statsTickMsg struct{}

// pollStats reads the counters while the Stats screen is showing, and stops when it isn't.
func (m *Model) pollStats() tea.Cmd {
	d := m.dev()
	if d == nil {
		return nil
	}
	m.rstats.polling = true
	return m.radioCall(func(ctx context.Context) tea.Msg { s, err := d.Stats(ctx); return statsMsg{s, err} })
}

func (m *Model) statsMsg(msg tea.Msg) tea.Cmd {
	rs := &m.rstats
	switch msg := msg.(type) {
	case statsMsg:
		rs.err = msg.err
		if msg.err == nil {
			rs.hist = append(rs.hist, statSample{m.now, msg.s})
			if len(rs.hist) > statsKeep {
				rs.hist = rs.hist[len(rs.hist)-statsKeep:]
			}
		}
		if !m.visible(6) {
			rs.polling = false
			return nil
		}
		return tea.Tick(statsEvery, func(time.Time) tea.Msg { return statsTickMsg{} })
	case statsTickMsg:
		if !m.visible(6) {
			rs.polling = false
			return nil
		}
		return m.pollStats()
	}
	return nil
}

// rate is how fast a counter grew between the last two samples, per minute.
func rate(hist []statSample, f func(radio.Stats) uint32) (float64, bool) {
	if len(hist) < 2 {
		return 0, false
	}
	a, b := hist[len(hist)-2], hist[len(hist)-1]
	dt := b.at.Sub(a.at).Minutes()
	if dt <= 0 || f(b.s) < f(a.s) {
		return 0, false
	}
	return float64(f(b.s)-f(a.s)) / dt, true
}

// series turns each pair of samples into a value, for sparklines.
func series(hist []statSample, f func(a, b statSample) float64) []float64 {
	var out []float64
	for i := 1; i < len(hist); i++ {
		out = append(out, f(hist[i-1], hist[i]))
	}
	return out
}

func busy(a, b statSample) float64 {
	dt := b.at.Sub(a.at)
	if dt <= 0 || b.s.RxAir < a.s.RxAir {
		return 0
	}
	return float64(b.s.RxAir-a.s.RxAir+b.s.TxAir-a.s.TxAir) / float64(dt) * 100
}

func (m *Model) statsTitle() string {
	if len(m.rstats.hist) == 0 {
		return "STATS"
	}
	return fmt.Sprintf("STATS · every %s · %d samples", statsEvery, len(m.rstats.hist))
}

func (m *Model) renderStats(w, rows int) []string {
	th := m.th
	left := m.radioStatLines(w)
	right := m.meshLines(w)
	if w >= 110 {
		half := w/2 - 1
		left, right = m.radioStatLines(half), m.meshLines(w-half-2)
		var out []string
		for i := 0; i < max(len(left), len(right)) && i < rows; i++ {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			out = append(out, fit(l, half)+th.Fg(th.Line, false).Render("│ ")+r)
		}
		return out
	}
	out := append(left, "")
	out = append(out, right...)
	if len(out) > rows {
		out = out[:rows]
	}
	return out
}

func (m *Model) radioStatLines(w int) []string {
	th, rs := m.th, &m.rstats
	head := th.Fg(th.Muted, true).Render("THIS RADIO")
	if m.dev() == nil {
		return []string{head, th.Fg(th.Dim, false).Render("A repeater console's counters are on the Admin screen's Status pane.")}
	}
	if len(rs.hist) == 0 {
		if rs.err != nil {
			return []string{head, th.Fg(th.Red, false).Render("couldn't read the counters: " + radio.Explain(rs.err))}
		}
		return []string{head, m.waiting("reading the counters…")}
	}
	last := rs.hist[len(rs.hist)-1]
	s := last.s
	label := func(t string) string { return th.Fg(th.Dim, false).Render(t + " ") }
	val := func(t string) string { return th.Fg(th.Text, true).Render(t) }
	sparkW := min(30, max(w/3, 8))
	var batt, noise, rx []float64
	for _, h := range rs.hist {
		batt = append(batt, float64(h.s.Battery.MilliVolts))
		noise = append(noise, float64(h.s.NoiseFloor))
	}
	rx = series(rs.hist, func(a, b statSample) float64 {
		if dt := b.at.Sub(a.at).Minutes(); dt > 0 && b.s.Recv >= a.s.Recv {
			return float64(b.s.Recv-a.s.Recv) / dt
		}
		return 0
	})
	util := series(rs.hist, busy)
	perMin := func(f func(radio.Stats) uint32) string {
		if r, ok := rate(rs.hist, f); ok {
			return th.Fg(th.Cyan, false).Render(fmt.Sprintf(" +%.0f/min", r))
		}
		return ""
	}
	up := max(s.Uptime.Seconds(), 1)
	out := []string{head,
		label("Uptime") + val(uptime(uint32(s.Uptime.Seconds()))) + label("   Queue") + val(fmt.Sprint(s.Queue)),
		label("Battery") + m.batteryText(s.Battery, true) + " " + m.spark(batt, sparkW, last.at),
		label("Noise floor") + val(fmt.Sprintf("%d dBm ", s.NoiseFloor)) + m.spark(noise, sparkW, last.at),
		label("Last packet") + val(fmt.Sprintf("RSSI %d · SNR ", s.LastRSSI)) + th.Fg(th.SNR(s.LastSNR), true).Render(fmt.Sprintf("%+.1f", s.LastSNR)),
		label("Airtime") + val(fmt.Sprintf("tx %.1f%% · rx %.1f%% of uptime", s.TxAir.Seconds()/up*100, s.RxAir.Seconds()/up*100)),
	}
	if len(util) > 0 {
		out = append(out, label("Channel busy")+val(fmt.Sprintf("%.0f%% ", util[len(util)-1]))+m.spark(util, sparkW, last.at))
	}
	out = append(out, "",
		label("Received")+val(commas(int(s.Recv)))+perMin(func(s radio.Stats) uint32 { return s.Recv })+" "+m.spark(rx, sparkW/2, last.at),
		label("  flood")+val(commas(int(s.RecvFlood)))+label("  direct")+val(commas(int(s.RecvDirect))),
		label("Sent")+val(commas(int(s.Sent)))+perMin(func(s radio.Stats) uint32 { return s.Sent }),
		label("  flood")+val(commas(int(s.SentFlood)))+label("  direct")+val(commas(int(s.SentDirect))),
	)
	errs := th.Fg(th.Text, true)
	if s.RecvErrors > 0 {
		errs = th.Fg(th.Yellow, true)
	}
	errLine := label("Receive errors") + errs.Render(commas(int(s.RecvErrors)))
	if s.ErrFlags != 0 {
		errLine += th.Fg(th.Red, true).Render(fmt.Sprintf("   error flags %#04x", s.ErrFlags))
	}
	out = append(out, errLine)
	for i := range out {
		out[i] = clip(out[i], w)
	}
	return out
}

// meshTally counts routing details from every packet heard, ours or not: who's advertising, how far
// packets travel, which repeaters carry them, and how strong they are. No one's messages.
type meshTally struct {
	packets  int
	adverts  map[time.Time]map[string]bool // by the hour they were heard
	hops     map[int]int
	hashes   map[byte]int
	snr      [6]int // below -10, -10..-5, -5..0, 0..5, 5..10, 10 and up
	channels map[string]int
}

func newMeshTally() meshTally {
	return meshTally{adverts: map[time.Time]map[string]bool{}, hops: map[int]int{}, hashes: map[byte]int{}, channels: map[string]int{}}
}

// add counts one copy of a packet; first is false for repeats, which only add their signal.
func (t *meshTally) add(d packets.Decoded, snr float32, first bool, now time.Time) {
	t.snr[min(max(int(snr+15)/5, 0), 5)]++
	if !first {
		return
	}
	t.packets++
	if d.Type == "ADVERT" && d.Name != "" {
		h := now.Truncate(time.Hour)
		if t.adverts[h] == nil {
			t.adverts[h] = map[string]bool{}
			delete(t.adverts, h.Add(-24*time.Hour))
		}
		t.adverts[h][d.Name] = true
	}
	if d.Err == nil && d.Flood {
		size := max(d.HashSize, 1)
		t.hops[min(len(d.Path)/size, 7)]++
		for i := 0; i+size <= len(d.Path); i += size {
			t.hashes[d.Path[i]]++
		}
	}
	if d.Type == "GRP_TXT" {
		name := d.Channel
		if name == "" {
			name = "(unknown channel)"
		}
		t.channels[name]++
	}
}

// meshLines summarise the packets heard this session.
func (m *Model) meshLines(w int) []string {
	th, now := m.th, m.now
	t := &m.inspector.mesh
	out := []string{th.Fg(th.Muted, true).Render("THE MESH, FROM WHAT WE'VE HEARD") + th.Fg(th.Dim, false).Render(fmt.Sprintf("  %d packets", t.packets))}
	if t.packets == 0 {
		return append(out, th.Fg(th.Dim, false).Render("Nothing heard yet."))
	}
	barW := max(min(w-28, 30), 4)
	const hours = 8
	seen := make([]map[string]bool, hours)
	for i := range seen {
		seen[hours-1-i] = t.adverts[now.Truncate(time.Hour).Add(-time.Duration(i)*time.Hour)]
	}
	hops, hashes, snr, channels := t.hops, t.hashes, t.snr, t.channels

	var perHour []float64
	for _, s := range seen {
		perHour = append(perHour, float64(len(s)))
	}
	out = append(out, "", th.Fg(th.Text, true).Render("Nodes advertising per hour")+th.Fg(th.Dim, false).Render(fmt.Sprintf("  last %d hours", hours)),
		"  "+m.spark(perHour, hours, now)+th.Fg(th.Dim, false).Render(fmt.Sprintf("  %d this hour", len(seen[hours-1]))))

	out = append(out, "", th.Fg(th.Text, true).Render("Busiest repeaters")+th.Fg(th.Dim, false).Render("  by packets carried"))
	type count struct {
		hash byte
		n    int
	}
	var busiest []count
	for h, n := range hashes {
		busiest = append(busiest, count{h, n})
	}
	sort.Slice(busiest, func(i, j int) bool {
		return busiest[i].n > busiest[j].n || busiest[i].n == busiest[j].n && busiest[i].hash < busiest[j].hash
	})
	if len(busiest) == 0 {
		out = append(out, th.Fg(th.Dim, false).Render("  no repeated packets yet"))
	}
	for i, c := range busiest {
		if i == 5 {
			break
		}
		out = append(out, m.barLine(fmt.Sprintf("%02X %s", c.hash, m.hashName(c.hash)), c.n, busiest[0].n, barW, th.Cyan))
	}

	out = append(out, "", th.Fg(th.Text, true).Render("Hops travelled")+th.Fg(th.Dim, false).Render("  flood packets"))
	most := 0
	for _, n := range hops {
		most = max(most, n)
	}
	for h := 0; h <= 7; h++ {
		if hops[h] == 0 {
			continue
		}
		label := fmt.Sprintf("%d hops", h)
		switch h {
		case 1:
			label = "1 hop"
		case 7:
			label = "7+ hops"
		}
		out = append(out, m.barLine(label, hops[h], most, barW, th.Violet))
	}

	out = append(out, "", th.Fg(th.Text, true).Render("Signal")+th.Fg(th.Dim, false).Render("  SNR of every copy heard"))
	most = 0
	for _, n := range snr {
		most = max(most, n)
	}
	for i, n := range snr {
		lo := i*5 - 15
		label := fmt.Sprintf("%+d to %+d dB", lo, lo+5)
		switch i {
		case 0:
			label = "below -10 dB"
		case 5:
			label = "+10 dB and up"
		}
		out = append(out, m.barLine(label, n, most, barW, th.SNR(float64(lo+2))))
	}

	if len(channels) > 0 {
		out = append(out, "", th.Fg(th.Text, true).Render("Channel messages"))
		var names []string
		most = 0
		for n, c := range channels {
			names = append(names, n)
			most = max(most, c)
		}
		sort.Slice(names, func(i, j int) bool {
			return channels[names[i]] > channels[names[j]] || channels[names[i]] == channels[names[j]] && names[i] < names[j]
		})
		for _, n := range names {
			out = append(out, m.barLine(n, channels[n], most, barW, th.Magenta))
		}
	}
	for i := range out {
		out[i] = clip(out[i], w)
	}
	return out
}

// hashName names the repeaters a path hash could be; several share each byte.
func (m *Model) hashName(h byte) string {
	if m.chat == nil {
		return ""
	}
	var names []string
	for _, c := range m.chat.Contacts() {
		if c.Type == radio.TypeRepeater && c.Key[0] == h {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, " or ")
}

func (m *Model) barLine(label string, n, most, w int, colour theme.RGB) string {
	th := m.th
	filled := 0
	if most > 0 {
		filled = max(n*w/most, 1)
	}
	if n == 0 {
		filled = 0
	}
	if m.level.Allows(motion.CountUp) && motion.Running(m.tabAt, countDur, m.now) {
		filled = int(float64(filled) * motion.EaseOut(motion.Progress(m.tabAt, countDur, m.now)))
	}
	return th.Fg(th.Dim, false).Render("  "+col(label, 20)+" ") + th.Fg(colour, false).Render(strings.Repeat("█", filled)) +
		th.Fg(th.Text, false).Render(fmt.Sprintf(" %d", n))
}
