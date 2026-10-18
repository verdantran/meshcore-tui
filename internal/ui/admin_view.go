package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

func (m *Model) adminTitle() string {
	if ct, sess, ok := m.openNode(); ok {
		title := "ADMIN › " + ct.Name
		if m.adm.local {
			title = strings.ToUpper(paneNames[m.adm.pane]) + " › " + ct.Name
		}
		if sess.State == admin.LoggedIn {
			title += " (" + admin.LevelName(sess.Level) + ")"
		}
		if sess.Owner != nil && sess.Owner.FirmwareVersion != "" {
			title += " · fw " + sess.Owner.FirmwareVersion
		}
		if m.adm.local {
			return title + " · local console"
		}
		return title + " · " + routeText(ct)
	}
	in := 0
	for _, c := range m.adminNodes() {
		if s, ok := m.admin.Find(c.Key); ok && s.State == admin.LoggedIn {
			in++
		}
	}
	title := fmt.Sprintf("ADMIN · %d NODES · %d SIGNED IN", len(m.adminNodes()), in)
	if m.adm.favOnly {
		title = fmt.Sprintf("ADMIN · %d FAVOURITES · %d SIGNED IN", len(m.adminNodes()), in)
	}
	alerts := 0
	for _, c := range m.adminNodes() {
		if m.alerts(c).Any() {
			alerts++
		}
	}
	if alerts > 0 {
		title += fmt.Sprintf(" · %d ALERTS", alerts)
	}
	return title
}

func (m *Model) renderAdmin(w, rows int) []string {
	th := m.th
	if m.chat == nil || m.chat.Device() == "" {
		return []string{th.Fg(th.Dim, false).Render("Repeaters and room servers appear once the radio has synced.")}
	}
	ct, sess, ok := m.openNode()
	if !ok {
		return m.nodeList(w, rows)
	}
	out := []string{m.paneBar(w), th.Fg(th.Line, false).Render(strings.Repeat("─", w))}
	if m.adm.local {
		out = nil // the tab bar lists the panes
	}
	var body []string
	switch {
	case m.adm.prompt != noPrompt:
		body = m.promptLines(ct, sess, w, rows-3)
	case m.adm.pane == paneHealth:
		body = m.healthLines(ct, sess, w, rows-len(out)-1)
	case sess.State != admin.LoggedIn:
		body = m.loginLines(ct, sess)
	case !sess.Admin && m.adm.pane != paneStatus && m.adm.pane != paneNeighbours && m.adm.pane != paneTelemetry:
		body = []string{th.Fg(th.Yellow, false).Render(paneNames[m.adm.pane] + " needs an admin login. You're signed in as a guest; L logs out so you can sign in again.")}
	default:
		body = m.paneLines(ct, sess, w, rows-3)
	}
	room := rows - len(out) - 1
	if len(body) > room {
		body = body[:room]
	}
	out = append(out, body...)
	for len(out) < rows-1 {
		out = append(out, "")
	}
	return append(out, th.Fg(th.Dim, false).Render(clip(m.paneHint(sess), w)))
}

func (m *Model) paneBar(w int) string {
	th, a := m.th, &m.adm
	var parts []string
	for i, name := range paneNames {
		if adminPane(i) == a.pane {
			if m.level.Allows(motion.Decode) && motion.Running(a.paneAt, wipeDur*2, m.now) {
				name = motion.DecodeText(name, int64(i), motion.Progress(a.paneAt, wipeDur*2, m.now))
			}
			parts = append(parts, m.raised(" "+name+" "))
			continue
		}
		parts = append(parts, th.Fg(th.Muted, false).Render(" "+name+" "))
	}
	return clip(strings.Join(parts, " ")+th.Fg(th.Dim, false).Render("  ←→"), w)
}

func (m *Model) paneHint(sess *admin.Session) string {
	if m.adm.prompt != noPrompt {
		return ""
	}
	return hintLine(m.paneHints(sess))
}

