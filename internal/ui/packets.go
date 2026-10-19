package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/verdantran/meshcore-tui/internal/chat"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/packets"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

const (
	packetKeep   = 2000
	hexReveal    = 260 * time.Millisecond
	rowArrival   = 700 * time.Millisecond
	rowCPS       = 300.0 // a new row types out inside rowArrival, even at 200 columns
	inspectorPad = 12    // rows kept for the detail pane
	packetCopies = 32    // copies of one packet kept; replays beyond are only counted
	firstCopies  = 16    // of those, the first heard always stay
)

// heardCopy is one reception of a packet; floods often arrive more than once by different routes.
type heardCopy struct {
	at   time.Time
	snr  float32
	rssi int8
	path []byte
	near bool // its last hop is a node we hear directly
}

// below reports whether c is a less likely route than o. Paths aren't signed, so anyone can make one up,
// but not the repeater next to us that passed it on, nor how strongly it came in.
func (c heardCopy) below(o heardCopy) bool {
	if c.near != o.near {
		return o.near
	}
	return c.snr < o.snr
}

// admit keeps a copy heard once the row is full, in place of the least likely of the later ones, so made-up
// paths sent first can't crowd out the real ones. The same path again is only counted.
func (r *packetRow) admit(c heardCopy) bool {
	worst := -1
	for i, k := range r.copies {
		if string(k.path) == string(c.path) {
			return false
		}
		if i >= firstCopies && (worst < 0 || k.below(r.copies[worst])) {
			worst = i
		}
	}
	if worst < 0 || !r.copies[worst].below(c) {
		return false
	}
	r.copies = append(slices.Delete(r.copies, worst, worst+1), c)
	return true
}

// nearHop reports whether a packet's last hop is a contact or heard node we reach without a repeater.
func (m *Model) nearHop(dec packets.Decoded) bool {
	if dec.HashSize == 0 || len(dec.Path) < dec.HashSize {
		return false
	}
	hop := dec.Path[len(dec.Path)-dec.HashSize:]
	if m.chat != nil {
		for _, c := range m.chat.Contacts() {
			if c.PathLen == 0 && bytes.HasPrefix(c.Key[:], hop) {
				return true
			}
		}
	}
	for _, h := range m.contacts.heard {
		if h.c.PathLen == 0 && bytes.HasPrefix(h.c.Key[:], hop) {
			return true
		}
	}
	return false
}

type packetRow struct {
	first  time.Time
	dec    packets.Decoded
	copies []heardCopy
	extra  int // copies heard beyond packetCopies
}

func (r *packetRow) heard() int { return len(r.copies) + r.extra }

var routeChoices = []string{"all", "flood", "direct"}
var snrChoices = []float32{-99, 0, 5}

type inspectorModel struct {
	rows      []*packetRow     // oldest first, only messages on open channels
	others    int              // everything else heard, counted but not kept
	seen      map[[8]byte]bool // every packet's hash, so repeats of hidden ones aren't counted twice
	seenOrder [][8]byte
	mesh      meshTally
	byHash    map[[8]byte]*packetRow
	sel       int // index in the filtered list, newest first; 0 follows live traffic
	field     int
	selAt     time.Time
	paused    bool
	held      int // packets that arrived while paused
	routeIdx  int
	snrIdx    int
	filter    lineEdit
	filtering bool
}

func newInspector() inspectorModel {
	return inspectorModel{byHash: map[[8]byte]*packetRow{}, seen: map[[8]byte]bool{}, mesh: newMeshTally()}
}

// shown reports whether a packet belongs on the Packets screen: an advert, which is broadcast to
// everyone, or a message or a message's signature on Public or a hashtag channel we've joined, which
// anyone may read. Everything else stays off it.
func (m *Model) shown(d packets.Decoded) bool {
	if d.Err == nil && d.Type == "ADVERT" {
		return true
	}
	// A signature on an open channel is as public as the message it signs, so it's shown too; other
	// datagrams aren't.
	signature := d.Type == "GRP_DATA" && d.Signature != nil
	if d.Err != nil || d.Type != "GRP_TXT" && !signature || !d.Decrypted || m.chat == nil {
		return false
	}
	ch, ok := m.packetChannel(d)
	return ok && chat.IsOpen(ch)
}

// packetChannel is the channel that decrypted a packet, found by slot and secret since names can repeat.
func (m *Model) packetChannel(d packets.Decoded) (radio.Channel, bool) {
	if !d.Decrypted || m.chat == nil {
		return radio.Channel{}, false
	}
	for _, ch := range m.chat.Channels() {
		if ch.Idx == d.ChannelIdx && ch.Secret == d.ChannelSecret {
			return ch, true
		}
	}
	return radio.Channel{}, false
}

