package ui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"rsc.io/qr"

	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

func (m *Model) renderContacts(w, rows int) []string {
	c := &m.contacts
	if m.chat == nil || m.chat.Device() == "" {
		return []string{m.th.Fg(m.th.Dim, false).Render("Contacts appear once the radio has synced.")}
	}
	switch c.overlay {
	case shareOverlay:
		return m.shareLines(w, rows)
	case editorOverlay:
		return m.editorLines(w, rows)
	case importOverlay:
		return m.importLines(w)
	case importConfirmOverlay:
		return m.importAskLines(w)
	case placeOverlay:
		return m.placeLines(w)
	case removeOverlay:
		return m.confirmLines("REMOVE CONTACT", "Remove %s from the radio? Its message history stays here.", "y remove · any other key cancel")
	case autoOverlay:
		return m.autoLines()
	case pruneOverlay:
		return m.pruneLines()
	}
	if c.heardView {
		return m.heardLines(w, rows)
	}
	detail := m.contactDetail(w)
	table := m.contactTable(w, max(rows-len(detail)-1, 3))
	out := append(table, m.th.Fg(m.th.Line, false).Render(strings.Repeat("─", w)))
	out = append(out, detail...)
	if len(out) > rows {
		out = out[:rows] // a very short terminal: the table keeps at least three rows, the detail gives way
	}
	return out
}

func (m *Model) contactsTitle() string {
	c := &m.contacts
	n := len(m.contactRows())
	title := fmt.Sprintf("CONTACTS %d · type %s · sort %s", n, typeFilters[c.typeFilter].name, sortNames[c.sortBy])
	if len(c.heard) > 0 {
		title += fmt.Sprintf(" · %d HEARD", len(c.heard))
	}
	return title
}

func (m *Model) contactTable(w, rows int) []string {
	th, c, now := m.th, &m.contacts, m.now
	var out []string
	if c.full {
		out = append(out, th.Block(th.Bg, th.Yellow).Render(" TABLE FULL ")+th.Paint(th.Yellow, false, "  new nodes can't be added · P suggests contacts to prune"))
	}
	if c.filtering || c.filter.text != "" {
		out = append(out, th.Paint(th.Yellow, true, "/ ")+filterText(th, c.filter, c.filtering, w-16)+th.Paint(th.Dim, false, "  · esc clear"))
	}
	nameW := min(max(w/4, 14), 26)
	routeW := max(w-nameW-48, 10)
	head := fmt.Sprintf("  %-*s %-9s %-9s %-*s %-4s %s", nameW, "NAME", "TYPE", "HEARD", routeW, "ROUTE", "LOC", "KEY")
	out = append(out, th.Paint(th.Muted, true, head))
	list := m.contactRows()
	if len(list) == 0 {
		return append(out, th.Paint(th.Dim, false, "  nothing matches"))
	}
	room := rows - len(out)
	start := min(max(c.sel-room/2, 0), max(len(list)-room, 0))
	for i := start; i < len(list) && len(out) < rows; i++ {
		ct := list[i]
		star := " "
		if ct.Flags&radio.FlagFavourite != 0 {
			star = "★"
		}
		loc := "—"
		if ct.Lat != 0 || ct.Lon != 0 {
			loc = "✓"
		}
		if _, ok := m.manualPlace(ct.Key); ok {
			loc = "✎"
		}
		heard := ago(now.Sub(ct.LastAdvert))
		if ct.LastAdvert.IsZero() || ct.LastAdvert.Unix() <= 0 {
			heard = "never"
		}
		if i == c.sel {
			line := fmt.Sprintf("%s %s %-9s %-9s %s %-4s %s", star, col(ct.Name, nameW), ct.Type, heard, col(routeText(ct), routeW), loc, m.keyHash(ct.Key))
			out = append(out, th.Paint(th.Magenta, true, "▌")+m.raised(fit(line, w-1)))
			continue
		}
		out = append(out, m.arriving(" "+th.Paint(th.Yellow, true, star)+" "+
			th.Paint(th.Text, false, col(ct.Name, nameW))+" "+
			th.Paint(typeColour(th, ct.Type), false, fmt.Sprintf("%-9s", ct.Type))+" "+
			th.Paint(th.Dim, false, fmt.Sprintf("%-9s", heard))+" "+
			th.Paint(th.Muted, false, col(routeText(ct), routeW))+" "+
			th.Paint(th.Dim, false, fmt.Sprintf("%-4s", loc))+" "+
			th.Paint(th.Muted, false, m.keyHash(ct.Key)), w, c.arrived[ct.Key]))
	}
	return out
}

