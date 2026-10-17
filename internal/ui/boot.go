package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
)

// expectedSteps sizes the boot progress bar; real links send the same sequence.
const (
	expectedSteps   = 8
	expectedConsole = 3
)

const bootCPS = 90.0

type bootLine struct {
	key, label, detail string
	state              radio.StepState
	at, doneAt         time.Time
}

type bootModel struct {
	start    time.Time
	target   Target
	lines    []bootLine
	failed   bool
	err      error
	finished time.Time
}

func newBoot(now time.Time, t Target) bootModel { return bootModel{start: now, target: t} }

func (b *bootModel) step(e radio.BootStep, now time.Time) {
	for i := range b.lines {
		if b.lines[i].key == e.Key {
			l := &b.lines[i]
			l.state = e.State
			if e.Detail != "" {
				l.detail = e.Detail
			}
			if e.State != radio.StepWait {
				l.doneAt = now
			}
			if e.State == radio.StepFail {
				b.failed = true
			}
			return
		}
	}
	b.lines = append(b.lines, bootLine{key: e.Key, label: e.Label, detail: e.Detail, state: e.State, at: now})
	if e.State != radio.StepWait {
		b.lines[len(b.lines)-1].doneAt = now
	}
}

func (b *bootModel) done(now time.Time) { b.finished = now }

func (b *bootModel) typing(level motion.Level, now time.Time) bool {
	if !level.Allows(motion.Typing) {
		return false
	}
	for _, l := range b.lines {
		full := fmt.Sprintf("▸ %-10s %s", l.label, l.detail)
		if motion.Running(l.at, motion.TypeDur(full, bootCPS), now) {
			return true
		}
	}
	return false
}

// typedBy is when the last boot line finishes typing out.
func (b *bootModel) typedBy(level motion.Level) time.Time {
	var end time.Time
	for _, l := range b.lines {
		t := l.at
		if level.Allows(motion.Typing) {
			t = t.Add(motion.TypeDur(fmt.Sprintf("▸ %-10s %s", l.label, l.detail), bootCPS))
		}
		if t.After(end) {
			end = t
		}
	}
	return end
}

func (b *bootModel) fail(err error, now time.Time) {
	b.failed, b.err = true, err
	if b.finished.IsZero() {
		b.finished = now
	}
}

func (b *bootModel) completed() int {
	n := 0
	for _, l := range b.lines {
		if l.state == radio.StepOK {
			n++
		}
	}
	return n
}