// paneHints are the keys of the open node's pane, for its hint line and for ?.
func (m *Model) paneHints(sess *admin.Session) []hint {
	a := &m.adm
	var hs []hint
	switch {
	case a.pane == paneHealth:
		if !a.local {
			hs = append(hs, hint{"w", "watch or stop"})
		}
		hs = append(hs, hint{"enter", "cursor"}, hint{"r", "24 hours or 7 days"}, hint{"u", "poll now"}, hint{"C", "clear history"})
	case sess.State != admin.LoggedIn:
		return []hint{{"enter", "log in"}, {"esc", "back to the node list"}}
	default:
		hs = map[adminPane][]hint{
			paneStatus:     {{"u", "refresh"}, {"a", "auto-refresh"}, {"A", "send advert"}, {"c", "sync clock"}, {"R", "reboot"}},
			paneSettings:   {{"↑↓", "choose"}, {"enter", "edit"}, {"x", "discard edit"}, {"u", "read group"}, {"U", "read all"}, {"s", "review and apply"}},
			paneNeighbours: {{"u", "refresh"}, {"d", "discover again"}, {"x", "remove"}},
			paneACL:        {{"u", "refresh"}, {"n", "add a contact"}, {"+ -", "change level"}, {"x", "remove"}},
			paneRegions:    {{"u", "refresh"}, {"n", "new region under this one"}, {"h", "home"}, {"f", "flood on or off"}, {"x", "remove"}, {"s", "save"}},
			paneTelemetry:  {{"u", "refresh"}},
			paneLog:        {{"r", "read and sum up (USB)"}, {"s", "start logging"}, {"t", "stop"}, {"C", "erase"}},
			paneConsole:    {{"enter", "send"}, {"↑↓", "history"}, {"tab", "complete"}, {"esc", "clear, then leave"}},
		}[a.pane]
		if a.pane == paneStatus && !a.local {
			hs = append(hs, hint{"w", "watch"}, hint{"L", "log out"})
		}
	}
	switch {
	case a.local && a.pane == paneStatus:
		hs = append(hs, hint{"1-9 ←→", "screens"})
	case a.local && a.pane == paneHealth:
		hs = append(hs, hint{"", "polled every " + every(m.watchEvery()) + " while connected"})
	case !a.local && a.pane != paneConsole:
		hs = append(hs, hint{"esc", "back"})
	}
	return hs
}

// authBadge is a node's login state: AUTH while waiting, then OK or FAIL.
func (m *Model) authBadge(sess *admin.Session, ok bool) string {
	th, now := m.th, m.now
	if !ok {
		return th.Fg(th.Dim, false).Render("signed out")
	}
	switch sess.State {
	case admin.LoggingIn:
		n := 3
		if m.level.Allows(motion.Ramp) {
			n = int(now.UnixMilli()/180) % 4
		}
		return th.Fg(th.Yellow, true).Render("AUTH " + strings.Repeat("▸", n) + strings.Repeat(" ", 3-n))
	case admin.LoggedIn:
		label := "[ OK ] " + strings.ToUpper(admin.LevelName(sess.Level))
		if m.level.Allows(motion.Flash) && motion.Running(m.adm.resultAt[sess.Key], authFlash, now) {
			return th.Block(th.Bg, th.Green).Render(label)
		}
		return th.Fg(th.Green, true).Render(label)
	case admin.LoginFailed:
		return th.Fg(th.Red, true).Render("[FAIL]")
	}
	return th.Fg(th.Dim, false).Render("signed out")
}