func (m *Model) contactDetail(w int) []string {
	th, c, now := m.th, &m.contacts, m.now
	ct, ok := m.selectedContact()
	if !ok {
		return nil
	}
	label := func(s string) string { return th.Fg(th.Dim, false).Render(fmt.Sprintf("%-12s", s)) }
	val := func(s string) string { return th.Fg(th.Text, false).Render(s) }
	fav := ""
	if ct.Flags&radio.FlagFavourite != 0 {
		fav = th.Fg(th.Yellow, true).Render("  ★ favourite")
	}
	out := []string{th.Fg(typeColour(th, ct.Type), true).Render("▌"+ct.Name) + th.Fg(th.Dim, false).Render("  "+strings.ToLower(ct.Type.String())) + fav}
	out = append(out, label("key")+th.Fg(th.Muted, false).Render(truncate(radio.KeyHex(ct.Key[:]), w-12)))
	where := "not shared · l places it by hand"
	if ct.Lat != 0 || ct.Lon != 0 {
		where = fmt.Sprintf("%.4f, %.4f", ct.Lat, ct.Lon)
	}
	if p, ok := m.manualPlace(ct.Key); ok {
		where = fmt.Sprintf("%.4f, %.4f · set by you", p.Lat, p.Lon)
	}
	out = append(out, label("last advert")+val(ago(now.Sub(ct.LastAdvert)))+th.Fg(th.Dim, false).Render("  ·  ")+label("location")+val(where))

	route := "flood: no stored route, so messages flood the mesh"
	if ct.PathLen == 0 {
		route = "direct: in range, no repeaters"
	} else if ct.PathLen > 0 {
		route = fmt.Sprintf("direct: %d hops via %s", ct.PathLen, hashes(ct.Path, ct.HashSize))
	}
	if t, ok := c.discovering[ct.Key]; ok {
		route += th.Fg(th.Yellow, false).Render("   discovering " + motion.RampBar(now, 3) + " " + ago(now.Sub(t)))
	} else if f, ok := c.found[ct.Key]; ok {
		route += th.Fg(th.Green, false).Render(fmt.Sprintf("   found: out %s · back %s", orFlood(f.Out, f.OutSize), orFlood(f.In, f.InSize)))
	}
	out = append(out, label("route")+val(route))

	adv := th.Fg(th.Dim, false).Render("…")
	if ap, ok := c.advert[ct.Key]; ok {
		via := "straight from them, no repeaters"
		if len(ap.Path) > 0 {
			via = fmt.Sprintf("%d hops via %s", len(ap.Path)/max(ap.HashSize, 1), hashes(ap.Path, ap.HashSize))
		}
		adv = val(fmt.Sprintf("%s, heard %s", via, ago(now.Sub(ap.At))))
	}
	out = append(out, label("advert path")+adv)
	out = append(out, label("trace")+m.traceLine(ct))
	sig := th.Fg(th.Dim, false).Render("not pinned · K pins this key to " + ct.Name + ", so messages signed as them can be checked")
	if p, ok := m.pinOf(ct.Name); ok {
		if p.Key == ct.Key {
			sig = th.Fg(th.Green, true).Render("✓ pinned") + th.Fg(th.Dim, false).Render(" · K unpins")
		} else {
			sig = th.Fg(th.Red, true).Render("⚠ the name is pinned to a different key, "+radio.KeyHex(p.Key[:4])) + th.Fg(th.Dim, false).Render(" · K pins this one instead")
		}
	}
	out = append(out, label("signing key")+sig)
	out = append(out, "")
	keys := "enter or m message · t trace · d discover route · p set route · r reset route · M route on the map · l place on the map · K pin signing key · f favourite · e share · x remove · I import · a heard · A auto-add · P prune · F type or favourites · o sort · / filter"
	for _, l := range strings.Split(renderWidth.Wrap(keys, w, "·"), "\n") {
		out = append(out, th.Fg(th.Dim, false).Render(l))
	}
	if len(out) > detailRows {
		out = out[:detailRows]
	}
	return out
}

