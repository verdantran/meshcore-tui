package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"meshcore-tui/internal/admin"
	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/store"
	"meshcore-tui/internal/theme"
)

const (
	healthReveal = 420 * time.Millisecond // charts draw in left to right when the pane opens
	historyLoad  = 8 * 24 * time.Hour     // a week on screen, plus a day for the first rates
)

// watchCheck is how often the watch looks for a node that's due a poll; tests shorten it.
var watchCheck = 20 * time.Second

func (m *Model) repeaterMode() bool { return m.adm.local }

// closeNode goes back to the node list, which a repeater on USB doesn't have.
func (m *Model) closeNode() {
	m.adm.picking = false
	if !m.adm.local {
		m.adm.opened = false
	}
}

func (m *Model) watched(key [32]byte) bool {
	if m.adm.local {
		return key == m.adm.open
	}
	return m.adminConfig().Watching(radio.KeyHex(key[:]))
}

// watchedNodes are the nodes the watch polls: the node itself on USB, or the watch list's contacts.
func (m *Model) watchedNodes() []radio.Contact {
	var out []radio.Contact
	for _, c := range m.allNodes() {
		if m.watched(c.Key) {
			out = append(out, c)
		}
	}
	return out
}

func (m *Model) watchEvery() time.Duration {
	c := m.adminConfig().Admin
	if m.adm.local {
		return time.Duration(max(c.LocalEvery, 10)) * time.Second
	}
	return time.Duration(max(c.WatchEvery, 1)) * time.Minute
}