func (m *Model) nodeList(w, rows int) []string {
	th, now := m.th, m.now
	nodes := m.adminNodes()
	if len(nodes) == 0 && m.adm.favOnly {
		return []string{th.Fg(th.Dim, false).Render("No favourite repeaters or room servers. F shows them all; f on one marks it a favourite.")}
	}
	if len(nodes) == 0 {
		return []string{th.Fg(th.Dim, false).Render("No repeaters or room servers in your contacts yet. They're added when their adverts are heard.")}
	}
	nameW := min(max(w/6, 12), 24)
	const trendW = 12
	out := []string{th.Fg(th.Muted, true).Render(fmt.Sprintf("   %-*s %-9s %-15s %-2s %-18s %-9s %-6s %-9s %-9s %-*s %s", nameW, "NAME", "TYPE", "SESSION", "PW", "BATTERY", "UPTIME", "NOISE", "FIRMWARE", "POLLED", trendW, "RX 24H", "ALERTS"))}
	room := rows - 2
	start := min(max(m.adm.sel-room/2, 0), max(len(nodes)-room, 0))
	for i := start; i < len(nodes) && len(out) < rows-1; i++ {
		c := nodes[i]
		sess, has := m.admin.Find(c.Key)
		batt := th.Fg(th.Text, false).Render(fmt.Sprintf("%-18s", "—"))
		up, noise, polled, fw := "—", "—", "never", "—"
		if has && sess.Owner != nil {
			fw = sess.Owner.FirmwareVersion
		}
		saved := " "
		if ok, _ := m.keys.Saved(c.Key); ok {
			saved = "●"
		}
		if has {
			if p, ok := sess.Latest(); ok {
				st := statsOf(p)
				batt = m.batteryText(c.Key, radio.Battery{MilliVolts: st.BattMilliVolts}, false)
				up, noise, polled = uptime(st.UpTimeSecs), fmt.Sprint(st.NoiseFloor), ago(now.Sub(p.At))
			}
		}
		badge := m.authBadge(sess, has)
		pad := strings.Repeat(" ", max(15-visibleWidth(badge), 0))
		marker := " "
		if i == m.adm.sel {
			marker = th.Fg(th.Magenta, true).Render("▌")
		}
		star := " "
		if favourite(c) {
			star = th.Fg(th.Yellow, true).Render("★")
		}
		watch := " "
		if m.watched(c.Key) {
			watch = th.Fg(th.Green, true).Render("◉")
		}
		if hist := m.historyOf(c.Key); len(hist) > 0 && (!has || hist[len(hist)-1].At.After(lastStatus(sess))) {
			p := hist[len(hist)-1]
			st := statsOf(p)
			batt = m.batteryText(c.Key, radio.Battery{MilliVolts: st.BattMilliVolts}, false)
			up, noise, polled = uptime(st.UpTimeSecs), fmt.Sprint(st.NoiseFloor), ago(now.Sub(p.At))
		}
		line := marker + star + watch + th.Fg(th.Text, i == m.adm.sel).Render(col(c.Name, nameW)) + " " +
			th.Fg(typeColour(th, c.Type), false).Render(fmt.Sprintf("%-9s", c.Type)) + " " + badge + pad + " " +
			th.Fg(th.Green, false).Render(fmt.Sprintf("%-2s ", saved)) +
			batt + " " + th.Fg(th.Text, false).Render(fmt.Sprintf("%-9s %-6s %-9s ", up, noise, clip(fw, 9))) + th.Fg(th.Dim, false).Render(fmt.Sprintf("%-9s ", polled)) +
			m.trend(c.Key, trendW) + " " + th.Fg(th.Red, true).Render(m.alerts(c).String())
		out = append(out, clip(line, w))
	}
	return append(out, "", th.Fg(th.Dim, false).Render(clip("enter open · L log in or out · u U poll one or all · w watch · H history · f F favourite, only those · x forget password · ● saved ◉ watched", w)))
}