func (m *Model) book() packets.Book {
	b := packets.Book{Self: m.ready.Self.PubKey}
	if m.chat != nil {
		b.Channels, b.Contacts = m.chat.Channels(), m.chat.Contacts()
	}
	return b
}

// heardPacket files a packet from the radio, grouping repeats of one already seen. Every packet feeds
// the mesh tally; only messages on open channels are kept to look at.
func (m *Model) heardPacket(p radio.Packet) {
	in := &m.inspector
	dec := packets.Decode(p.Raw, m.book())
	first := dec.Err != nil || !in.seen[dec.Hash]
	if first && dec.Err == nil {
		in.seen[dec.Hash] = true
		in.seenOrder = append(in.seenOrder, dec.Hash)
		if len(in.seenOrder) > 4*packetKeep {
			delete(in.seen, in.seenOrder[0])
			in.seenOrder = in.seenOrder[1:]
		}
	}
	shown := m.shown(dec)
	in.mesh.add(dec, p.SNR, first, shown, m.now)
	m.messagePacket(dec, p.Raw, p.SNR)
	if dec.Decrypted && dec.Type == "GRP_TXT" {
		m.heardEcho(dec) // every copy is another repeater passing it on
	}
	if !shown {
		if first {
			in.others++
		}
		return
	}
	row, again := in.byHash[dec.Hash]
	again = again && dec.Err == nil
	copy := heardCopy{at: m.now, snr: p.SNR, rssi: p.RSSI, path: dec.Path, near: m.nearHop(dec)}
	if again && len(row.copies) >= packetCopies {
		row.extra++
		if !row.admit(copy) {
			return
		}
	}
	m.ticker.add(hexBytes(p.Raw, dec.Hidden), m.now, m.w)
	m.mapHeard(dec, p.SNR)
	if again {
		if len(row.copies) < packetCopies {
			row.copies = append(row.copies, copy)
		}
		return
	}
	row = &packetRow{first: m.now, dec: dec, copies: []heardCopy{copy}}
	if in.paused {
		in.held++
	} else if in.sel > 0 && m.shows(row) {
		in.sel++ // keep the same packet selected while new ones arrive above it
	}
	in.rows = append(in.rows, row)
	if dec.Err == nil {
		in.byHash[dec.Hash] = row
	}
	if len(in.rows) > packetKeep {
		gone := in.rows[0]
		if gone.dec.Err == nil {
			delete(in.byHash, gone.dec.Hash)
		}
		in.rows = in.rows[1:]
	}
}

// shows reports whether a row passes the current filters.
func (m *Model) shows(r *packetRow) bool {
	in := &m.inspector
	switch {
	case in.routeIdx == 1 && !r.dec.Flood || in.routeIdx == 2 && r.dec.Flood:
		return false
	case r.bestSNR() < snrChoices[in.snrIdx]:
		return false
	}
	q := strings.ToLower(in.filter.text)
	return q == "" || strings.Contains(strings.ToLower(r.dec.Summary+" "+r.dec.Type+" "+hashes(r.dec.Path)), q)
}

// visiblePackets is the filtered list, newest first. While paused, rows that arrived since stay hidden.
func (m *Model) visiblePackets() []*packetRow {
	in := &m.inspector
	var out []*packetRow
	last := len(in.rows) - 1
	if in.paused {
		last -= in.held
	}
	for i := last; i >= 0; i-- {
		if m.shows(in.rows[i]) {
			out = append(out, in.rows[i])
		}
	}
	return out
}

func (r *packetRow) bestSNR() float32 {
	best := float32(-99)
	for _, c := range r.copies {
		best = max(best, c.snr)
	}
	return best
}

func (m *Model) selectedPacket() (*packetRow, bool) {
	list := m.visiblePackets()
	if len(list) == 0 {
		return nil, false
	}
	m.inspector.sel = min(max(m.inspector.sel, 0), len(list)-1)
	return list[m.inspector.sel], true
}