// every is a poll interval as people say it: 10m, 90s.
func every(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func (m *Model) toggleWatch(ct radio.Contact) {
	cfg, k := m.adminConfig(), radio.KeyHex(ct.Key[:])
	on := !cfg.Watching(k)
	cfg.SetWatch(k, on)
	if m.opt.Config != nil {
		m.saveConfig()
	}
	if on {
		delete(m.adm.lastTry, ct.Key)
		m.addLog(m.now, fmt.Sprintf("WATCHING  %s: its status is polled every %s and kept", ct.Name, every(m.watchEvery())), radio.ToneOK)
		return
	}
	delete(m.adm.noPassword, ct.Key)
	m.addLog(m.now, "UNWATCHED  "+ct.Name+": polling stops; its history stays until cleared with C on Health", radio.ToneInfo)
}

// historyOf is a node's kept status, read from the store the first time it's wanted.
func (m *Model) historyOf(key [32]byte) []admin.StatusPoint {
	if h, ok := m.adm.kept[key]; ok {
		return h
	}
	var h []admin.StatusPoint
	if m.opt.Store != nil {
		rows, err := m.opt.Store.Status(key, m.now.Add(-historyLoad))
		if err != nil {
			m.addLog(m.now, "HISTORY  couldn't read it: "+err.Error(), radio.ToneWarn)
		}
		for _, r := range rows {
			if p, ok := admin.PointFrom(r.At, r.Room, r.Data); ok {
				h = append(h, p)
			}
		}
	}
	m.adm.kept[key] = h
	return h
}

// keepStatus adds a watched node's newest status to its history.
func (m *Model) keepStatus(sess *admin.Session, data []byte) {
	p, ok := sess.Latest()
	if !ok {
		return
	}
	h := append(m.historyOf(sess.Key), p)
	for len(h) > 0 && m.now.Sub(h[0].At) > historyLoad {
		h = h[1:]
	}
	m.adm.kept[sess.Key] = h
	if m.opt.Store == nil {
		return
	}
	if err := m.opt.Store.AddStatus(sess.Key, store.StatusRow{At: p.At, Room: sess.Type == radio.TypeRoom, Data: data}); err != nil && !m.adm.storeErr {
		m.adm.storeErr = true
		m.addLog(m.now, "HISTORY NOT SAVED  "+err.Error(), radio.ToneWarn)
	}
}

func (m *Model) clearHistory(ct radio.Contact) {
	if m.opt.Store != nil {
		if err := m.opt.Store.ClearStatus(ct.Key); err != nil {
			m.fail("HISTORY  couldn't clear it: " + err.Error())
			return
		}
	}
	m.adm.kept[ct.Key] = nil
	m.addLog(m.now, "HISTORY CLEARED  "+ct.Name, radio.ToneInfo)
}

// startWatch begins the watch for a new connection; ticks from an earlier one are ignored.
func (m *Model) startWatch() tea.Cmd {
	m.adm.watchGen++
	clear(m.adm.lastTry)
	return m.watchTick()
}

func (m *Model) watchTick() tea.Cmd {
	gen := m.adm.watchGen
	return tea.Tick(watchCheck, func(time.Time) tea.Msg { return watchTickMsg{gen} })
}

// pollWatched polls the watched node most overdue, if any is and the radio isn't busy with other requests.
func (m *Model) pollWatched() tea.Cmd {
	if m.linked != linkUp || m.radio == nil || m.admin.Queued() > 0 {
		return nil
	}
	if _, busy := m.admin.Running(); busy {
		return nil
	}
	var due radio.Contact
	most := time.Duration(-1)
	for _, c := range m.watchedNodes() {
		last := m.adm.lastTry[c.Key]
		if h := m.historyOf(c.Key); len(h) > 0 && h[len(h)-1].At.After(last) {
			last = h[len(h)-1].At
		}
		if over := m.now.Sub(last) - m.watchEvery(); over >= 0 && over > most {
			due, most = c, over
		}
	}
	if most < 0 {
		return nil
	}
	m.adm.lastTry[due.Key] = m.now
	sess := m.admin.Session(due)
	switch sess.State {
	case admin.LoggedIn:
		return m.backgroundJob(m.admin.RequestStatus(due, m.now))
	case admin.LoggingIn:
		return nil
	}
	if pw, ok := m.opt.Keychain.Get(due.Key); ok {
		delete(m.adm.noPassword, due.Key)
		return m.backgroundJob(m.admin.Login(due, pw, m.now))
	}
	m.adm.noPassword[due.Key] = true
	return nil
}

func (m *Model) backgroundJob(j *admin.Job) tea.Cmd {
	if j == nil {
		return nil
	}
	m.adm.background[j.ID] = true
	return m.startJob(j)
}

// watchMissed notes a watch poll that got no answer, without the alarm a request you made would raise.
func (m *Model) watchMissed(id int, why string) {
	delete(m.adm.background, id)
	j, _ := m.admin.Running()
	name := "a watched node"
	if j != nil && j.ID == id {
		name = m.contactName(j.Key)
	}
	m.addLog(m.now, fmt.Sprintf("WATCH  %s: %s; trying again in %s", name, why, every(m.watchEvery())), radio.ToneWarn)
}

func (m *Model) healthKey(s string, ct radio.Contact, sess *admin.Session) (tea.Cmd, bool) {
	a := &m.adm
	switch s {
	case "enter":
		a.picking, a.pick = true, -1
	case "r":
		a.week, a.paneAt = !a.week, m.now
	case "C":
		if len(m.historyOf(ct.Key)) == 0 {
			m.fail("HISTORY  there's none kept for " + ct.Name)
			return nil, true
		}
		m.askConfirm(confirmAction{title: "CLEAR THE HISTORY OF " + strings.ToUpper(ct.Name), clear: true,
			warning: "Every status poll kept for it is deleted from this computer. The node itself isn't touched."})
	case "u":
		if sess.State != admin.LoggedIn {
			m.fail("POLL  log in to " + ct.Name + " first, with enter")
			return nil, true
		}
		return m.startJob(m.admin.RequestStatus(ct, m.now)), true
	default:
		return nil, false
	}
	return nil, true
}

// healthPickKey moves the cursor on the health charts; the drawing keeps it inside them.
func (m *Model) healthPickKey(s string) bool {
	a := &m.adm
	switch s {
	case "left", "h":
		a.pick = max(a.pick-1, 0)
	case "right", "l":
		a.pick++
	case "home", "g":
		a.pick = 0
	case "end", "G":
		a.pick = 1 << 20
	case "enter", "esc":
		a.picking = false
	default:
		return false
	}
	return true
}

// healthPicked is the bucket under the cursor, or -1: placed on the newest poll at first, and kept on the charts.
func (m *Model) healthPicked(h admin.Health, buckets int) int {
	a := &m.adm
	if !a.picking {
		return -1
	}
	if a.pick < 0 {
		a.pick = 0
		for _, s := range h.Series {
			for i, v := range s.Values {
				if !math.IsNaN(v) && i > a.pick {
					a.pick = i
				}
			}
		}
	}
	a.pick = min(a.pick, buckets-1)
	return a.pick
}

// healthSeries measures a node over the chosen window; over USB there's no duplicate count to show.
func (m *Model) healthSeries(key [32]byte, buckets int) admin.Health {
	span := 24 * time.Hour
	if m.adm.week {
		span = 7 * span
	}
	h := admin.HealthOf(m.historyOf(key), m.now.Add(-span), m.now, buckets)
	if m.adm.local {
		var keep []admin.Series
		for _, s := range h.Series {
			if s.Name != "Duplicates" {
				keep = append(keep, s)
			}
		}
		h.Series = keep
	}
	return h
}

func (m *Model) healthLines(ct radio.Contact, sess *admin.Session, w, rows int) []string {
	th, a := m.th, &m.adm
	const labelW, valueW = 15, 12
	chartW := max(w-labelW-valueW-16, 10)
	h := m.healthSeries(ct.Key, chartW)

	state := th.Fg(th.Green, true).Render("◉ WATCHED") + th.Fg(th.Dim, false).Render(fmt.Sprintf(" · polled every %s", every(m.watchEvery())))
	if !m.watched(ct.Key) {
		state = th.Fg(th.Muted, true).Render("○ NOT WATCHED") + th.Fg(th.Dim, false).Render(fmt.Sprintf(" · w polls it every %s and keeps what it says", every(m.watchEvery())))
	}
	if hist := m.historyOf(ct.Key); len(hist) > 0 {
		state += th.Fg(th.Dim, false).Render(" · last poll " + ago(m.now.Sub(hist[len(hist)-1].At)))
	}
	day, week := " 24 HOURS ", " 7 DAYS "
	if a.week {
		day, week = th.Fg(th.Muted, false).Render(day), m.raised(week)
	} else {
		day, week = m.raised(day), th.Fg(th.Muted, false).Render(week)
	}
	switcher := day + " " + week
	out := []string{clip(state, w-visibleWidth(switcher)-1) + strings.Repeat(" ", max(w-visibleWidth(state)-visibleWidth(switcher), 1)) + switcher}
	if m.watched(ct.Key) && a.noPassword[ct.Key] && sess.State != admin.LoggedIn {
		out = append(out, th.Fg(th.Yellow, false).Render(clip("Its password isn't saved, so the watch can't log in to poll it. Log in with ctrl+s ticked to save it.", w)))
	}
	out = append(out, "")
	if len(h.Series) == 0 {
		window := "24 hours"
		if a.week {
			window = "7 days"
		}
		msg := "No polls kept from the last " + window + ". Rates need two polls in a row, so the first shows after the second poll."
		if !m.watched(ct.Key) {
			msg = "Nothing kept for it yet. Watch it with w, and each poll is kept here, even after you quit."
		}
		return append(out, th.Fg(th.Dim, false).Render(clip(msg, w)))
	}

	tall := min(max((rows-len(out)-3)/len(h.Series), 1), 3)
	reveal := 1.0
	if m.level.Allows(motion.Wipe) {
		reveal = motion.EaseOut(motion.Progress(a.paneAt, healthReveal, m.now))
	}
	pick := m.healthPicked(h, chartW)
	for _, s := range h.Series {
		if len(out)+tall > rows-3 {
			break
		}
		colour := func(v float64) theme.RGB { return m.gradeColour(m.healthGrade(ct.Key, s.Name, v)) }
		lo, hi := seriesRange(s)
		chart := m.lineChart(s.Values, lo, hi, chartW, tall, colour, reveal, pick)
		shown := s.Latest
		if pick >= 0 {
			shown = math.NaN()
			if pick < len(s.Values) {
				shown = s.Values[pick]
			}
		}
		for r, line := range chart {
			left := strings.Repeat(" ", labelW+valueW)
			right := ""
			if r == len(chart)-1 {
				left = th.Fg(th.Muted, true).Render(fmt.Sprintf("%-*s", labelW, strings.ToUpper(s.Name))) +
					th.Fg(colour(shown), true).Render(fmt.Sprintf("%-*s", valueW, m.healthMeasure(ct.Key, shown, s.Unit)))
				right = th.Fg(th.Dim, false).Render("  avg " + m.healthMeasure(ct.Key, s.Avg, s.Unit))
			}
			out = append(out, clip(left+line+right, w))
		}
	}
	out = append(out, strings.Repeat(" ", labelW+valueW)+th.Fg(th.Dim, false).Render(axis(chartW, a.week)))
	if pick >= 0 {
		at := h.From.Add(time.Duration((float64(pick) + 0.5) * float64(h.To.Sub(h.From)) / float64(chartW)))
		when := at.Format("15:04")
		if a.week {
			when = at.Format("Mon 15:04")
		}
		when += " · " + ago(m.now.Sub(at))
		mark := th.Fg(th.Text, true).Render("▲ " + when)
		lead := labelW + valueW + pick
		if lead+visibleWidth(mark) > w {
			mark, lead = th.Fg(th.Text, true).Render(when+" ▲"), lead-visibleWidth(when)-1
		}
		out = append(out, clip(strings.Repeat(" ", max(lead, 0))+mark, w))
	}
	note := plural(h.Polls, "poll") + " in this window"
	if pick >= 0 {
		note = "← → step through time · enter or esc stops"
	}
	switch h.Reboots {
	case 0:
	case 1:
		note += " · rebooted once"
	default:
		note += fmt.Sprintf(" · rebooted %d times", h.Reboots)
	}
	return append(out, th.Fg(th.Dim, false).Render(clip(note, w)))
}

// seriesRange scales rates and percentages from zero; levels like battery and noise use their own spread.
func seriesRange(s admin.Series) (float64, float64) {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range s.Values {
		if !math.IsNaN(v) {
			lo, hi = min(lo, v), max(hi, v)
		}
	}
	if s.Unit == "/h" || s.Unit == "%" {
		lo = 0
	}
	if hi-lo < 1e-9 {
		lo, hi = lo-1, hi+1
	}
	return lo, hi
}

// healthMeasure is measure with a battery's percentage beside its voltage.
func (m *Model) healthMeasure(node [32]byte, v float64, unit string) string {
	if unit != "V" || math.IsNaN(v) || m.chemistry(node) == radio.Wired {
		return measure(v, unit)
	}
	return fmt.Sprintf("%s %d%%", measure(v, unit), radio.Battery{MilliVolts: uint16(math.Round(v * 1000))}.PercentOf(m.chemistry(node)))
}

func measure(v float64, unit string) string {
	if math.IsNaN(v) {
		return "—"
	}
	switch unit {
	case "/h":
		return commas(int(math.Round(v))) + "/h"
	case "%":
		if v < 10 {
			return fmt.Sprintf("%.1f%%", v)
		}
		return fmt.Sprintf("%.0f%%", v)
	case "V":
		return fmt.Sprintf("%.2f V", v)
	}
	return fmt.Sprintf("%.0f %s", v, unit)
}

// chart draws values as block columns rows tall; gaps show as dots and only the revealed part is drawn.
func (m *Model) chart(vals []float64, lo, hi float64, w, rows int, colour theme.RGB, reveal float64) []string {
	th := m.th
	levels := rows * 8
	shown := int(reveal * float64(w))
	lines := make([]strings.Builder, rows)
	last := -1
	for i, v := range vals {
		if !math.IsNaN(v) {
			last = i
		}
	}
	for i := range w {
		v := math.NaN()
		if i < len(vals) {
			v = vals[i]
		}
		for r := range rows {
			cell := " "
			switch {
			case i >= shown:
			case math.IsNaN(v):
				if r == rows-1 {
					cell = th.Fg(th.Line, false).Render("·")
				}
			default:
				level := int((v-lo)/(hi-lo)*float64(levels-1)+0.5) + 1
				part := level - (rows-1-r)*8
				style := th.Fg(colour, false)
				if i == last {
					style = th.Fg(th.Text, true)
				}
				switch {
				case part >= 8:
					cell = style.Render("█")
				case part > 0:
					cell = style.Render(string(sparkRunes[part-1]))
				}
			}
			lines[r].WriteString(cell)
		}
	}
	out := make([]string, rows)
	for r := range lines {
		out[r] = lines[r].String()
	}
	return out
}

// brailleBit is the dot at (x, y) within a braille cell, two dots wide and four tall.
var brailleBit = [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

// lineChart draws values as a braille line rows tall, each cell in its value's colour; gaps show as
// dots, the newest value is bright, and only the revealed part is drawn.
func (m *Model) lineChart(vals []float64, lo, hi float64, w, rows int, colour func(float64) theme.RGB, reveal float64, cursor int) []string {
	th := m.th
	dotsH := rows * 4
	at := func(v float64) int {
		return dotsH - 1 - int(math.Round((v-lo)/(hi-lo)*float64(dotsH-1)))
	}
	val := func(i int) float64 {
		if i < 0 || i >= len(vals) {
			return math.NaN()
		}
		return vals[i]
	}
	shown := int(reveal * float64(w))
	cells := make([][]rune, rows)
	for r := range cells {
		cells[r] = make([]rune, w)
	}
	dot := func(x, y int) {
		if x >= 0 && x < 2*shown && y >= 0 && y < dotsH {
			cells[y/4][x/2] |= brailleBit[y%4][x%2]
		}
	}
	last := -1
	for i := range min(w, len(vals)) {
		v := val(i)
		if math.IsNaN(v) {
			continue
		}
		last = i
		y0, next := at(v), val(i+1)
		if math.IsNaN(next) {
			dot(2*i, y0)
			dot(2*i+1, y0)
			continue
		}
		// A step to the next value, drawn as a line from this cell's left dot to the next cell's.
		y1 := at(next)
		steps := max(abs(y1-y0), 2)
		for s := 0; s <= steps; s++ {
			dot(2*i+int(math.Round(float64(2*s)/float64(steps))), y0+int(math.Round(float64((y1-y0)*s)/float64(steps))))
		}
	}
	out := make([]string, rows)
	for r := range rows {
		var b strings.Builder
		for i := range w {
			v := val(i)
			at := func(st lipgloss.Style) lipgloss.Style {
				if i != cursor {
					return st
				}
				if th.Mono {
					return st.Reverse(true)
				}
				return st.Background(th.Line.Color())
			}
			switch {
			case i >= shown:
				b.WriteString(" ")
			case cells[r][i] != 0:
				style := th.Fg(colour(v), false)
				if i == last || i == cursor {
					style = th.Fg(th.Text, true)
				}
				b.WriteString(at(style).Render(string(0x2800 + cells[r][i])))
			case math.IsNaN(v) && r == rows-1:
				b.WriteString(at(th.Fg(th.Line, false)).Render("·"))
			default:
				b.WriteString(at(lipgloss.NewStyle()).Render(" "))
			}
		}
		out[r] = b.String()
	}
	return out
}

func abs(n int) int { return max(n, -n) }

// axis labels a chart's time span under it.
func axis(w int, week bool) string {
	marks := []string{"-24h", "-18h", "-12h", "-6h", "now"}
	if week {
		marks = []string{"-7d", "-5d", "-3d", "-1d", "now"}
	}
	b := []rune(strings.Repeat(" ", w))
	for i, mk := range marks {
		at := i * (w - 1) / (len(marks) - 1)
		if week && i > 0 && i < len(marks)-1 {
			at = (7 - [...]int{7, 5, 3, 1}[i]) * (w - 1) / 7
		}
		at = min(max(at-len(mk)/2, 0), w-len(mk))
		copy(b[at:], []rune(mk))
	}
	return string(b)
}

// trend is a small chart of a watched node's received rate over the last day, for the node list.
func (m *Model) trend(key [32]byte, w int) string {
	if len(m.historyOf(key)) == 0 {
		return strings.Repeat(" ", w)
	}
	h := admin.HealthOf(m.historyOf(key), m.now.Add(-24*time.Hour), m.now, w)
	for _, s := range h.Series {
		if s.Name == "Received" {
			lo, hi := seriesRange(s)
			return m.chart(s.Values, lo, hi, w, 1, m.th.Cyan, 1)[0]
		}
	}
	return strings.Repeat(" ", w)
}

// packetLogLines sum up the log read over USB.
func (m *Model) packetLogLines(sess *admin.Session, w, rows int) []string {
	th := m.th
	pl := sess.PacketLog
	label := func(s string) string { return th.Fg(th.Muted, true).Render(fmt.Sprintf("%-10s", s)) }
	val := func(s string) string { return th.Fg(th.Text, true).Render(s) }
	dim := func(s string) string { return th.Fg(th.Dim, false).Render(s) }
	if len(pl.Entries) == 0 {
		return []string{dim(fmt.Sprintf("The log is empty. Start logging with s; packets the node hears from then on are kept. Read %s.", ago(m.now.Sub(sess.PacketLogAt))))}
	}
	s := pl.Summary()
	span := s.Last.Sub(s.First)
	out := []string{
		clip(label("LOG")+val(commas(len(pl.Entries)))+dim(" entries · "+s.First.Format("02 Jan 15:04")+" to "+s.Last.Format("02 Jan 15:04")+
			" UTC ("+spanText(span)+") · read "+ago(m.now.Sub(sess.PacketLogAt))), w),
		clip(label("RECEIVED")+val(commas(s.RX))+dim(fmt.Sprintf("  flood %s · direct %s", commas(s.Flood), commas(s.Direct)))+
			"   "+label("SENT")+val(commas(s.TX))+"   "+label("FAILED")+th.Fg(map[bool]theme.RGB{true: th.Red, false: th.Text}[s.Fail > 0], true).Render(commas(s.Fail)), w),
	}
	chartW := max(min(w-12, 72), 10)
	rx, tx := pl.Buckets(s.First, s.Last, chartW)
	peak := 1.0
	for i := range rx {
		peak = max(peak, float64(rx[i]), float64(tx[i]))
	}
	floats := func(n []int) []float64 {
		f := make([]float64, len(n))
		for i, v := range n {
			f[i] = float64(v)
		}
		return f
	}
	reveal := 1.0
	if m.level.Allows(motion.Wipe) {
		reveal = motion.EaseOut(motion.Progress(m.adm.paneAt, healthReveal, m.now))
	}
	out = append(out, "",
		label("RX")+m.chart(floats(rx), 0, peak, chartW, 1, th.Cyan, reveal)[0],
		label("TX")+m.chart(floats(tx), 0, peak, chartW, 1, th.Magenta, reveal)[0],
		label("")+dim(fit(s.First.Format("15:04"), chartW-5)+s.Last.Format("15:04")), "")

	barW := max(min(w/3, 30), 6)
	most := 1
	for _, t := range s.Types {
		most = max(most, t.RX+t.TX)
	}
	out = append(out, th.Fg(th.Muted, true).Render(fmt.Sprintf("%-12s %-*s %6s %6s", "BY TYPE", barW, "", "RX", "TX")))
	for _, t := range s.Types {
		if len(out) >= rows-4 {
			break
		}
		n := (t.RX + t.TX) * barW / most
		bar := th.Fg(th.Cyan, false).Render(strings.Repeat("█", t.RX*barW/most)) + th.Fg(th.Magenta, false).Render(strings.Repeat("█", max(n-t.RX*barW/most, 0)))
		out = append(out, clip(th.Fg(th.Text, false).Render(fmt.Sprintf("%-12s ", t.Name))+bar+strings.Repeat(" ", barW-n)+
			val(fmt.Sprintf(" %6s %6s", commas(t.RX), commas(t.TX))), w))
	}
	if s.Signals > 0 && len(out) < rows-2 {
		var bands []string
		for i, n := range s.SNR {
			bands = append(bands, dim(admin.SNRBands[i]+" ")+val(commas(n)))
		}
		out = append(out, "", clip(label("SIGNAL")+dim("avg ")+th.Fg(th.SNR(s.SNRAvg), true).Render(fmt.Sprintf("SNR %.1f", s.SNRAvg))+
			dim(fmt.Sprintf(" · RSSI %.0f   ", s.RSSIAvg))+strings.Join(bands, dim(" · ")), w))
	}
	if len(s.Senders) > 0 && len(out) < rows-1 {
		var top []string
		for _, h := range s.Senders[:min(len(s.Senders), 6)] {
			top = append(top, val(fmt.Sprintf("%02X", h.Hash))+dim(fmt.Sprintf(" ×%d", h.Count)))
		}
		out = append(out, clip(label("SENDERS")+strings.Join(top, dim(" · "))+dim("   by first key byte, for messages and requests"), w))
	}
	return out
}

func spanText(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