func uptime(secs uint32) string {
	d := time.Duration(secs) * time.Second
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

func (m *Model) loginLines(ct radio.Contact, sess *admin.Session) []string {
	th := m.th
	out := []string{"", "  " + m.authBadge(sess, true), ""}
	switch sess.State {
	case admin.LoggingIn:
		out = append(out, th.Fg(th.Muted, false).Render("  Waiting for "+ct.Name+" to answer. Logins can take a few seconds over several hops."))
	case admin.LoginFailed:
		out = append(out, th.Fg(th.Red, false).Render("  The login failed or got no answer."), th.Fg(th.Muted, false).Render("  Press enter to try again."))
	default:
		out = append(out, th.Fg(th.Muted, false).Render("  Press enter to log in."))
	}
	return out
}

func (m *Model) promptLines(ct radio.Contact, sess *admin.Session, w, rows int) []string {
	th, a := m.th, &m.adm
	field := func(label string, mask bool) string {
		return th.Fg(th.Magenta, true).Render(label+" ▸ ") + a.input.render(th, w-visibleWidth(label)-3, mask)
	}
	switch a.prompt {
	case promptPassword:
		box := th.Fg(th.Dim, false).Render("[ ]")
		if a.remember {
			box = th.Fg(th.Green, true).Render("[x]")
		}
		return []string{th.Fg(th.Muted, true).Render("LOG IN TO " + strings.ToUpper(ct.Name)), "",
			field("password", true), "",
			box + th.Fg(th.Text, false).Render(" remember it in the system keychain ") + th.Fg(th.Dim, false).Render("(ctrl+s)"), "",
			th.Fg(th.Dim, false).Render("enter log in · leave it empty to log in as a guest · esc cancel")}
	case promptEdit:
		st := m.settingRows(sess)[a.setSel]
		out := []string{th.Fg(th.Muted, true).Render("EDIT " + strings.ToUpper(st.Label)), "", field(st.Key, st.Kind == admin.KindPassword), ""}
		if st.Help != "" {
			out = append(out, th.Fg(th.Dim, false).Render(clip(st.Help, w)))
		}
		return append(out, th.Fg(th.Dim, false).Render("enter stage the change · it's sent when you apply with s · esc cancel"))
	case promptRegion:
		return []string{th.Fg(th.Muted, true).Render("NEW REGION UNDER " + strings.ToUpper(sess.Regions[a.regSel].Name)), "", field("name", false), "",
			th.Fg(th.Dim, false).Render("enter add it with flood allowed · esc cancel")}
	case promptDiff:
		return m.diffLines(ct, w)
	case promptConfirm:
		c := a.confirm
		out := []string{th.Fg(th.Red, true).Render(c.title), ""}
		for _, l := range strings.Split(ansi.Wrap(c.warning, w, " "), "\n") {
			out = append(out, th.Fg(th.Yellow, false).Render(l))
		}
		return append(out, "", th.Fg(th.Text, false).Render("Type "+ct.Name+" to go ahead:"), field("name", false), "",
			th.Fg(th.Dim, false).Render("enter go ahead · esc cancel"))
	case promptPick:
		people := m.aclCandidates(sess)
		out := []string{th.Fg(th.Muted, true).Render("GIVE A CONTACT ACCESS") + th.Fg(th.Dim, false).Render("  ·  they start read-only; + raises it · enter add · esc cancel"), ""}
		if len(people) == 0 {
			return append(out, th.Fg(th.Dim, false).Render("Every chat contact is already on the list."))
		}
		start := min(max(a.pickSel-(rows-3)/2, 0), max(len(people)-(rows-2), 0))
		for i := start; i < len(people) && len(out) < rows; i++ {
			line := fmt.Sprintf(" %s %s", col(people[i].Name, 24), radio.KeyHex(people[i].Key[:6]))
			if i == a.pickSel {
				out = append(out, th.Fg(th.Magenta, true).Render("▌")+m.raised(fit(line, w-1)))
				continue
			}
			out = append(out, " "+th.Fg(th.Text, false).Render(line))
		}
		return out
	}
	return nil
}

func (m *Model) diffLines(ct radio.Contact, w int) []string {
	th := m.th
	changes := m.admin.Changes(ct.Key)
	out := []string{th.Fg(th.Muted, true).Render("REVIEW CHANGES TO " + strings.ToUpper(ct.Name)), ""}
	reboot := false
	for _, ch := range changes {
		from, to := shown(ch.Setting, ch.From), shown(ch.Setting, ch.To)
		if from == "" {
			from = "(not read)"
		}
		note := ""
		if ch.Setting.Reboot {
			note, reboot = th.Fg(th.Yellow, false).Render("  needs a reboot"), true
		}
		out = append(out, clip(th.Fg(th.Text, false).Render(fmt.Sprintf("  %-28s ", ch.Setting.Label))+
			th.Fg(th.Dim, false).Strikethrough(true).Render(from)+th.Fg(th.Dim, false).Render(" → ")+th.Fg(th.Cyan, true).Render(to)+note, w))
	}
	out = append(out, "")
	hint := "y apply · any other key go back"
	if reboot {
		hint = "y apply · Y apply and reboot so they take effect · any other key go back"
	}
	return append(out, th.Fg(th.Dim, false).Render(hint))
}

func (m *Model) paneLines(ct radio.Contact, sess *admin.Session, w, rows int) []string {
	switch m.adm.pane {
	case paneStatus:
		return m.statusLines(ct, sess, w)
	case paneSettings:
		return m.settingsLines(sess, w, rows)
	case paneNeighbours:
		return m.neighbourLines(sess, w, rows)
	case paneACL:
		return m.aclLines(sess, w, rows)
	case paneRegions:
		return m.regionLines(sess, w, rows)
	case paneTelemetry:
		return m.telemetryLines(sess, w)
	case paneLog:
		return m.logLines(sess, w, rows)
	}
	return m.consoleLines(sess, w, rows)
}

func (m *Model) waiting(what string) string {
	return m.th.Fg(m.th.Yellow, false).Render(motion.RampBar(m.now, 3) + " " + what)
}

// pendingJob reports whether a request of this kind is queued or in flight for the node.
func (m *Model) pendingJob(key [32]byte, kind admin.JobKind) bool {
	j, ok := m.admin.Running()
	return ok && j.Key == key && j.Kind == kind
}

func (m *Model) statusLines(ct radio.Contact, sess *admin.Session, w int) []string {
	th, now := m.th, m.now
	latest, ok := sess.Latest()
	if !ok {
		if m.pendingJob(ct.Key, admin.JobStatus) || m.admin.Queued() > 0 {
			return []string{m.waiting("asking " + ct.Name + " for its status…")}
		}
		return []string{th.Fg(th.Dim, false).Render("No status yet. Press u to ask.")}
	}
	prev := latest
	if n := len(sess.Status); n > 1 {
		prev = sess.Status[n-2]
	}
	roll := func(from, to float64) float64 {
		if !m.level.Allows(motion.CountUp) {
			return to
		}
		return motion.Count(from, to, latest.At, countDur, now)
	}
	cur, old := statsOf(latest), statsOf(prev)
	label := func(s string) string { return th.Fg(th.Dim, false).Render(s + " ") }
	val := func(s string) string { return th.Fg(th.Text, true).Render(s) }
	num := func(from, to uint32) string { return val(commas(int(roll(float64(from), float64(to))))) }

	var batt []float64
	for _, p := range sess.Status {
		batt = append(batt, float64(statsOf(p).BattMilliVolts))
	}
	snr := roll(float64(old.LastSNR), float64(cur.LastSNR)) / 4
	up := max(cur.UpTimeSecs, 1)
	rxAir := 0.0
	if latest.Room == nil {
		rxAir = float64(latest.Stats.RxAirTimeSecs) / float64(up) * 100
	}
	out := []string{
		label("Uptime") + val(fmt.Sprintf("%-10s", uptime(cur.UpTimeSecs))) + label("Battery") +
			m.batteryText(ct.Key, radio.Battery{MilliVolts: uint16(roll(float64(old.BattMilliVolts), float64(cur.BattMilliVolts)))}, true) + " " +
			m.spark(batt, min(24, w/4), latest.At) +
			"   " + label("Queue") + val(fmt.Sprint(cur.TxQueueLen)),
		label("Noise floor") + val(fmt.Sprintf("%-6.0f", roll(float64(old.NoiseFloor), float64(cur.NoiseFloor)))) +
			label("Last RSSI") + val(fmt.Sprintf("%-6d", cur.LastRSSI)) + label("Last SNR") + th.Fg(th.SNR(snr), true).Render(fmt.Sprintf("%-6.1f", snr)) +
			label("Airtime") + val(fmt.Sprintf("tx %.1f%% rx %.1f%%", float64(cur.AirTimeSecs)/float64(up)*100, rxAir)),
		label("Packets ") + label("rx") + num(old.PacketsRecv, cur.PacketsRecv) + label("  tx") + num(old.PacketsSent, cur.PacketsSent) +
			label("  flood fwd") + num(old.SentFlood, cur.SentFlood) + label("  direct fwd") + num(old.SentDirect, cur.SentDirect) +
			label("  dupes") + num(uint32(old.FloodDups)+uint32(old.DirectDups), uint32(cur.FloodDups)+uint32(cur.DirectDups)),
	}
	if latest.Room != nil {
		out = append(out, label("Room    ")+label("posted")+val(fmt.Sprint(latest.Room.Posted))+label("  pushed")+val(fmt.Sprint(latest.Room.PostPushes)))
	} else {
		errs := th.Fg(th.Text, true)
		if latest.Stats.RecvErrors > 0 {
			errs = th.Fg(th.Yellow, true)
		}
		out = append(out, label("Errors  ")+label("rx")+errs.Render(commas(int(latest.Stats.RecvErrors))))
	}
	for i := range out {
		out[i] = clip(out[i], w)
	}
	polled := "polled " + ago(now.Sub(latest.At))
	if m.adm.auto[ct.Key] {
		polled += fmt.Sprintf(" · auto-refresh every %s", m.autoEvery())
	}
	out = append(out, "", th.Fg(th.Dim, false).Render(polled))
	if m.pendingJob(ct.Key, admin.JobStatus) {
		out = append(out, m.waiting("refreshing…"))
	}
	return out
}

func commas(n int) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// spark draws the last w values as block heights; the newest point glows briefly when it arrives.
func (m *Model) spark(vals []float64, w int, at time.Time) string {
	th := m.th
	if len(vals) > w {
		vals = vals[len(vals)-w:]
	}
	if len(vals) == 0 {
		return ""
	}
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		lo, hi = min(lo, v), max(hi, v)
	}
	var b strings.Builder
	for _, v := range vals[:len(vals)-1] {
		b.WriteRune(sparkRune(v, lo, hi))
	}
	lead := th.Fg(th.Cyan, true)
	if m.level.Allows(motion.Flash) && motion.Running(at, lampDur, m.now) {
		lead = th.Fg(th.Text, true)
	}
	return th.Fg(th.Cyan, false).Render(b.String()) + lead.Render(string(sparkRune(vals[len(vals)-1], lo, hi)))
}