// render draws the boot log as a centred panel; it types each line as it appears.
func (m *Model) renderBoot() []string {
	th, now, b := m.th, m.now, &m.boot
	width := min(max(m.w-8, 40), 76)
	inner := width - 4
	var body []string

	body = append(body, "")
	for _, l := range b.lines {
		labelW := 10
		full := fmt.Sprintf("▸ %-*s %s", labelW, l.label, l.detail)
		shown, typed := full, true
		if m.level.Allows(motion.Typing) {
			shown, typed = motion.Typewriter(full, l.at, bootCPS, now)
		}
		var tag string
		switch {
		case !typed:
			tag = th.Fg(th.Text, true).Render("█")
		case l.state == radio.StepWait:
			ramp := "…"
			if m.level.Allows(motion.Ramp) {
				ramp = string(motion.RampRune(now))
			}
			tag = th.Fg(th.Yellow, true).Render(ramp + " [WAIT]")
		case l.state == radio.StepOK:
			tag = th.Fg(th.Green, true).Render("[ OK ]")
		default:
			tag = th.Fg(th.Red, true).Render("[FAIL]")
		}
		text := shown
		if l.state == radio.StepFail && m.level.Allows(motion.Glitch) && motion.Running(m.glitch, glitchDur*3, now) {
			text = motion.GlitchText(shown, l.at.UnixNano(), int(now.Sub(m.glitch)/motion.FrameDur))
		}
		textStyle := th.Fg(th.Muted, false)
		if l.state == radio.StepFail {
			textStyle = th.Fg(th.Red, false)
		}
		label := textStyle.Render(text)
		if pct, cut, ok := m.batteryPercent(l, typed, text); ok {
			label = textStyle.Render(text[:cut+1]) + batteryIcon(th, pct, m.plainPaint) + textStyle.Render(fmt.Sprintf(" %d%%", pct))
			if m.chemistry(m.ready.Self.PubKey) == radio.Wired {
				label = textStyle.Render(strings.TrimSuffix(text, l.detail) + "wired")
			}
		}
		gap := inner - visibleWidth(label) - visibleWidth(tag)
		dots := ""
		if typed && gap > 2 {
			dots = th.Fg(th.Line, false).Render(" " + strings.Repeat("·", gap-2) + " ")
		} else if gap > 0 {
			dots = strings.Repeat(" ", gap)
		}
		body = append(body, label+dots+tag)
	}

	body = append(body, "")
	expected := expectedSteps
	if b.target.Mode == ModeRepeater {
		expected = expectedConsole
	}
	filled := min(inner*b.completed()/expected, inner)
	if !b.finished.IsZero() && !b.failed {
		filled = inner
	}
	bar := th.Fg(th.Cyan, true).Render(strings.Repeat("█", filled)) + th.Fg(th.Line, false).Render(strings.Repeat("░", inner-filled))
	body = append(body, bar)
	switch {
	case b.failed:
		msg := "connection failed"
		if b.err != nil {
			msg = b.err.Error()
		}
		body = append(body, "")
		for i, l := range strings.Split(ansi.Wrap(msg, inner-2, " "), "\n") {
			lead := "  "
			if i == 0 {
				lead = "✕ "
			}
			body = append(body, th.Fg(th.Red, true).Render(lead+l))
		}
		hint := "r retry · q quit"
		if m.opt.Ports != nil {
			hint = "r retry · c choose another link · q quit"
		}
		body = append(body, th.Fg(th.Dim, false).Render(hint))
	case !b.finished.IsZero():
		body = append(body, "", th.Fg(th.Green, true).Render("● LINK ESTABLISHED"))
	default:
		body = append(body, "", th.Fg(th.Dim, false).Render(fmt.Sprintf("linking %s %s…", strings.ToLower(b.target.Kind), b.target.Addr)))
	}

	border := th.Magenta
	if b.failed {
		border = th.Red
	}
	box := m.panel("LINK SEQUENCE", body, width, len(body)+2, border, true)
	logo := m.logo(b.start)
	block := append(logo, "")
	block = append(block, box...)
	return centre(block, m.w, m.h)
}

// logo is the wordmark, decoded in from noise when a screen opens.
func (m *Model) logo(opened time.Time) []string {
	th := m.th
	word := "M E S H C O R E ─ T U I"
	if m.level.Allows(motion.Decode) {
		word = motion.DecodeText(word, 0x5eed, motion.Progress(opened, logoDecodeDur, m.now))
	}
	rule := strings.Repeat("━", len([]rune(word)))
	return []string{
		th.Fg(th.Magenta, true).Render(word),
		th.Fg(th.Cyan, false).Render(rule),
		th.Fg(th.Dim, false).Render("companion radio console"),
	}
}

// batteryPercent finds the percentage at the end of a typed POWER line, so the icon can go just before it.
// The radio worked it out as Li-ion; it's read again from the voltage for the battery set for this radio.
func (m *Model) batteryPercent(l bootLine, typed bool, text string) (pct, at int, ok bool) {
	if l.key != "power" || !typed || l.state != radio.StepOK {
		return 0, 0, false
	}
	at = strings.LastIndex(text, " ")
	if at < 0 {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(text[at+1:], "%d%%", &pct); err != nil {
		return 0, 0, false
	}
	var v float64
	if _, err := fmt.Sscanf(l.detail, "%f V", &v); err == nil {
		pct = radio.Battery{MilliVolts: uint16(math.Round(v * 1000))}.PercentOf(m.chemistry(m.ready.Self.PubKey))
	}
	return pct, at, true
}
