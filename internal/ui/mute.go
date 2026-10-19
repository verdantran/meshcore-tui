package ui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
)

// muteFor is how a conversation is kept in [notifications] muted: a channel by name, anyone else by key.
func muteFor(c store.Convo, title string) config.Mute {
	if c.IsChannel() {
		return config.Mute{Channel: title}
	}
	_, key, _ := strings.Cut(string(c), ":")
	return config.Mute{Contact: key}
}

// sameMute reports whether two mutes are for the same conversation. A sender known only by their key's
// first bytes matches the whole key.
func sameMute(a, b config.Mute) bool {
	if a.Channel != "" || b.Channel != "" {
		return a.Channel == b.Channel
	}
	short, long := a.Contact, b.Contact
	if len(short) > len(long) {
		short, long = long, short
	}
	return len(short) >= 12 && strings.EqualFold(short, long[:len(short)])
}

// mutedUntil reports whether a conversation is muted now, and until when; zero is until it's unmuted.
func (m *Model) mutedUntil(c store.Convo, title string) (time.Time, bool) {
	if len(m.notes.muted) == 0 {
		return time.Time{}, false
	}
	want := muteFor(c, title)
	for _, mu := range m.notes.muted {
		if sameMute(mu, want) && (mu.Until.IsZero() || m.now.Before(mu.Until)) {
			return mu.Until, true
		}
	}
	return time.Time{}, false
}

// setMute mutes a conversation until a time (zero for until unmuted), or unmutes it, and saves the change.
func (m *Model) setMute(c store.Convo, title string, on bool, until time.Time) {
	n := &m.notes
	want := muteFor(c, title)
	n.muted = slices.DeleteFunc(n.muted, func(mu config.Mute) bool {
		return sameMute(mu, want) || !mu.Until.IsZero() && !m.now.Before(mu.Until)
	})
	name := m.convoLabel(c, title)
	if on {
		want.Until = until
		n.muted = append(n.muted, want)
		m.addLog(m.now, "MUTED  "+name+" "+m.untilText(until)+" · messages still arrive, without notifications", radio.ToneInfo)
	} else {
		m.addLog(m.now, "UNMUTED  "+name, radio.ToneInfo)
	}
	if m.opt.Config != nil {
		m.opt.Config.Notifications.Muted = slices.Clone(n.muted)
		m.saveConfig()
	}
}

func (m *Model) untilText(until time.Time) string {
	switch {
	case until.IsZero():
		return "until unmuted"
	case until.Sub(m.now) < 24*time.Hour:
		return "until " + until.Format("15:04")
	}
	return "until " + until.Format("Mon 2 Jan 15:04")
}

// toggleMute mutes the open conversation until it's unmuted, or unmutes it.
func (m *Model) toggleMute() {
	cur, ok := m.selectedConvo()
	if !ok {
		return
	}
	_, muted := m.mutedUntil(cur.Convo, cur.Title)
	m.setMute(cur.Convo, cur.Title, !muted, time.Time{})
}

// muteLength reads a length of time to mute for: Go's 8h or 90m, or days as 2d.
func muteLength(s string) (time.Duration, bool) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		return time.Duration(n) * 24 * time.Hour, err == nil && n > 0 && n <= config.MaxKeepDays
	}
	d, err := time.ParseDuration(s)
	return d, err == nil && d > 0
}

// muteTarget finds the conversation a mute command names, or the open one when it names none.
func (m *Model) muteTarget(a []string) (store.Convo, string, error) {
	if m.chat == nil {
		return "", "", fmt.Errorf("no conversations yet")
	}
	if len(a) == 0 {
		cur, ok := m.selectedConvo()
		if !ok {
			return "", "", fmt.Errorf("no conversation is open; name one")
		}
		return cur.Convo, cur.Title, nil
	}
	want := strings.ToLower(strings.TrimPrefix(a[0], "#"))
	type found struct {
		c     store.Convo
		title string
	}
	var hits []found
	for _, info := range m.chats.infos {
		if slug(info.Title) == want {
			hits = append(hits, found{info.Convo, info.Title})
		}
	}
	for _, ct := range m.chat.Contacts() {
		c := store.ContactConvo(ct.Key)
		if slug(ct.Name) == want && (ct.Type == radio.TypeChat || ct.Type == radio.TypeRoom) &&
			!slices.ContainsFunc(hits, func(f found) bool { return f.c == c }) {
			hits = append(hits, found{c, ct.Name})
		}
	}
	switch len(hits) {
	case 0:
		return "", "", fmt.Errorf("no conversation or contact called %q", a[0])
	case 1:
		return hits[0].c, hits[0].title, nil
	}
	var names []string
	for _, h := range hits {
		names = append(names, m.convoLabel(h.c, h.title))
	}
	return "", "", fmt.Errorf("%q could be %s; open the one you mean in Chats and press m", a[0], strings.Join(names, " or "))
}

func muteCommands() []command {
	complete := func(m *Model) []string {
		var out []string
		for _, info := range m.chats.infos {
			out = append(out, slug(info.Title))
		}
		return out
	}
	return []command{
		{name: "mute", args: "[conversation] [8h|2d]", help: "stop notifications from a conversation, the open one if none is named, for a while or until unmuted",
			run: func(m *Model, a []string) error {
				var until time.Time
				if len(a) > 0 {
					if d, ok := muteLength(a[len(a)-1]); ok {
						until, a = m.now.Add(d).Truncate(time.Second), a[:len(a)-1]
					}
				}
				if len(a) > 1 {
					return fmt.Errorf("mute takes a conversation and a length of time, such as 8h or 2d")
				}
				c, title, err := m.muteTarget(a)
				if err != nil {
					return err
				}
				m.setMute(c, title, true, until)
				return nil
			},
			complete: complete},
		{name: "unmute", args: "[conversation]", help: "let a conversation notify again, the open one if none is named",
			run: func(m *Model, a []string) error {
				if len(a) > 1 {
					return fmt.Errorf("unmute takes one conversation")
				}
				c, title, err := m.muteTarget(a)
				if err != nil {
					return err
				}
				if _, ok := m.mutedUntil(c, title); !ok {
					return fmt.Errorf("%s isn't muted", m.convoLabel(c, title))
				}
				m.setMute(c, title, false, time.Time{})
				return nil
			},
			complete: complete},
	}
}