func sparkRune(v, lo, hi float64) rune {
	if hi == lo {
		return sparkRunes[len(sparkRunes)/2-1]
	}
	return sparkRunes[int((v-lo)/(hi-lo)*float64(len(sparkRunes)-1)+0.5)]
}

func (m *Model) settingsLines(sess *admin.Session, w, rows int) []string {
	th, a := m.th, &m.adm
	settings := m.settingRows(sess)
	a.setSel = min(a.setSel, len(settings)-1)
	type row struct {
		group string
		i     int
	}
	var list []row
	group := ""
	for i, st := range settings {
		if st.Group != group {
			group = st.Group
			list = append(list, row{group: group, i: -1})
		}
		list = append(list, row{i: i})
	}
	selRow := 0
	for r, l := range list {
		if l.i == a.setSel {
			selRow = r
		}
	}
	var out []string
	if n := len(sess.Pending); n > 0 {
		out = append(out, th.Fg(th.Yellow, true).Render(fmt.Sprintf("%d CHANGES STAGED", n))+th.Fg(th.Dim, false).Render("  ·  s reviews and applies them"))
	}
	out = append(out, m.appliedLines(sess, w)...)
	room := rows - len(out) - 3
	start := min(max(selRow-room/2, 0), max(len(list)-room, 0))
	labelW := min(max(w/3, 20), 32)
	for r := start; r < len(list) && len(out) < rows-3; r++ {
		l := list[r]
		if l.i < 0 {
			out = append(out, th.Fg(th.Muted, true).Render(strings.ToUpper(l.group)))
			continue
		}
		st := settings[l.i]
		v := sess.Settings[st.Key]
		var value string
		switch {
		case v.Loading:
			value = th.Fg(th.Yellow, false).Render(motion.RampBar(m.now, 3))
		case v.Err != "":
			value = th.Fg(th.Red, false).Render(v.Err)
		case v.At.IsZero():
			value = th.Fg(th.Dim, false).Render("not read")
		case st.Kind == admin.KindPassword:
			value = th.Fg(th.Text, false).Render(shown(st, v.Value))
		default:
			value = th.Fg(th.Text, false).Render(v.Value + unitText(st.Unit))
		}
		if p, ok := sess.Pending[st.Key]; ok {
			value += th.Fg(th.Dim, false).Render(" → ") + th.Fg(th.Cyan, true).Render(shown(st, p)+unitText(st.Unit))
		}
		flags := ""
		if st.Reboot {
			flags += th.Fg(th.Yellow, false).Render(" ⟳")
		}
		if st.ReadOnly {
			flags += th.Fg(th.Dim, false).Render(" ro")
		}
		name := fmt.Sprintf("  %-*s", labelW, clip(st.Label, labelW))
		if l.i == a.setSel {
			out = append(out, clip(th.Fg(th.Magenta, true).Render("▌")+m.raised(name[1:])+" "+value+flags, w))
			continue
		}
		out = append(out, clip(th.Fg(th.Text, false).Render(name)+" "+value+flags, w))
	}
	out = append(out, "")
	if st := settings[a.setSel]; st.Help != "" {
		out = append(out, th.Fg(th.Dim, false).Render(clip(st.Key+": "+st.Help, w)))
	} else {
		out = append(out, th.Fg(th.Dim, false).Render(st.Key))
	}
	return out
}