// traceLine draws a trace hop by hop: each hop's signal appears in turn and is coloured on the signal scale.
func (m *Model) traceLine(ct radio.Contact) string {
	th, c, now := m.th, &m.contacts, m.now
	tag, ok := c.lastTrace[ct.Key]
	if !ok {
		if _, _, can := tracePath(ct, m.pathHash()); !can {
			return th.Fg(th.Dim, false).Render("needs a stored route; discover one with d")
		}
		return th.Fg(th.Dim, false).Render("press t to trace " + routeText(ct))
	}
	run := c.traces[tag]
	switch {
	case run.lost:
		return th.Fg(th.Red, true).Render("[FAIL]") + th.Fg(th.Red, false).Render(" no answer along "+hashes(run.path, run.size))
	case run.result == nil:
		return th.Fg(th.Yellow, true).Render(motion.RampBar(now, 3)) + th.Fg(th.Yellow, false).Render(" tracing "+hashes(run.path, run.size)+" · "+ago(now.Sub(run.started)))
	}
	r := run.result
	hops := splitPath(r.Hashes, max(r.HashSize, 1), true)
	steps := len(r.SNRs) + 1
	shown := steps
	if m.level.Allows(motion.Wipe) {
		shown = int(motion.Progress(run.at, traceReveal, now)*float64(steps)) + 1
	}
	var b strings.Builder
	b.WriteString(th.Fg(th.Text, true).Render("me"))
	for i := 0; i < steps && i < shown; i++ {
		snr, to := float64(r.LastSNR), "me"
		if i < len(r.SNRs) {
			snr, to = float64(r.SNRs[i]), "??"
			if i < len(hops) {
				to = fmt.Sprintf("%X", hops[i])
			}
		}
		b.WriteString(th.Fg(th.Dim, false).Render(" ─"))
		b.WriteString(th.Fg(th.SNR(snr), true).Render(fmt.Sprintf("%+.1f", snr)))
		b.WriteString(th.Fg(th.Dim, false).Render("→ "))
		b.WriteString(th.Fg(th.Text, true).Render(to))
	}
	return b.String()
}

func (m *Model) heardLines(w, rows int) []string {
	th, c := m.th, &m.contacts
	out := []string{th.Fg(th.Muted, true).Render("HEARD THIS SESSION, NOT CONTACTS") + th.Fg(th.Dim, false).Render("  ·  enter add · esc back"), ""}
	if len(c.heard) == 0 {
		return append(out, th.Fg(th.Dim, false).Render("Nothing yet. Nodes appear here when their advert arrives but auto-add doesn't cover them (A to change)."))
	}
	room := rows - len(out)
	start := min(max(c.heardSel-room/2, 0), max(len(c.heard)-room, 0))
	for i := start; i < len(c.heard) && len(out) < rows; i++ {
		h := c.heard[i]
		line := fmt.Sprintf(" %s %-9s %s  heard %s", col(h.c.Name, 24), h.c.Type, m.keyHash(h.c.Key), ago(m.now.Sub(h.at)))
		if i == c.heardSel {
			out = append(out, th.Fg(th.Magenta, true).Render("▌")+m.raised(fit(line, w-1)))
			continue
		}
		out = append(out, m.arriving(" "+th.Fg(typeColour(th, h.c.Type), false).Render(line), w, c.arrived[h.c.Key]))
	}
	return out
}

func (m *Model) shareLines(w, rows int) []string {
	th, c := m.th, &m.contacts
	ct, _ := m.selectedContact()
	out := []string{th.Fg(th.Muted, true).Render("SHARE "+strings.ToUpper(ct.Name)) + th.Fg(th.Dim, false).Render("  ·  y copy the link · any other key close"), ""}
	if c.link == "" {
		return append(out, th.Fg(th.Yellow, false).Render(motion.RampBar(m.now, 3)+" exporting from the radio…"))
	}
	code := qrLines(c.link)
	linkRows := strings.Split(renderWidth.Hardwrap(c.link, w, false), "\n")
	if len(code)+len(linkRows)+3 > rows || len(code) > 0 && visibleWidth(code[0]) > w {
		out = append(out, th.Fg(th.Yellow, false).Render("The terminal is too small for the QR code; enlarge it, or copy the link with c."), "")
	} else {
		pad := strings.Repeat(" ", max((w-visibleWidth(code[0]))/2, 0))
		for _, l := range code {
			out = append(out, pad+l)
		}
		out = append(out, "")
	}
	for _, l := range linkRows {
		out = append(out, th.Fg(th.Cyan, false).Render(l))
	}
	return out
}

