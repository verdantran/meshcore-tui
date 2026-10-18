package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/notify"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
)

// Notifications that land close together are grouped into one alert.
const noteGather = 1200 * time.Millisecond

type note struct {
	title, body string
}

type notifyFlushMsg struct{}

type notifier struct {
	method  notify.Method
	bell    bool
	events  []string
	env     func(string) string
	pending []note
	waiting bool
}

func newNotifier(o Options) notifier {
	n := notifier{method: notify.Resolve("auto", o.Env), bell: true, events: []string{"dm", "mention"}, env: o.Env}
	if c := o.Config; c != nil {
		n.method = notify.Resolve(c.Notifications.Desktop, o.Env)
		n.bell = c.Notifications.Bell
		n.events = c.Notifications.Events
	}
	return n
}

// considerNote queues a notification for a received message unless you're already looking at it.
func (m *Model) considerNote(msg store.Message, title string) tea.Cmd {
	n := &m.notes
	channel := strings.HasPrefix(string(msg.Convo), "ch:")
	mention := m.chat.Mentions(msg.Text)
	if channel && !(mention && slices.Contains(n.events, "mention")) || !channel && !slices.Contains(n.events, "dm") {
		return nil
	}
	if cur, ok := m.selectedConvo(); ok && m.focused && m.phase == phaseMain && m.visible(0) && cur.Convo == msg.Convo {
		return nil
	}
	head := msg.Sender
	if channel {
		head = msg.Sender + " in " + title
	}
	n.pending = append(n.pending, note{title: head, body: msg.Text})
	if n.waiting {
		return nil
	}
	n.waiting = true
	return tea.Tick(noteGather, func(time.Time) tea.Msg { return notifyFlushMsg{} })
}

func (m *Model) flushNotes() tea.Cmd {
	n := &m.notes
	n.waiting = false
	if len(n.pending) == 0 {
		return nil
	}
	title, body := "meshcore-tui · "+n.pending[0].title, n.pending[0].body
	if len(n.pending) > 1 {
		var who []string
		for _, p := range n.pending {
			if !slices.Contains(who, p.title) {
				who = append(who, p.title)
			}
		}
		title = fmt.Sprintf("meshcore-tui · %d new messages", len(n.pending))
		body = strings.Join(who, ", ")
	}
	n.pending = nil
	m.pushToast(strings.TrimPrefix(title, "meshcore-tui · "), body)
	if n.method == notify.Off {
		return nil
	}
	return m.sendNote(title, body)
}

// sendNote writes a notification now, and logs it, so it's visible even if the terminal swallows it.
func (m *Model) sendNote(title, body string) tea.Cmd {
	n := &m.notes
	m.addLog(m.now, "NOTIFIED  "+strings.TrimPrefix(title, "meshcore-tui · ")+" · via "+n.describe(), radio.ToneInfo)
	cmds := []tea.Cmd{tea.Raw(notify.Escape(n.method, title, body, n.bell, n.env))}
	if n.method == notify.OS {
		cmds = append(cmds, func() tea.Msg { notify.Desktop(title, body); return nil })
	}
	return tea.Batch(cmds...)
}

func (n *notifier) describe() string {
	how := map[notify.Method]string{notify.Terminal: "terminal escape", notify.OS: "system notification", notify.BellOnly: "bell only", notify.Off: "nothing (off)"}[n.method]
	if n.bell && n.method != notify.Off && n.method != notify.BellOnly {
		how += " + bell"
	}
	return how
}

func notifyCommand() command {
	return command{name: "notify", help: "send a test notification, to check your terminal shows it",
		run: func(m *Model, _ []string) error {
			if m.notes.method == notify.Off {
				return fmt.Errorf("notifications are off; set desktop in [notifications] to turn them on")
			}
			m.queued = m.sendNote("meshcore-tui · test", "If you can see or hear this, notifications work.")
			return nil
		}}
}