// shown hides a password-like setting's value.
func shown(st admin.Setting, v string) string {
	if st.Kind == admin.KindPassword && v != "" {
		return "••••••"
	}
	return v
}

func unitText(u string) string {
	if u == "" {
		return ""
	}
	return " " + u
}

// appliedLines show the last apply: each old value struck through as its set is confirmed, the new one typed in.
func (m *Model) appliedLines(sess *admin.Session, w int) []string {
	th, a, now := m.th, &m.adm, m.now
	if len(a.applied) == 0 || now.Sub(a.appliedAt) > time.Minute {
		return nil
	}
	var out []string
	for _, ch := range a.applied {
		v := sess.Settings[ch.Setting.Key]
		from, to := shown(ch.Setting, ch.From), shown(ch.Setting, ch.To)
		line := th.Fg(th.Text, false).Render(fmt.Sprintf("  %-24s ", clip(ch.Setting.Label, 24)))
		switch {
		case v.At.Before(a.appliedAt) || v.Loading:
			line += th.Fg(th.Dim, false).Render(from) + " " + th.Fg(th.Yellow, false).Render(motion.RampBar(now, 3))
		case v.Err != "":
			line += th.Fg(th.Red, true).Render("[FAIL] ") + th.Fg(th.Red, false).Render(v.Err)
		default:
			typed := to
			if m.level.Allows(motion.Typing) {
				typed, _ = motion.Typewriter(to, v.At, applyCPS, now)
			}
			line += th.Fg(th.Dim, false).Strikethrough(true).Render(from) + th.Fg(th.Dim, false).Render(" → ") + th.Fg(th.Green, true).Render(typed)
		}
		out = append(out, clip(line, w))
	}
	return append(out, "")
}

