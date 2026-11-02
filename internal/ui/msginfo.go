package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
)

// weakSNR is below where the app's signal colours turn red.
const weakSNR = 0

// weakMark flags a message that came in, or whose repeats came back, at a weak signal. The figures are
// kept for i.
func (m *Model) weakMark(msg store.Message) string {
	snr, has := msg.SNR, msg.HasSNR
	if msg.Out {
		snr, has = msg.EchoSNR, msg.HasEcho
	}
	if !has || snr >= weakSNR {
		return ""
	}
	return m.th.Paint(m.th.Dim, false, " · ") + m.th.Paint(m.th.Red, false, "▁ weak")
}

// infoKey handles the keys the signal panel takes while it's open, reporting whether it took this one.
func (m *Model) infoKey(s string, msg store.Message) (tea.Cmd, bool) {
	switch s {
	case "i", "esc":
		m.chats.info = false
		return nil, true
	case "P":
		m.packetsFor(msg)
		return nil, true
	}
	return nil, false
}

// packetsFor opens Packets filtered to a channel message's packets.
func (m *Model) packetsFor(msg store.Message) {
	if !msg.Convo.IsChannel() {
		m.addLog(m.now, "PACKETS  only channel messages show on Packets: a direct message's packet can't be read", radio.ToneInfo)
		return
	}
	in := &m.inspector
	in.filter, in.filtering, in.sel = lineEdit{}, false, 0
	in.filter.set(msg.Text)
	m.chats.info = false
	m.setTab(3)
}

// copies are the picked message's heard copies, read when the panel shows a message it hasn't yet.
func (m *Model) copies(id int64) []store.Copy {
	c := &m.chats
	if c.infoID != id {
		cs, err := m.chat.Copies(id)
		if err != nil {
			m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
		}
		c.infoID, c.infoCopies = id, cs
	}
	return c.infoCopies
}

// infoLines is the signal panel's body for a message.
func (m *Model) infoLines(msg store.Message) []string {
	th := m.th
	dim := func(s string) string { return th.Paint(th.Dim, false, s) }
	snr := func(v float32) string { return th.Paint(th.SNR(float64(v)), true, fmt.Sprintf("SNR %+.1f", v)) }
	rssi := func(v int) string { return th.Paint(th.Text, false, fmt.Sprintf("RSSI %d", v)) }
	cs := m.copies(msg.ID)
	var out []string
	who, at := msg.Sender, msg.RecvAt
	if msg.Out {
		who, at = "you", msg.SentAt
	}
	out = append(out, th.Paint(th.Text, true, who)+dim(" · "+at.Format("15:04:05")))

	switch {
	case !msg.Out:
		line := dim("not measured")
		if msg.HasSNR {
			line = snr(msg.SNR)
		}
		if len(cs) > 0 {
			line += dim("  ") + rssi(cs[0].RSSI)
		}
		out = append(out, line+dim("  ·  from the last hop, the node nearest you"))
		if len(cs) == 0 {
			out = append(out, dim("no RSSI: its packet wasn't heard, or it came before signals were kept"))
		}
	case msg.Convo.IsChannel():
		switch {
		case len(cs) > 0:
			out = append(out, dim(fmt.Sprintf("heard coming back from %s", plural(len(cs), "repeat"))))
		case msg.Repeats > 0:
			out = append(out, dim(fmt.Sprintf("heard %s, from before signals were kept", plural(msg.Repeats, "repeat"))))
		default:
			out = append(out, dim("no repeats heard"))
		}
	default:
		out = append(out, dim("a direct message's delivery has no signal to show: the radio reports none"))
	}

	if len(cs) > 0 && (msg.Out || len(cs) > 1) {
		out = append(out, "")
		head := "HEARD"
		if msg.Out {
			head = "REPEATS"
		}
		out = append(out, th.Paint(th.Magenta, true, head))
		for i, c := range cs {
			when := c.At.Format("15:04:05")
			if msg.Out {
				when = fmt.Sprintf("+%.1fs", c.At.Sub(msg.SentAt).Seconds())
			}
			note := ""
			if !msg.Out && i == 0 {
				note = dim("  the way it came")
			}
			out = append(out, fmt.Sprintf("%s  %s  %s  %s%s", dim(fmt.Sprintf("%-8s", when)), dim(fmt.Sprintf("%-9s", lastHop(c))),
				padTo(snr(c.SNR), 9), rssi(c.RSSI), note))
		}
	}
	keys := "i close"
	if msg.Convo.IsChannel() {
		keys += " · P its packets"
	}
	return append(out, "", dim(keys))
}

// lastHop names the node a copy was heard from: the last repeater on its path, or the sender itself.
func lastHop(c store.Copy) string {
	if c.PathHash <= 0 || len(c.Path) < c.PathHash {
		return "direct"
	}
	return fmt.Sprintf("via %X", c.Path[len(c.Path)-c.PathHash:])
}

func padTo(s string, w int) string { return s + strings.Repeat(" ", max(w-visibleWidth(s), 0)) }

// overlayInfo draws the picked message's signal panel over the middle of the screen.
func (m *Model) overlayInfo(lines []string) {
	c := &m.chats
	if !c.info || !c.reading || !m.visible(0) || m.asking != nil {
		return
	}
	i := m.picked()
	if i < 0 {
		c.info = false
		return
	}
	body := m.infoLines(c.msgs[i])
	w := min(m.w-4, 64)
	for _, l := range body {
		w = min(max(w, visibleWidth(l)+4), m.w)
	}
	if w < 30 || len(body)+2 > len(lines) {
		return
	}
	box := m.panel("SIGNAL", body, w, len(body)+2, m.th.Magenta, true)
	x, top := (m.w-w)/2, (len(lines)-len(box))/2
	for j, b := range box {
		head, _ := take(lines[top+j], x, "")
		lines[top+j] = head + "\x1b[0m" + b + skip(lines[top+j], x+w)
	}
}