func (m *Model) inspectorKey(s string) (tea.Cmd, bool) {
	in := &m.inspector
	if in.filtering {
		filterKey(&in.filter, &in.filtering, s)
		in.sel = 0
		return nil, true
	}
	moved := func() { in.field, in.selAt = 0, m.now }
	switch s {
	case "up", "k":
		if in.sel > 0 {
			in.sel--
			moved()
		}
	case "down", "j":
		in.sel++
		moved()
	case "g", "home":
		in.sel = 0
		moved()
	case "G", "end":
		in.sel = max(len(m.visiblePackets())-1, 0)
		moved()
	case "[":
		in.field = max(in.field-1, 0)
	case "]":
		if r, ok := m.selectedPacket(); ok {
			in.field = min(in.field+1, len(r.dec.Fields)-1)
		}
	case "space":
		in.paused = !in.paused
		if !in.paused {
			in.held = 0
		}
	case "C":
		if len(in.rows) == 0 {
			m.fail("PACKETS  nothing to clear")
			return nil, true
		}
		m.ask(fmt.Sprintf("clear the %d packets heard?", len(in.rows)), "clear", func() tea.Cmd {
			mesh := m.inspector.mesh // clearing the list doesn't reset the Stats tally
			m.inspector = newInspector()
			m.inspector.mesh = mesh
			m.addLog(m.now, "PACKETS CLEARED", radio.ToneInfo)
			return nil
		})
	case "F":
		in.routeIdx, in.sel = (in.routeIdx+1)%len(routeChoices), 0
	case "+", "=":
		in.snrIdx, in.sel = min(in.snrIdx+1, len(snrChoices)-1), 0
	case "-":
		in.snrIdx, in.sel = max(in.snrIdx-1, 0), 0
	case "/":
		in.filtering, in.filter = true, lineEdit{}
	case "M", "enter":
		if r, ok := m.selectedPacket(); ok {
			m.showPacketPath(r)
		}
	case "y":
		if r, ok := m.selectedPacket(); ok {
			m.addLog(m.now, "COPIED  the packet's bytes, to the clipboard (hidden ones as ··)", radio.ToneOK)
			return m.copyText(r.dec.MaskedHex()), true
		}
	case "esc":
		if in.filter.text == "" {
			return nil, false
		}
		in.filter = lineEdit{}
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) inspectorAnimating() bool {
	in := &m.inspector
	if !m.visible(3) {
		return false
	}
	if motion.Running(in.selAt, hexReveal, m.now) {
		return true
	}
	for i := len(in.rows) - 1; i >= 0 && i >= len(in.rows)-5; i-- {
		if motion.Running(in.rows[i].first, rowArrival, m.now) {
			return true
		}
	}
	return false
}

func (m *Model) inspectorTitle() string {
	in := &m.inspector
	t := fmt.Sprintf("PACKETS %d", len(in.rows))
	if in.others > 0 {
		t += fmt.Sprintf(" · %d OTHERS NOT SHOWN", in.others)
	}
	var f []string
	if in.routeIdx > 0 {
		f = append(f, routeChoices[in.routeIdx])
	}
	if in.snrIdx > 0 {
		f = append(f, fmt.Sprintf("SNR ≥ %.0f", snrChoices[in.snrIdx]))
	}
	if len(f) > 0 {
		t += " · " + strings.Join(f, " · ")
	}
	if in.paused {
		t += fmt.Sprintf(" · PAUSED, %d NEW", in.held)
	}
	return t
}

var partColours = func(th theme.Theme) map[packets.Part]theme.RGB {
	return map[packets.Part]theme.RGB{packets.PartHeader: th.Magenta, packets.PartTransport: th.Violet, packets.PartPath: th.Cyan,
		packets.PartAddress: th.Yellow, packets.PartMeta: th.Muted, packets.PartBody: th.Text}
}

func (m *Model) renderInspector(w, rows int) []string {
	th, in := m.th, &m.inspector
	var out []string
	if in.filtering || in.filter.text != "" {
		out = append(out, th.Fg(th.Yellow, true).Render("/ ")+filterText(th, in.filter, in.filtering, w-16)+th.Fg(th.Dim, false).Render("  · esc clear"))
	}
	list := m.visiblePackets()
	if len(list) == 0 {
		msg := "Waiting for adverts, and messages on Public or a hashtag channel you've joined. Nothing else is shown here."
		if len(in.rows) > 0 {
			msg = "Nothing matches the filters."
		}
		return append(out, th.Fg(th.Dim, false).Render(msg))
	}
	detail := m.packetDetail(w, min(inspectorPad+4, rows/2))
	tableRows := max(rows-len(out)-len(detail)-1, 3)
	out = append(out, m.packetTable(list, w, tableRows)...)
	out = append(out, th.Fg(th.Line, false).Render(strings.Repeat("─", w)))
	return append(out, detail...)
}

// compactPackets is the width below which the list keeps only time, type, signal, copies and summary.
const compactPackets = 100

func (m *Model) typeColour(r *packetRow) theme.RGB {
	th := m.th
	switch {
	case r.dec.Err != nil:
		return th.Red
	case r.dec.Decrypted:
		return th.Green
	case r.dec.Type == "ADVERT":
		return th.Cyan
	}
	return th.Muted
}

func (m *Model) packetTable(list []*packetRow, w, rows int) []string {
	th, in, now := m.th, &m.inspector, m.now
	compact := w < compactPackets
	head := fmt.Sprintf("  %-8s %-6s %-8s %4s  %-14s %6s %5s %4s %3s  %s", "TIME", "ROUTE", "TYPE", "HOPS", "PATH", "SNR", "RSSI", "LEN", "×", "SUMMARY")
	if compact {
		head = fmt.Sprintf("  %-5s %-8s %6s %2s  %s", "TIME", "TYPE", "SNR", "×", "SUMMARY")
	}
	out := []string{th.Paint(th.Muted, true, truncate(head, w))}
	start := min(max(in.sel-(rows-1)/2, 0), max(len(list)-(rows-1), 0))
	for i := start; i < len(list) && len(out) < rows; i++ {
		r := list[i]
		c := r.copies[0]
		route := "DIRECT"
		if r.dec.Flood {
			route = "FLOOD"
		}
		times := ""
		if n := r.heard(); n > 99 {
			times = "99+" // the full count is in the detail pane
		} else if n > 1 {
			times = fmt.Sprintf("%d", n)
		}
		path := hashes(r.dec.Path)
		if path == "" {
			path = "—"
		}
		summary := r.dec.Summary
		if r.dec.Signature != nil {
			summary = m.signatureSummary(r.dec)
		}
		if compact {
			at, typ, snr, n := r.first.Local().Format("15:04")+" ", fmt.Sprintf("%-8s ", truncate(r.dec.Type, 8)), fmt.Sprintf("%+6.2f ", c.snr), fmt.Sprintf("%-4s", fmt.Sprintf("%2s", times))
			if i == in.sel {
				out = append(out, th.Paint(th.Magenta, true, "▌")+m.raised(fit(" "+at+typ+snr+n+summary, w-1)))
			} else {
				out = append(out, m.typedRow("  "+th.Paint(th.Dim, false, at)+th.Paint(m.typeColour(r), true, typ)+
					th.Paint(th.SNR(float64(c.snr)), false, snr)+th.Paint(th.Magenta, true, n)+
					th.Paint(th.Text, false, truncate(summary, max(w-28, 4))), r.first))
			}
			continue
		}
		if i == in.sel {
			line := fmt.Sprintf("%-8s %-6s %-8s %4d  %-14s %+6.2f %5d %4d %3s  %s", r.first.Local().Format("15:04:05"), route, truncate(r.dec.Type, 8),
				len(r.dec.Path), truncate(path, 14), c.snr, c.rssi, len(r.dec.Raw), times, summary)
			out = append(out, th.Paint(th.Magenta, true, "▌")+m.raised(fit(" "+line, w-1)))
			continue
		}
		bar := " "
		if m.level.Allows(motion.Flash) && motion.Running(r.first, rowArrival, now) {
			bar = th.Paint(th.Cyan.Lerp(th.Bg, motion.Progress(r.first, rowArrival, now)), true, "▌")
		}
		typeCol := m.typeColour(r)
		out = append(out, bar+m.typedRow(" "+th.Paint(th.Dim, false, r.first.Local().Format("15:04:05")+" ")+
			th.Paint(th.Muted, false, fmt.Sprintf("%-6s ", route))+th.Paint(typeCol, true, fmt.Sprintf("%-8s ", truncate(r.dec.Type, 8)))+
			th.Paint(th.Muted, false, fmt.Sprintf("%4d  ", len(r.dec.Path)))+th.Paint(th.Cyan, false, fmt.Sprintf("%-14s ", truncate(path, 14)))+
			th.Paint(th.SNR(float64(c.snr)), false, fmt.Sprintf("%+6.2f ", c.snr))+th.Paint(th.Dim, false, fmt.Sprintf("%5d %4d ", c.rssi, len(r.dec.Raw)))+
			th.Paint(th.Magenta, true, fmt.Sprintf("%3s  ", times))+th.Paint(th.Text, false, truncate(summary, max(w-70, 10))), r.first))
	}
	return out
}

// typedRow types a new packet's row out left to right, with a cursor at its head, before it takes its colours.
func (m *Model) typedRow(row string, at time.Time) string {
	th := m.th
	if !m.level.Allows(motion.Typing) {
		return row
	}
	typed, done := motion.Typewriter(ansi.Strip(row), at, rowCPS, m.now)
	if done {
		return row
	}
	return th.Fg(th.Cyan, false).Render(typed) + th.Fg(th.Text, true).Render("█")
}

func (m *Model) packetDetail(w, rows int) []string {
	th, in, now := m.th, &m.inspector, m.now
	r, ok := m.selectedPacket()
	if !ok {
		return nil
	}
	colours := partColours(th)
	sel := packets.Field{Start: -1, End: -1}
	if in.field < len(r.dec.Fields) {
		sel = r.dec.Fields[in.field]
	}
	// Left: decoded fields. Right: the hex dump, coloured by part, with the selected field highlighted.
	var left []string
	for i, f := range r.dec.Fields {
		name := th.Fg(colours[f.Part], true).Render(fmt.Sprintf("%-22s", f.Name))
		value := th.Fg(th.Text, false).Render(f.Value)
		if i == in.field {
			left = append(left, th.Fg(th.Magenta, true).Render("▸ ")+name+value)
			continue
		}
		left = append(left, "  "+name+value)
	}
	if n := r.heard(); n > 1 {
		var via []string
		for _, c := range r.copies {
			via = append(via, fmt.Sprintf("%s (%+.1f)", orFlood(c.path), c.snr))
		}
		head := fmt.Sprintf("heard %d times: ", n)
		if r.extra > 0 {
			head = fmt.Sprintf("heard %d times, %d kept: ", n, len(r.copies))
		}
		left = append(left, "", th.Fg(th.Magenta, true).Render(head)+th.Fg(th.Muted, false).Render(strings.Join(via, " · ")))
	}
	left = append(left, "", th.Fg(th.Dim, false).Render("enter map · [ ] fields · space pause · F route · + - signal · / filter · y copy · C clear"))

	shown := len(r.dec.Raw)
	if m.level.Allows(motion.Typing) && motion.Running(in.selAt, hexReveal, now) {
		shown = int(motion.Progress(in.selAt, hexReveal, now) * float64(len(r.dec.Raw)))
	}
	partAt := func(i int) packets.Part {
		for _, f := range r.dec.Fields {
			if i >= f.Start && i < f.End {
				return f.Part
			}
		}
		return packets.PartBody
	}
	var right []string
	for off := 0; off < len(r.dec.Raw); off += 16 {
		var b strings.Builder
		b.WriteString(th.Fg(th.Dim, false).Render(fmt.Sprintf("%04X ", off)))
		for i := off; i < off+16 && i < len(r.dec.Raw); i++ {
			if i >= shown {
				b.WriteString(th.Fg(th.Line, false).Render(" ··"))
				continue
			}
			cell := fmt.Sprintf("%02X", r.dec.Raw[i])
			if r.dec.Hidden(i) {
				cell = "··"
			}
			if i >= sel.Start && i < sel.End {
				b.WriteString(" " + th.Block(th.Bg, colours[partAt(i)]).Render(cell))
			} else {
				b.WriteString(" " + th.Fg(colours[partAt(i)], false).Render(cell))
			}
		}
		right = append(right, b.String())
	}
	leftW := max(w-52, 30)
	var out []string
	for i := 0; i < rows && (i < len(left) || i < len(right)); i++ {
		l, rr := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			rr = right[i]
		}
		out = append(out, fit(l, leftW)+"  "+rr)
	}
	return out
}