// qrLines draws a QR code two modules per character row, always black on white so phones can read it.
func qrLines(text string) []string {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return nil
	}
	const quiet = 2
	size := code.Size + 2*quiet
	dark := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	black, white := lipgloss.Color("#000000"), lipgloss.Color("#ffffff")
	var out []string
	for y := 0; y < size; y += 2 {
		var b strings.Builder
		for x := 0; x < size; x++ {
			top, bottom := white, white
			if dark(x, y) {
				top = black
			}
			if y+1 < size && dark(x, y+1) {
				bottom = black
			}
			b.WriteString(lipgloss.NewStyle().Foreground(top).Background(bottom).Render("▀"))
		}
		out = append(out, b.String())
	}
	return out
}

func (m *Model) editorLines(w, rows int) []string {
	th, c := m.th, &m.contacts
	ct, _ := m.selectedContact()
	chips := th.Fg(th.Text, true).Render("me")
	for h := range slices.Chunk(c.editPath, c.editSize) {
		chips += th.Fg(th.Dim, false).Render(" › ") + th.Fg(th.Cyan, true).Render(fmt.Sprintf("%X", h))
	}
	chips += th.Fg(th.Dim, false).Render(" › ") + th.Fg(typeColour(th, ct.Type), true).Render(ct.Name)
	if len(c.editPath) == 0 {
		chips += th.Fg(th.Dim, false).Render("   (no repeaters: in direct range)")
	}
	out := []string{th.Fg(th.Muted, true).Render("SET ROUTE TO "+strings.ToUpper(ct.Name)) +
		th.Fg(th.Dim, false).Render("  ·  ↑↓ choose · enter add hop · backspace remove hop · s save · esc cancel"), "", chips, ""}
	reps := m.repeaters()
	byHash := map[string][]string{}
	for _, r := range reps {
		byHash[string(r.Key[:c.editSize])] = append(byHash[string(r.Key[:c.editSize])], r.Name)
	}
	start := min(max(c.editSel-(rows-6)/2, 0), max(len(reps)-(rows-5), 0))
	for i := start; i < len(reps) && len(out) < rows; i++ {
		r := reps[i]
		note := ""
		if same := byHash[string(r.Key[:c.editSize])]; len(same) > 1 {
			note = "  shares its hash with " + strings.Join(without(same, r.Name), ", ")
		}
		line := fmt.Sprintf(" %X  %s %s", r.Key[:c.editSize], col(r.Name, 22), routeText(r))
		if i == c.editSel {
			out = append(out, th.Fg(th.Magenta, true).Render("▌")+m.raised(fit(line, w-1)))
			continue
		}
		out = append(out, " "+th.Fg(th.Text, false).Render(line)+th.Fg(th.Yellow, false).Render(note))
	}
	return out
}

