package ui

import (
	"encoding/json"
	"fmt"
	"meshcore-tui/internal/chat"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"meshcore-tui/internal/config"
	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/packets"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/theme"
)

const (
	packetKeep   = 2000
	hexReveal    = 260 * time.Millisecond
	rowArrival   = 700 * time.Millisecond
	inspectorPad = 12 // rows kept for the detail pane
)

// heardCopy is one reception of a packet; floods often arrive more than once by different routes.
type heardCopy struct {
	at   time.Time
	snr  float32
	rssi int8
	path []byte
}

type packetRow struct {
	first  time.Time
	dec    packets.Decoded
	copies []heardCopy
}

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
	filter    string
	filtering bool
}

func newInspector() inspectorModel {
	return inspectorModel{byHash: map[[8]byte]*packetRow{}, seen: map[[8]byte]bool{}, mesh: newMeshTally()}
}

// shown reports whether a packet belongs on the Packets screen: an advert, which is broadcast to
// everyone, or a message on Public or a hashtag channel we've joined, which anyone may read.
// Everything else stays off it; see SPEC §10.
func (m *Model) shown(d packets.Decoded) bool {
	if d.Err == nil && d.Type == "ADVERT" {
		return true
	}
	if d.Err != nil || d.Type != "GRP_TXT" || !d.Decrypted || m.chat == nil {
		return false
	}
	for _, ch := range m.chat.Channels() {
		if ch.Name == d.Channel {
			return chat.IsOpen(ch)
		}
	}
	return false
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
	in.mesh.add(dec, p.SNR, first, m.now)
	m.messagePacket(dec, p.Raw, p.SNR)
	if dec.Decrypted && dec.Type == "GRP_TXT" {
		m.heardEcho(dec.Channel, dec.Sender, dec.Text) // every copy is another repeater passing it on
	}
	if !m.shown(dec) {
		if first {
			in.others++
		}
		return
	}
	m.ticker.add(hexBytes(p.Raw, dec.Hidden), m.now, m.w)
	m.mapHeard(dec, p.SNR)
	copy := heardCopy{at: m.now, snr: p.SNR, rssi: p.RSSI, path: dec.Path}
	if dec.Err == nil {
		if row, ok := in.byHash[dec.Hash]; ok {
			row.copies = append(row.copies, copy)
			return
		}
	}
	row := &packetRow{first: m.now, dec: dec, copies: []heardCopy{copy}}
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
	q := strings.ToLower(in.filter)
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
		switch s {
		case "esc":
			in.filter, in.filtering = "", false
		case "enter":
			in.filtering = false
		case "backspace":
			in.filter = dropLast(in.filter)
		default:
			if len([]rune(s)) == 1 {
				in.filter += s
			}
		}
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
		mesh := in.mesh // clearing the list doesn't reset the Stats tally
		*in = newInspector()
		in.mesh = mesh
		m.addLog(m.now, "PACKETS CLEARED", radio.ToneInfo)
	case "R":
		in.routeIdx, in.sel = (in.routeIdx+1)%len(routeChoices), 0
	case "n":
		in.snrIdx, in.sel = (in.snrIdx+1)%len(snrChoices), 0
	case "/":
		in.filtering, in.filter = true, ""
	case "M":
		if r, ok := m.selectedPacket(); ok {
			m.showPacketPath(r)
		}
	case "y":
		if r, ok := m.selectedPacket(); ok {
			m.addLog(m.now, "COPIED  the packet's bytes, to the clipboard (hidden ones as ··)", radio.ToneOK)
			return tea.SetClipboard(r.dec.MaskedHex()), true
		}
	case "esc":
		if in.filter == "" {
			return nil, false
		}
		in.filter = ""
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
	if in.filtering || in.filter != "" {
		cursor := ""
		if in.filtering {
			cursor = th.Fg(th.Text, true).Render("█")
		}
		out = append(out, th.Fg(th.Yellow, true).Render("/ ")+th.Fg(th.Text, false).Render(in.filter)+cursor+th.Fg(th.Dim, false).Render("  · esc clears"))
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
	out := []string{th.Fg(th.Muted, true).Render(truncate(head, w))}
	start := min(max(in.sel-(rows-1)/2, 0), max(len(list)-(rows-1), 0))
	for i := start; i < len(list) && len(out) < rows; i++ {
		r := list[i]
		c := r.copies[0]
		route := "DIRECT"
		if r.dec.Flood {
			route = "FLOOD"
		}
		times := ""
		if n := len(r.copies); n > 1 {
			times = fmt.Sprintf("%d", n)
		}
		path := hashes(r.dec.Path)
		if path == "" {
			path = "—"
		}
		summary := r.dec.Summary
		if compact {
			at, typ, snr, n := r.first.Local().Format("15:04")+" ", fmt.Sprintf("%-8s ", truncate(r.dec.Type, 8)), fmt.Sprintf("%+6.2f ", c.snr), fmt.Sprintf("%2s  ", times)
			if i == in.sel {
				out = append(out, th.Fg(th.Magenta, true).Render("▌")+m.raised(fit(" "+at+typ+snr+n+summary, w-1)))
			} else {
				out = append(out, "  "+th.Fg(th.Dim, false).Render(at)+th.Fg(m.typeColour(r), true).Render(typ)+
					th.Fg(th.SNR(float64(c.snr)), false).Render(snr)+th.Fg(th.Magenta, true).Render(n)+
					th.Fg(th.Text, false).Render(truncate(summary, max(w-28, 4))))
			}
			continue
		}
		if i == in.sel {
			line := fmt.Sprintf("%-8s %-6s %-8s %4d  %-14s %+6.2f %5d %4d %3s  %s", r.first.Local().Format("15:04:05"), route, truncate(r.dec.Type, 8),
				len(r.dec.Path), truncate(path, 14), c.snr, c.rssi, len(r.dec.Raw), times, summary)
			out = append(out, th.Fg(th.Magenta, true).Render("▌")+m.raised(fit(" "+line, w-1)))
			continue
		}
		bar := " "
		if m.level.Allows(motion.Flash) && motion.Running(r.first, rowArrival, now) {
			bar = th.Fg(th.Cyan.Lerp(th.Bg, motion.Progress(r.first, rowArrival, now)), true).Render("▌")
		}
		typeCol := m.typeColour(r)
		out = append(out, bar+" "+th.Fg(th.Dim, false).Render(r.first.Local().Format("15:04:05")+" ")+
			th.Fg(th.Muted, false).Render(fmt.Sprintf("%-6s ", route))+th.Fg(typeCol, true).Render(fmt.Sprintf("%-8s ", truncate(r.dec.Type, 8)))+
			th.Fg(th.Muted, false).Render(fmt.Sprintf("%4d  ", len(r.dec.Path)))+th.Fg(th.Cyan, false).Render(fmt.Sprintf("%-14s ", truncate(path, 14)))+
			th.Fg(th.SNR(float64(c.snr)), false).Render(fmt.Sprintf("%+6.2f ", c.snr))+th.Fg(th.Dim, false).Render(fmt.Sprintf("%5d %4d ", c.rssi, len(r.dec.Raw)))+
			th.Fg(th.Magenta, true).Render(fmt.Sprintf("%3s  ", times))+th.Fg(th.Text, false).Render(truncate(summary, max(w-70, 10))))
	}
	return out
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
	if n := len(r.copies); n > 1 {
		var via []string
		for _, c := range r.copies {
			via = append(via, fmt.Sprintf("%s (%+.1f)", orFlood(c.path), c.snr))
		}
		left = append(left, "", th.Fg(th.Magenta, true).Render(fmt.Sprintf("heard %d times: ", n))+th.Fg(th.Muted, false).Render(strings.Join(via, " · ")))
	}
	left = append(left, "", th.Fg(th.Dim, false).Render("[ ] step through fields · space pause · R route · n signal · / filter · C clear"))

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
	dir := m.opt.ExportDir
	if dir == "" {
		dir = filepath.Join(config.DataDir(), "exports")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "packets-"+m.now.Format("20060102-150405")+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for i := len(list) - 1; i >= 0; i-- {
		r := list[i]
		j := packetJSON{Time: r.first, SNR: r.copies[0].snr, RSSI: r.copies[0].rssi, Route: r.dec.Route, Type: r.dec.Type,
			Path: hashes(r.dec.Path), Hex: r.dec.MaskedHex(), Summary: r.dec.Summary, Channel: r.dec.Channel, Copies: len(r.copies)}
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
