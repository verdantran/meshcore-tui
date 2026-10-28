package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/text/unicode/norm"

	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/privfile"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

// fold is text as a filter compares it: lower case, without accents, so "zoe" finds "Zoë" whether the ë
// came as one character or as an e and a mark.
func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// sortName is what a name sorts by: folded, and without the emoji or symbols some lead with, so
// "🍺 Harbourside" sorts among the H's rather than after every letter.
func sortName(s string) string {
	if t := strings.TrimLeftFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }); t != "" {
		s = t
	}
	return fold(s)
}

// searchModel is the ctrl+f overlay that searches every conversation.
type searchModel struct {
	open    bool
	query   lineEdit
	results []store.Message
	sel     int

	gen     int  // counts edits to the query, so results for an older one are dropped
	shown   int  // the gen the results are for
	jump    bool // enter was pressed before the results came, so open the first when they do
	stopOld context.CancelFunc
}

// searchDelay is how long typing pauses before the search runs.
var searchDelay = 150 * time.Millisecond

type searchDueMsg struct{ gen int }

type searchDoneMsg struct {
	gen     int
	results []store.Message
}

func (m *Model) openSearch() {
	m.closeSearch()
	m.search = searchModel{open: true}
}

func (m *Model) closeSearch() {
	if m.search.stopOld != nil {
		m.search.stopOld()
	}
	m.search.open, m.search.stopOld = false, nil
}

// searchEdited waits for typing to pause before searching for the new query.
func (m *Model) searchEdited() tea.Cmd {
	s := &m.search
	s.gen++
	if m.chat == nil || strings.TrimSpace(s.query.text) == "" {
		if s.stopOld != nil {
			s.stopOld()
		}
		s.results, s.sel, s.shown, s.stopOld = nil, 0, s.gen, nil
		return nil
	}
	gen := s.gen
	return tea.Tick(searchDelay, func(time.Time) tea.Msg { return searchDueMsg{gen} })
}

// searchMsg runs the search off the UI goroutine once typing has paused, and takes its results.
func (m *Model) searchMsg(msg tea.Msg) tea.Cmd {
	s := &m.search
	switch msg := msg.(type) {
	case searchDueMsg:
		if !s.open || msg.gen != s.gen || m.chat == nil {
			return nil
		}
		if s.stopOld != nil {
			s.stopOld()
		}
		ctx, stop := context.WithCancel(context.Background())
		s.stopOld = stop
		find, gen := m.chat.SearchLater(s.query.text, "", 200), msg.gen
		return func() tea.Msg {
			found, _ := find(ctx)
			return searchDoneMsg{gen, found}
		}
	case searchDoneMsg:
		if !s.open || msg.gen != s.gen {
			return nil
		}
		s.results, s.sel, s.shown = msg.results, 0, msg.gen
		if s.jump {
			s.jump = false
			if len(s.results) > 0 {
				m.jumpTo(s.results[0])
			}
			m.closeSearch()
		}
	}
	return nil
}

func (m *Model) searchKey(k string) tea.Cmd {
	s := &m.search
	switch k {
	case "esc":
		m.closeSearch()
	case "up":
		s.sel = max(s.sel-1, 0)
	case "down":
		s.sel = min(s.sel+1, max(len(s.results)-1, 0))
	case "enter":
		if s.shown != s.gen {
			s.jump = true
			return nil
		}
		if s.sel < len(s.results) {
			m.jumpTo(s.results[s.sel])
		}
		m.closeSearch()
	default:
		before := s.query.text
		if s.query.key(k) && s.query.text != before {
			return m.searchEdited()
		}
	}
	return nil
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

// exportVerdict is a message's signature badge for an export, which has no colour to say a tick is a pin.
func exportVerdict(j judge) func(store.Message) string {
	return func(msg store.Message) string {
		v, _, _ := j.verdict(msg)
		if v == "✓ signed" {
			return "✓ signed by the pinned key"
		}
		return v
	}
}

type exportDoneMsg struct {
	n    int
	path string
	err  error
}

// createExport makes a file in the exports folder that only you can read.
func (m *Model) createExport(name string) (*os.File, string, error) {
	dir := m.opt.ExportDir
	if dir == "" {
		dir = filepath.Join(config.DataDir(), "exports")
	}
	if err := privfile.Dir(dir); err != nil {
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
			export := m.chat.ExportLater(c, asJSON, exportVerdict(m.frozenJudge()))
			m.queued = func() tea.Msg {
				n, err := export(f)
				if cerr := f.Close(); err == nil {
					err = cerr
				}
				return exportDoneMsg{n, path, err}
			}
			return nil
		},
		complete: func(*Model) []string { return []string{"json", "all", "packets"} }}
}

func (m *Model) exportDone(msg exportDoneMsg) {
	if msg.err != nil {
		m.fail("EXPORT  " + msg.err.Error())
		return
	}
	m.addLog(m.now, fmt.Sprintf("EXPORTED  %d messages → %s", msg.n, msg.path), radio.ToneOK)
}

// mentionLabel shows @[name] as @name; the brackets are only there so names can hold spaces.
func mentionLabel(seg string) string {
	if strings.HasPrefix(seg, "@[") && strings.HasSuffix(seg, "]") {
		return "@" + seg[2:len(seg)-1]
	}
	return seg
}