func without(list []string, drop string) []string {
	var out []string
	for _, s := range list {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}

func (m *Model) importLines(w int) []string {
	th := m.th
	return []string{
		th.Fg(th.Muted, true).Render("IMPORT A CONTACT") + th.Fg(th.Dim, false).Render("  ·  paste a meshcore:// link · enter import · esc cancel"), "",
		th.Fg(th.Magenta, true).Render("▸ ") + m.contacts.importIn.render(th, w-2, false),
	}
}

func (m *Model) importAskLines(w int) []string {
	th, lc := m.th, m.contacts.importing
	out := []string{th.Fg(th.Muted, true).Render("IMPORT A CONTACT"), ""}
	if lc.name == "" {
		out = append(out, th.Fg(th.Yellow, false).Render(truncate("The link doesn't say who it's for in a way this app can read.", w)))
	} else {
		out = append(out, th.Fg(th.Text, true).Render(truncate(lc.name, w-20))+th.Fg(th.Muted, false).Render("  key "+radio.KeyHex(lc.key[:6])))
	}
	if lc.clash != "" {
		out = append(out, "", th.Fg(th.Red, true).Render(truncate("⚠ You already have a "+lc.clash+" with a different key. This may be someone else using the name.", w)))
	}
	return append(out, "", th.Fg(th.Dim, false).Render("y import · any other key cancel"))
}

func (m *Model) placeLines(w int) []string {
	th := m.th
	ct, _ := m.selectedContact()
	shared := "it doesn't share one"
	if ct.Lat != 0 || ct.Lon != 0 {
		shared = fmt.Sprintf("it shares %.4f, %.4f; yours is used instead", ct.Lat, ct.Lon)
	}
	return []string{
		th.Fg(th.Muted, true).Render("PLACE "+strings.ToUpper(ct.Name)) + th.Fg(th.Dim, false).Render(truncate("  ·  latitude, longitude · enter place · empty clears it · esc cancel", w-len(ct.Name)-6)), "",
		th.Fg(th.Magenta, true).Render("▸ ") + m.contacts.placeIn.render(th, w-2, false), "",
		th.Fg(th.Dim, false).Render(truncate("Only this app uses it, for the map and its routes; "+shared+".", w)),
	}
}

func (m *Model) confirmLines(title, question, hint string) []string {
	th := m.th
	ct, _ := m.selectedContact()
	return []string{th.Fg(th.Red, true).Render(title), "", th.Fg(th.Text, false).Render(fmt.Sprintf(question, ct.Name)), "", th.Fg(th.Dim, false).Render(hint)}
}

func (m *Model) autoLines() []string {
	th, c := m.th, &m.contacts
	out := []string{th.Fg(th.Muted, true).Render("AUTO-ADD") + th.Fg(th.Dim, false).Render("  ·  which newly heard nodes the radio adds by itself · space toggle · ←→ hops · s save · esc cancel"), ""}
	if !c.autoReady {
		return append(out, th.Fg(th.Yellow, false).Render(motion.RampBar(m.now, 3)+" reading from the radio…"))
	}
	a := c.auto
	rows := []struct {
		label string
		on    bool
	}{{"Chat nodes", a.Chat}, {"Repeaters", a.Repeater}, {"Room servers", a.Room}, {"Sensors", a.Sensor}, {"Replace the oldest contact when full", a.OverwriteOldest}}
	for i, r := range rows {
		box := th.Fg(th.Dim, false).Render("[ ]")
		if r.on {
			box = th.Fg(th.Green, true).Render("[x]")
		}
		line := box + " " + th.Fg(th.Text, false).Render(r.label)
		if i == c.autoSel {
			line = th.Fg(th.Magenta, true).Render("▌") + line
		} else {
			line = " " + line
		}
		out = append(out, line)
	}
	hops := "no limit"
	if a.MaxHops > 0 {
		hops = fmt.Sprintf("up to %d hops away", a.MaxHops-1)
		if a.MaxHops == 1 {
			hops = "direct neighbours only"
		}
	}
	line := th.Fg(th.Cyan, true).Render("‹ "+fmt.Sprint(a.MaxHops)+" ›") + " " + th.Fg(th.Text, false).Render("Hop limit: "+hops)
	if c.autoSel == len(rows) {
		line = th.Fg(th.Magenta, true).Render("▌") + line
	} else {
		line = " " + line
	}
	return append(out, line)
}

func (m *Model) pruneLines() []string {
	th, c := m.th, &m.contacts
	out := []string{th.Fg(th.Yellow, true).Render("PRUNE CONTACTS"), "",
		th.Fg(th.Text, false).Render("The table is full. These are the least recently heard contacts that aren't favourites:"), ""}
	for _, k := range c.prune {
		name := m.contactName(k)
		heard := ""
		for _, ct := range m.chat.Contacts() {
			if ct.Key == k {
				heard = ago(m.now.Sub(ct.LastAdvert))
			}
		}
		out = append(out, "  "+th.Fg(th.Text, false).Render(col(name, 24))+th.Fg(th.Dim, false).Render("heard "+heard))
	}
	return append(out, "", th.Fg(th.Dim, false).Render("y remove them all · any other key cancel · history is kept"))
}
