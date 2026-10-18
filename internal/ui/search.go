package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

// searchModel is the ctrl+f overlay that searches every conversation.
type searchModel struct {
	open    bool
	query   lineEdit
	results []store.Message
	sel     int
}

func (m *Model) openSearch() {
	m.search = searchModel{open: true}
}

func (m *Model) runSearch() {
	s := &m.search
	s.results, s.sel = nil, 0
	if m.chat != nil && strings.TrimSpace(s.query.text) != "" {
		s.results, _ = m.chat.Search(s.query.text, "", 200)
	}
}

func (m *Model) searchKey(k string) {
	s := &m.search
	switch k {
	case "esc":
		s.open = false
	case "up":
		s.sel = max(s.sel-1, 0)
	case "down":
		s.sel = min(s.sel+1, max(len(s.results)-1, 0))
	case "enter":
		if s.sel < len(s.results) {
			m.jumpTo(s.results[s.sel])
		}
		s.open = false
	default:
		before := s.query.text
		if s.query.key(k) && s.query.text != before {
			m.runSearch()
		}
	}
}

// jumpTo opens a message's conversation and scrolls it into view.
func (m *Model) jumpTo(msg store.Message) {
	m.setTab(0)
	c := &m.chats
	c.filter, c.filtering, c.writing = lineEdit{}, false, false
	for i, info := range c.infos {
		if info.Convo == msg.Convo {
			c.sel = i
		}
	}
	m.refreshChats()
	for i, cm := range c.msgs {
		if cm.ID == msg.ID {
			c.scroll = len(c.msgs) - 1 - i
		}
	}
	c.focus = msg.ID
}

func (m *Model) searchLines(w, rows int) []string {
	th, s := m.th, &m.search
	prompt := th.Fg(th.Magenta, true).Render("find▸ ") + s.query.render(th, w-20, false)
	count := ""
	if s.query.text != "" {
		count = th.Fg(th.Dim, false).Render(fmt.Sprintf("  %d found", len(s.results)))
	}
	out := []string{prompt + count, ""}
	if s.query.text == "" {
		return append(out, th.Fg(th.Dim, false).Render("Type to search every conversation. ↑↓ choose · enter open · esc close"))
	}
	start := max(s.sel-(rows-3)/2, 0)
	for i := start; i < len(s.results) && len(out) < rows; i++ {
		r := s.results[i]
		where := m.chat.Title(r.Convo)
		line := th.Fg(th.Dim, false).Render(r.RecvAt.Local().Format("02 Jan 15:04")+"  ") +
			th.Fg(th.Cyan, false).Render(col(where, 14)) + " " +
			th.Fg(th.Muted, true).Render(r.Sender+": ") + m.highlight(r.Text, s.query.text, th.Text, nil)
		if i == s.sel {
			out = append(out, th.Fg(th.Magenta, true).Render("▌")+m.raised(fit(" "+plainResult(r, where), w-1)))
			continue
		}
		out = append(out, " "+line)
	}
	return out
}

// plainResult is a result row without colour, for drawing on the raised selection fill.
func plainResult(r store.Message, where string) string {
	return r.RecvAt.Local().Format("02 Jan 15:04") + "  " + col(where, 14) + " " + r.Sender + ": " + r.Text
}

type span struct {
	start, end int
	kind       int // 0 search match, 1 your name, 2 someone else
}

// highlight styles one line of message text: search matches, @you and other @names.
func (m *Model) highlight(line, query string, base theme.RGB, marks []emph) string {
	th := m.th
	var spans []span
	if q := strings.ToLower(strings.TrimSpace(query)); q != "" {
		lower := strings.ToLower(line)
		for i := 0; ; {
			j := strings.Index(lower[i:], q)
			if j < 0 {
				break
			}
			spans = append(spans, span{i + j, i + j + len(q), 0})
			i += j + len(q)
		}
	}
	self := ""
	if m.chat != nil {
		self = strings.ToLower(m.chat.Self())
	}
	for _, mn := range chat.FindMentions(line) {
		kind := 2
		if strings.EqualFold(mn.Name, self) {
			kind = 1
		}
		spans = append(spans, span{mn.Start, mn.End, kind})
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var b strings.Builder
	pos := 0
	for _, sp := range spans {
		if sp.start < pos {
			continue // overlaps a span already drawn
		}
		b.WriteString(emphRender(th, base, line[pos:sp.start], marksIn(marks, pos, sp.start)))
		seg := line[sp.start:sp.end]
		switch sp.kind {
		case 0:
			b.WriteString(th.Block(th.Bg, th.Yellow).Render(seg))
		case 1:
			b.WriteString(th.Block(th.Text, th.Tint(th.Magenta)).Render(mentionLabel(seg)))
		default: // violet, so it stands apart from the cyan sender names around it
			b.WriteString(th.Block(th.Text, th.Tint(th.Violet)).Render(mentionLabel(seg)))
		}
		pos = sp.end
	}
	b.WriteString(emphRender(th, base, line[pos:], marksIn(marks, pos, len(line))))
	return b.String()
}

// fileName keeps a conversation's name to what's safe in a file name: letters, digits, - and _. Names come
// from other people, and a / or .. in one would put the file somewhere else.
func fileName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, strings.ToLower(name))
	if strings.Trim(name, "_") == "" {
		return "conversation"
	}
	return name
}

// createExport makes a file in the exports folder that only you can read.
func (m *Model) createExport(name string) (*os.File, string, error) {
	dir := m.opt.ExportDir
	if dir == "" {
		dir = filepath.Join(config.DataDir(), "exports")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	return f, path, err
}

func exportCommand() command {
	return command{name: "export", args: "[json] [all] | packets", help: "save this conversation, all of them, or the visible packets, to a file",
		run: func(m *Model, a []string) error {
			if len(a) == 1 && a[0] == "packets" {
				return m.exportPackets()
			}
			if m.chat == nil || m.chat.Device() == "" {
				return fmt.Errorf("nothing to export yet")
			}
			asJSON, all := false, false
			for _, w := range a {
				switch w {
				case "json":
					asJSON = true
				case "all":
					all = true
				case "text":
				default:
					return fmt.Errorf("export takes json and all, not %q", w)
				}
			}
			var c store.Convo
			name := "all-conversations"
			if !all {
				cur, ok := m.selectedConvo()
				if !ok {
					return fmt.Errorf("no conversation selected")
				}
				c, name = cur.Convo, fileName(slug(cur.Title))
			}
			ext := ".txt"
			if asJSON {
				ext = ".json"
			}
			f, path, err := m.createExport(name + "-" + m.now.Format("20060102-150405") + ext)
			if err != nil {
				return err
			}
			n, err := m.chat.Export(f, c, asJSON)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			m.addLog(m.now, fmt.Sprintf("EXPORTED  %d messages → %s", n, path), radio.ToneOK)
			return nil
		},
		complete: func(*Model) []string { return []string{"json", "all", "packets"} }}
}

// mentionLabel shows @[name] as @name; the brackets are only there so names can hold spaces.
func mentionLabel(seg string) string {
	if strings.HasPrefix(seg, "@[") && strings.HasSuffix(seg, "]") {
		return "@" + seg[2:len(seg)-1]
	}
	return seg
}
