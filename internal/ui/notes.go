package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/config"
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

// noteSummaryMsg comes when the notifications held back by the limit can go out as one.
type noteSummaryMsg struct{}

type notifier struct {
	method  notify.Method
	show    string // message, sender or nothing: how much of it leaves the app
	bell    bool
	events  []string
	env     func(string) string
	pending []note
	waiting bool

	limit   int         // notifications a minute; 0 is no limit
	sent    []time.Time // those that went out in the last minute
	held    []note      // over the limit, for the summary
	summing bool        // a summary is due
	muted   []config.Mute
}

func newNotifier(o Options) notifier {
	n := notifier{method: notify.Resolve("auto", o.Env, o.Remote), show: "message", bell: true, events: []string{"dm", "mention"}, env: o.Env,
		limit: config.Default().Notifications.PerMin}
	if c := o.Config; c != nil {
		n.method = notify.Resolve(c.Notifications.Desktop, o.Env, o.Remote)
		if slices.Contains(showLevels, c.Notifications.Show) {
			n.show = c.Notifications.Show
		}
		n.bell = c.Notifications.Bell
		n.events = c.Notifications.Events
		n.limit = c.Notifications.PerMin
		n.muted = slices.Clone(c.Notifications.Muted)
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
	if _, ok := m.mutedUntil(msg.Convo, title); ok {
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
	notes := n.pending
	n.pending = nil
	title, body := gather(notes, "new")
	m.pushToast(title, body)
	if n.method == notify.Off {
		return nil
	}
	if len(n.held) > 0 || !n.room(m.now) {
		n.held = append(n.held, notes...)
		return n.summaryDue(m.now)
	}
	n.sent = append(n.sent, m.now)
	return m.sendNote(n.private(title, body, len(notes), "new"))
}

// summariseNotes sends what the limit held back as one notification, once the minute has room again.
func (m *Model) summariseNotes() tea.Cmd {
	n := &m.notes
	n.summing = false
	if len(n.held) == 0 {
		return nil
	}
	if !n.room(m.now) {
		return n.summaryDue(m.now)
	}
	title, body := gather(n.held, "more")
	title, body = n.private(title, body, len(n.held), "more")
	n.held = nil
	n.sent = append(n.sent, m.now)
	return m.sendNote(title, body)
}

// room reports whether another notification fits in the last minute, forgetting those older than that.
func (n *notifier) room(now time.Time) bool {
	n.sent = slices.DeleteFunc(n.sent, func(t time.Time) bool { return now.Sub(t) >= time.Minute })
	return n.limit <= 0 || len(n.sent) < n.limit
}

func (n *notifier) summaryDue(now time.Time) tea.Cmd {
	if n.summing {
		return nil
	}
	n.summing = true
	wait := time.Second
	if len(n.sent) > 0 {
		wait = max(n.sent[0].Add(time.Minute).Sub(now), wait)
	}
	return tea.Tick(wait, func(time.Time) tea.Msg { return noteSummaryMsg{} })
}

// gather is the title and body for some messages: the one, or how many ("new" or "more") and from whom.
func gather(notes []note, which string) (title, body string) {
	if len(notes) == 1 && which == "new" {
		return notes[0].title, notes[0].body
	}
	var who []string
	for _, p := range notes {
		if !slices.Contains(who, p.title) {
			who = append(who, p.title)
		}
	}
	return plural(len(notes), which+" message"), strings.Join(who, ", ")
}

// private keeps back what the show setting says shouldn't leave the app.
func (n *notifier) private(title, body string, count int, which string) (string, string) {
	one := count == 1 && which == "new"
	switch {
	case n.show == "nothing" && one:
		return "meshcore-tui", "New message"
	case n.show == "nothing":
		return "meshcore-tui", plural(count, which+" message")
	case n.show == "sender" && one:
		body = "New message"
	}
	return "meshcore-tui · " + title, body
}

var showLevels = []string{"message", "sender", "nothing"}

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
	return command{name: "notify", args: "[show message|sender|nothing]", help: "send a test notification, or choose how much of a message reaches the desktop and lock screen",
		run: func(m *Model, a []string) error {
			if len(a) > 0 {
				if len(a) != 2 || a[0] != "show" || !slices.Contains(showLevels, a[1]) {
					return fmt.Errorf("notify show takes message, sender or nothing")
				}
				m.notes.show = a[1]
				m.addLog(m.now, "NOTIFICATIONS  show "+a[1], radio.ToneInfo)
				if m.opt.Config != nil {
					m.opt.Config.Notifications.Show = a[1]
					m.saveConfig()
				}
				return nil
			}
			if m.notes.method == notify.Off {
				return fmt.Errorf("notifications are off; set desktop in [notifications] to turn them on")
			}
			m.queued = m.sendNote("meshcore-tui · test", "If you can see or hear this, notifications work.")
			return nil
		}}
}