type packetJSON struct {
	Time      time.Time `json:"time"`
	SNR       float32   `json:"snr"`
	RSSI      int8      `json:"rssi"`
	Route     string    `json:"route"`
	Type      string    `json:"type"`
	Path      string    `json:"path,omitempty"`
	Hex       string    `json:"hex"`
	Summary   string    `json:"summary"`
	Channel   string    `json:"channel,omitempty"`
	Decrypted string    `json:"decrypted,omitempty"`
	Copies    int       `json:"heard"`
}

// exportPackets writes the visible packets, oldest first, one JSON object per line.
func (m *Model) exportPackets() error {
	list := m.visiblePackets()
	f, path, err := m.createExport("packets-" + m.now.Format("20060102-150405") + ".jsonl")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for i := len(list) - 1; i >= 0; i-- {
		r := list[i]
		j := packetJSON{Time: r.first, SNR: r.copies[0].snr, RSSI: r.copies[0].rssi, Route: r.dec.Route, Type: r.dec.Type,
			Path: hashes(r.dec.Path), Hex: r.dec.MaskedHex(), Summary: r.dec.Summary, Channel: r.dec.Channel, Copies: r.heard()}
		if r.dec.Decrypted {
			j.Decrypted = r.dec.Sender + ": " + r.dec.Text
		}
		if err := enc.Encode(j); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	m.addLog(m.now, fmt.Sprintf("EXPORTED  %d packets → %s", len(list), path), radio.ToneOK)
	return nil
}