func (m *Model) neighbourLines(sess *admin.Session, w, rows int) []string {
	th := m.th
	if sess.NeighbourAt.IsZero() {
		if m.pendingJob(sess.Key, admin.JobNeighbours) || m.admin.Queued() > 0 {
			return []string{m.waiting("asking for its neighbours…")}
		}
		return []string{th.Fg(th.Dim, false).Render("Press u to ask which repeaters it hears directly.")}
	}
	out := []string{th.Fg(th.Muted, true).Render(fmt.Sprintf("  %-24s %-14s %-8s %s", "NEIGHBOUR", "KEY", "SNR", "HEARD"))}
	if len(sess.Neighbours) == 0 {
		out = append(out, th.Fg(th.Dim, false).Render("  It hasn't heard any other repeaters directly."))
	}
	for i, n := range sess.Neighbours {
		if len(out) >= rows-2 {
			break
		}
		marker := "  "
		if i == m.adm.nbSel {
			marker = th.Fg(th.Magenta, true).Render("▌") + " "
		}
		out = append(out, clip(marker+th.Fg(th.Text, false).Render(col(n.Name, 24)+" ")+
			th.Fg(th.Muted, false).Render(fmt.Sprintf("%-14s ", radio.KeyHex(n.Prefix)))+
			th.Fg(th.SNR(float64(n.SNR)), true).Render(fmt.Sprintf("%+-8.1f", n.SNR))+th.Fg(th.Dim, false).Render(ago(n.Heard)), w))
	}
	return append(out, "", th.Fg(th.Dim, false).Render(fmt.Sprintf("%d of %d shown · fetched %s", len(sess.Neighbours), sess.NeighbourAll, ago(m.now.Sub(sess.NeighbourAt)))))
}

func (m *Model) aclLines(sess *admin.Session, w, rows int) []string {
	th, a := m.th, &m.adm
	if sess.ACLAt.IsZero() {
		if m.pendingJob(sess.Key, admin.JobACL) || m.admin.Queued() > 0 {
			return []string{m.waiting("reading the access list…")}
		}
		return []string{th.Fg(th.Dim, false).Render("Press u to read who has access.")}
	}
	out := []string{th.Fg(th.Muted, true).Render(fmt.Sprintf("  %-24s %-14s %s", "WHO", "KEY", "LEVEL"))}
	for i, r := range sess.ACL {
		if len(out) >= rows-2 {
			break
		}
		line := fmt.Sprintf(" %s %-14s %s", col(r.Name, 24), radio.KeyHex(r.Prefix[:]), admin.LevelName(r.Level))
		if i == a.aclSel {
			out = append(out, th.Fg(th.Magenta, true).Render("▌")+m.raised(fit(line, w-1)))
			continue
		}
		colour := th.Text
		if r.Level == 3 {
			colour = th.Yellow
		}
		out = append(out, " "+th.Fg(colour, false).Render(line))
	}
	return append(out, "", th.Fg(th.Dim, false).Render("Changing a level needs their full key, so they must be one of your contacts. Guests aren't listed."))
}

func (m *Model) regionLines(sess *admin.Session, w, rows int) []string {
	th, a := m.th, &m.adm
	if sess.RegionsAt.IsZero() {
		if sess.Busy() {
			return []string{m.waiting("reading regions…")}
		}
		return []string{th.Fg(th.Dim, false).Render("Press u to read the region tree.")}
	}
	a.regSel = min(a.regSel, max(len(sess.Regions)-1, 0))
	var out []string
	for i, r := range sess.Regions {
		if len(out) >= rows-3 {
			break
		}
		flood := th.Fg(th.Green, false).Render("flood allowed")
		if !r.Flood {
			flood = th.Fg(th.Red, false).Render("flood denied")
		}
		home := ""
		if r.Home {
			home = th.Fg(th.Yellow, true).Render("  ⌂ home")
		}
		branch := strings.Repeat("  ", r.Depth)
		if r.Depth > 0 {
			branch = strings.Repeat("  ", r.Depth-1) + "└ "
		}
		name := " " + col(branch+r.Name, 22)
		if i == a.regSel {
			out = append(out, clip(th.Fg(th.Magenta, true).Render("▌")+m.raised(name)+" "+flood+home, w))
			continue
		}
		out = append(out, clip(" "+th.Fg(th.Text, false).Render(name)+" "+flood+home, w))
	}
	note := "fetched " + ago(m.now.Sub(sess.RegionsAt)) + " · changes are lost on reboot until saved with s"
	if sess.Busy() {
		note = motion.RampBar(m.now, 3) + " " + note
	}
	return append(out, "", th.Fg(th.Dim, false).Render(clip(note, w)))
}

