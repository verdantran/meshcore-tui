package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
)

// pruneEvery is how often old messages are looked for while the app runs.
const pruneEvery = 24 * time.Hour

type pruner struct {
	gen  int
	stop context.CancelFunc // the running prune's; nil when none is
	last time.Time
}

type pruneDoneMsg struct {
	gen    int
	days   int
	cutoff time.Time
	n      int
	err    error
}

func (m *Model) keepDays() int { return m.adminConfig().Messages.KeepDays }

func keepCutoff(now time.Time, days int) time.Time {
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}

func dayCount(n int) string {
	if n == 1 {
		return "1 day"
	}
	return commas(n) + " days"
}

// pruneDue starts deleting messages older than the history setting, off the UI goroutine, once a radio is
// ready and then once a day.
func (m *Model) pruneDue() tea.Cmd {
	p := &m.prune
	days := m.keepDays()
	if m.chat == nil || m.chat.Device() == "" || days <= 0 || p.stop != nil || !p.last.IsZero() && m.now.Sub(p.last) < pruneEvery {
		return nil
	}
	ctx, stop := context.WithCancel(context.Background())
	p.gen++
	p.stop, p.last = stop, m.now
	cutoff := keepCutoff(m.now, days)
	prune, gen := m.chat.PruneLater(cutoff), p.gen
	return func() tea.Msg {
		n, err := prune(ctx)
		return pruneDoneMsg{gen, days, cutoff, n, err}
	}
}

func (m *Model) stopPrune() {
	if m.prune.stop != nil {
		m.prune.stop()
		m.prune.stop = nil
	}
}

func (m *Model) pruneDone(msg pruneDoneMsg) {
	if msg.gen == m.prune.gen {
		m.prune.stop = nil
	}
	if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
		m.addLog(m.now, "HISTORY NOT PRUNED  "+msg.err.Error(), radio.ToneWarn)
	}
	if msg.n == 0 || m.chat == nil {
		return
	}
	m.addLog(m.now, fmt.Sprintf("PRUNED  %s messages older than %s", commas(msg.n), dayCount(msg.days)), radio.ToneInfo)
	m.chat.Pruned()
	reload := false
	for _, x := range m.chats.msgs {
		if x.RecvAt.Before(msg.cutoff) && !(x.Out && (x.State == store.Pending || x.State == store.Sent)) {
			reload = true
			break
		}
	}
	m.rebuildChats(reload)
}

func (m *Model) setKeepDays(n int) tea.Cmd {
	m.adminConfig().Messages.KeepDays = n
	m.stopPrune()
	m.prune.last = time.Time{}
	m.addLog(m.now, "HISTORY  "+keeping(n), radio.ToneInfo)
	if m.opt.Config != nil {
		m.saveConfig()
	}
	return m.pruneDue()
}

func keeping(days int) string {
	if days == 0 {
		return "keeping every message"
	}
	return "keeping " + dayCount(days) + " of messages"
}

func historyCommand() command {
	return command{name: "history", args: "[keep <days>|all]", help: "say how long messages are kept, or set it; older ones are deleted",
		run: func(m *Model, a []string) error {
			if len(a) == 0 {
				m.addLog(m.now, "HISTORY  "+keeping(m.keepDays()), radio.ToneInfo)
				return nil
			}
			if len(a) != 2 || a[0] != "keep" {
				return fmt.Errorf("history keep takes a number of days, or all")
			}
			days := 0
			if a[1] != "all" {
				n, err := strconv.Atoi(a[1])
				if err != nil {
					return fmt.Errorf("history keep takes a number of days, or all")
				}
				if err := config.CheckKeepDays(n); err != nil {
					return fmt.Errorf("history keep: %w", err)
				}
				days = n
			}
			if days > 0 && m.chat != nil {
				count, err := m.chat.CountOlder(keepCutoff(m.now, days))
				if err != nil {
					return err
				}
				if count > 0 {
					m.ask(fmt.Sprintf("delete %s messages older than %s?", commas(count), dayCount(days)), "delete", func() tea.Cmd { return m.setKeepDays(days) })
					return nil
				}
			}
			m.queued = m.setKeepDays(days)
			return nil
		},
		complete: func(m *Model) []string {
			if f := strings.Fields(m.pal.input.text); len(f) >= 2 && f[1] == "keep" && (len(f) > 2 || strings.HasSuffix(m.pal.input.text, " ")) {
				return []string{"all", "30", "90", "365"}
			}
			return []string{"keep"}
		}}
}
