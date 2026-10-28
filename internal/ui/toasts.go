package ui

import (
	"strings"
	"time"

	"github.com/verdantran/meshcore-tui/internal/motion"
)

const (
	toastFor   = 4 * time.Second
	toastSlide = 260 * time.Millisecond
	toastMax   = 3
)

// toast is a notification shown in the app, top right, until its timer bar runs out.
type toast struct {
	title, body string
	at          time.Time
}

func (m *Model) pushToast(title, body string) {
	live := m.toasts[:0]
	for _, t := range m.toasts {
		if m.now.Sub(t.at) < toastFor {
			live = append(live, t)
		}
	}
	m.toasts = append(live, toast{oneLine(title), oneLine(body), m.now})
	if len(m.toasts) > toastMax {
		m.toasts = m.toasts[len(m.toasts)-toastMax:]
	}
}

func (m *Model) toasting() bool {
	for _, t := range m.toasts {
		if m.now.Sub(t.at) < toastFor {
			return true
		}
	}
	return false
}

// toastSliding reports a toast sliding in or out; its timer bar in between needs only the slower clock.
func (m *Model) toastSliding() bool {
	if !m.level.Allows(motion.Wipe) {
		return false
	}
	for _, t := range m.toasts {
		if age := m.now.Sub(t.at); age >= 0 && age < toastFor && (age < toastSlide || age > toastFor-toastSlide) {
			return true
		}
	}
	return false
}

// overlayToasts lays the live toasts inside the right edge of the panes, below the tabs, newest at the top.
// Each slides in from the edge and out again, and its bottom border is the time it has left.
func (m *Model) overlayToasts(lines []string) {
	th, now := m.th, m.now
	tw := min(max(m.w/3, 30), 46, m.w-4)
	right, row := m.w-2, 3 // inside the pane's border, and below its top edge
	for i := len(m.toasts) - 1; i >= 0; i-- {
		t := m.toasts[i]
		age := now.Sub(t.at)
		if age >= toastFor || age < 0 || row+4 > len(lines) {
			continue
		}
		shown := 1.0
		if m.level.Allows(motion.Wipe) {
			shown = min(motion.EaseOut(motion.Progress(t.at, toastSlide, now)), motion.EaseOut(float64(toastFor-age)/float64(toastSlide)))
		}
		x := right - int(shown*float64(tw)+0.5)
		edge, inside := th.Fg(th.Magenta, true), th.Block(th.Text, th.Raised).Bold(false)
		title := " " + truncate(t.title, tw-6) + " "
		left := 1 - min(float64(age)/float64(toastFor), 1)
		bar := int(left*float64(tw-2) + 0.5)
		box := []string{
			edge.Render("┏━") + th.Block(th.Bg, th.Magenta).Render(title) + edge.Render(strings.Repeat("━", max(tw-3-visibleWidth(title), 0))+"┓"),
			edge.Render("┃") + inside.Render(fit(" "+t.body, tw-2)) + edge.Render("┃"),
			edge.Render("┗") + edge.Render(strings.Repeat("━", bar)) + th.Fg(th.Line, false).Render(strings.Repeat("─", tw-2-bar)) + edge.Render("┛"),
		}
		for j, b := range box {
			visible := min(tw, right-x)
			if visible <= 0 {
				break
			}
			lines[row+j] = fit(lines[row+j], x) + fit(b, visible) + skip(lines[row+j], x+visible)
		}
		row += len(box)
	}
}