func (m *Model) telemetryLines(sess *admin.Session, w int) []string {
	th := m.th
	if sess.TelemetryAt.IsZero() {
		if m.pendingJob(sess.Key, admin.JobTelemetry) || m.admin.Queued() > 0 {
			return []string{m.waiting("asking for telemetry…")}
		}
		return []string{th.Fg(th.Dim, false).Render("Press u to ask for its sensor readings.")}
	}
	var out []string
	for _, r := range sess.Telemetry {
		line := th.Fg(th.Dim, false).Render(fmt.Sprintf("  %-24s ", clip(r.Label, 24))) + th.Fg(th.Text, true).Render(fmt.Sprintf("%-12s ", r.Value))
		if r.IsNum {
			line += m.spark(sess.TelemetryLog[r.Label], min(32, w/3), sess.TelemetryAt)
		}
		out = append(out, clip(line, w))
	}
	if len(out) == 0 {
		out = append(out, th.Fg(th.Dim, false).Render("  It sent no readings."))
	}
	return append(out, "", th.Fg(th.Dim, false).Render("fetched "+ago(m.now.Sub(sess.TelemetryAt))))
}

func (m *Model) logLines(sess *admin.Session, w, rows int) []string {
	th := m.th
	if m.adm.local && sess.PacketLog != nil {
		out := m.packetLogLines(sess, w, rows-1)
		if sess.Busy() {
			out = append([]string{m.waiting("reading the log again…")}, out...)
		}
		return out
	}
	read := "Reading the log needs a USB cable to the node (repeater mode on the connect screen). Over the air you can start, stop and erase it."
	if m.adm.local {
		read = "Connected by USB: r reads the log and sums it up here, by packet type, route, signal and hour."
	}
	out := []string{th.Fg(th.Text, false).Render("The node can keep a log of packets it hears, in its own storage."), "",
		th.Fg(th.Dim, false).Render(clip(read, w)), ""}
	if sess.Busy() && m.adm.local {
		out = append(out, m.waiting("reading the log…"), "")
	}
	for _, l := range sess.Console {
		if strings.HasPrefix(l.Text, "log ") || strings.Contains(l.Text, "logging") || strings.Contains(l.Text, "log erased") {
			out = append(out, m.consoleRow(l, w))
		}
	}
	return out
}

func (m *Model) consoleRow(l admin.Line, w int) string {
	th := m.th
	stamp := th.Fg(th.Dim, false).Render(l.At.Format("15:04:05") + " ")
	switch {
	case l.Out:
		return clip(stamp+th.Fg(th.Magenta, true).Render("▸ ")+th.Fg(th.Cyan, false).Render(l.Text), w)
	case l.Error:
		return clip(stamp+th.Fg(th.Red, false).Render("  "+l.Text), w)
	}
	return clip(stamp+th.Fg(th.Text, false).Render("  "+l.Text), w)
}

func (m *Model) consoleLines(sess *admin.Session, w, rows int) []string {
	th := m.th
	var lines []string
	for _, l := range sess.Console {
		for i, part := range strings.Split(l.Text, "\n") {
			if strings.TrimSpace(part) == "" && i > 0 {
				continue
			}
			ll := l
			ll.Text = part
			if i > 0 {
				ll.At, ll.Out = l.At, false
			}
			lines = append(lines, m.consoleRow(ll, w))
		}
	}
	room := rows - 3
	if len(lines) > room {
		lines = lines[len(lines)-room:]
	}
	if len(lines) == 0 {
		lines = append(lines, th.Fg(th.Dim, false).Render("Commands go to the node as you'd type them on its serial console. Try ver, get name, or neighbors."))
	}
	prompt := th.Fg(th.Magenta, true).Render(strings.ToLower(clip(sess.Name, 16)) + "▸ ")
	prompt += m.adm.console.render(th, w-visibleWidth(prompt), false)
	if sess.Busy() {
		prompt += "  " + m.waiting("waiting for a reply")
	}
	return append(lines, "", clip(prompt, w))
}

// clip shortens styled text to w cells, keeping its colours.
func clip(s string, w int) string { return ansi.Truncate(s, max(w, 0), "…") }

// roomLine says whether you're logged in to the room whose conversation is open.
func (m *Model) roomLine(w int) string {
	th := m.th
	ct, ok := m.selectedRoom()
	if !ok {
		return ""
	}
	sess, has := m.admin.Find(ct.Key)
	if has && sess.State == admin.LoggedIn {
		return clip(m.authBadge(sess, true)+th.Fg(th.Dim, false).Render("  new posts arrive as the room pushes them"), w)
	}
	if has && sess.State == admin.LoggingIn {
		return clip(m.authBadge(sess, true), w)
	}
	return clip(th.Fg(th.Yellow, false).Render("Not logged in: the room won't send you posts or accept yours. ")+th.Fg(th.Cyan, true).Render("L")+th.Fg(th.Dim, false).Render(" logs in"), w)
}
