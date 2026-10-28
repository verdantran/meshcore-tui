package ui

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

// target is somewhere the palette's finder can go.
type target struct {
	kind, label string
	id          string // what recent use is remembered by
	act         func(m *Model) tea.Cmd
	fill        string // a command that takes arguments goes into the prompt instead of running

	fold  []rune // the label as matched: folded, look-alikes made plain
	owner []int  // the label rune each folded rune came from
}

// finderSources list the targets, each kind from its own source; a new kind is a new source.
var finderSources = []func(m *Model) []target{
	screenTargets, conversationTargets, addChannelTargets, contactTargets, commandTargets, themeTargets, motionTargets,
}

const (
	finderRows   = 6
	finderRecent = 12
)

type finder struct {
	all     []target
	matches []match
	sel     int
	moved   bool     // the selection was moved, so enter goes there even when the input is a command
	recent  []string // ids, most recently used first
	folds   map[string][]rune
	join    target // "join #name" for a hashtag channel typed that we're not in
}

type match struct {
	t     *target
	score int
	at    []int // label runes matched, for drawing; only for the rows that show
}

// openFinder gathers the targets afresh; they're scored as keys are typed, not as frames are drawn.
func (m *Model) openFinder() {
	f := &m.pal.find
	f.all, f.matches, f.sel, f.moved = f.all[:0], nil, 0, false
	if f.folds == nil {
		f.folds = map[string][]rune{}
	}
	for _, src := range finderSources {
		for _, t := range src(m) {
			t.fold, t.owner = f.fold(t.label)
			f.all = append(f.all, t)
		}
	}
}

// fold folds a label rune by rune, so a match can be drawn on the label as shown.
func (f *finder) fold(label string) ([]rune, []int) {
	var fold []rune
	var owner []int
	for i, r := range []rune(label) {
		k := string(r)
		sk, ok := f.folds[k]
		if !ok {
			sk = foldRune(r)
			f.folds[k] = sk
		}
		for _, s := range sk {
			fold, owner = append(fold, s), append(owner, i)
		}
	}
	return fold, owner
}

func foldRune(r rune) []rune {
	if separator(r) {
		return []rune{' '}
	}
	// Lower case first: Skeleton reads a capital I as l, and people type i.
	if s := untrusted.Skeleton(string(unicode.ToLower(r))); s != "" {
		return []rune(s)
	}
	return []rune{unicode.ToLower(r)}
}

func separator(r rune) bool { return unicode.IsSpace(r) || r == '_' || r == '-' || r == '.' }

// foldQuery folds what's typed the way labels are folded, leaving out separators: "dundry_hill" finds "Dundry Hill".
func foldQuery(q string) []rune {
	var out []rune
	for _, r := range q {
		if !separator(r) {
			out = append(out, foldRune(r)...)
		}
	}
	return out
}

// finding reports whether the drop-down is showing: something typed, not a recalled line, and not yet a
// command's arguments.
func (m *Model) finding() bool {
	return m.pal.open && len(m.pal.find.matches) > 0
}

// refind scores every target against the input; it runs on each edit.
func (m *Model) refind() {
	p := &m.pal
	f := &p.find
	f.matches, f.sel, f.moved = f.matches[:0], 0, false
	in := p.input.text
	if strings.TrimSpace(in) == "" || p.hist < len(p.history) || m.argsMode(in) {
		return
	}
	q := foldQuery(in)
	if len(q) == 0 {
		return
	}
	for i := range f.all {
		t := &f.all[i]
		s, ok := fuzzy(q, t.fold, nil)
		if !ok {
			continue
		}
		if j := slices.Index(f.recent, t.id); j >= 0 {
			s += 2 * (finderRecent - j)
		}
		f.matches = append(f.matches, match{t: t, score: s})
	}
	slices.SortStableFunc(f.matches, func(a, b match) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return len(a.t.fold) - len(b.t.fold)
	})
	if name := strings.TrimSpace(in); m.joinable(name) {
		f.join = target{kind: "channel", label: "join " + name, id: "join " + name, act: func(m *Model) tea.Cmd { return m.joinHashtag(name) }}
		// Half a name of a channel we're in shouldn't push it down.
		at := 0
		if slices.ContainsFunc(m.chat.Channels(), func(ch radio.Channel) bool {
			return len(ch.Name) >= len(name) && strings.EqualFold(ch.Name[:len(name)], name)
		}) {
			at = len(f.matches)
		}
		f.matches = slices.Insert(f.matches, at, match{t: &f.join})
	}
	for i := range f.matches[:min(finderRows, len(f.matches))] {
		mt := &f.matches[i]
		if mt.t == &f.join {
			continue
		}
		var at []int
		fuzzy(q, mt.t.fold, &at)
		for _, j := range at {
			mt.at = append(mt.at, mt.t.owner[j])
		}
	}
}

// argsMode is a command's name and a space: the argument hints show rather than the finder.
func (m *Model) argsMode(in string) bool {
	name, _, spaced := strings.Cut(strings.TrimLeft(in, " "), " ")
	return spaced && m.isCommand(name)
}

func (m *Model) isCommand(name string) bool {
	return slices.ContainsFunc(m.commandSet(), func(c command) bool { return c.name == name })
}

// Scores for a match: each letter, more where a word starts, more again for a run, less for each letter skipped.
const (
	fuzzyLetter = 2
	fuzzyWord   = 8
	fuzzyFirst  = 4
	fuzzyRun    = 6
	fuzzyGap    = 1
	fuzzyWhole  = 20
)

// fuzzy scores q's letters appearing in order in t, choosing the best placing; at, if given, gets where they fell.
func fuzzy(q, t []rune, at *[]int) (int, bool) {
	n, l := len(q), len(t)
	if n == 0 || n > l {
		return 0, false
	}
	const none = -1 << 30
	// best[i*l+j] is the best score with q[i] on t[j]; from[i*l+j] is where q[i-1] went.
	best, from := make([]int, n*l), make([]int, n*l)
	bonus := func(j int) int {
		b := fuzzyLetter
		if j == 0 || !unicode.IsLetter(t[j-1]) && !unicode.IsDigit(t[j-1]) {
			b += fuzzyWord
		}
		if j == 0 {
			b += fuzzyFirst
		}
		return b
	}
	for j := range l {
		best[j] = none
		if t[j] == q[0] {
			best[j] = bonus(j) - j*fuzzyGap/2
		}
	}
	for i := 1; i < n; i++ {
		prev, cur := best[(i-1)*l:i*l], best[i*l:(i+1)*l]
		run, runAt := none, -1 // the best place for q[i-1] short of j-1, less the letters skipped to j
		for j := range l {
			if j >= 2 {
				run -= fuzzyGap
				if p := prev[j-2]; p > none && p-fuzzyGap > run {
					run, runAt = p-fuzzyGap, j-2
				}
			}
			cur[j] = none
			if j == 0 || t[j] != q[i] {
				continue
			}
			s, k := run, runAt
			if p := prev[j-1]; p > none && p+fuzzyRun >= s {
				s, k = p+fuzzyRun, j-1
			}
			if k >= 0 {
				cur[j], from[i*l+j] = s+bonus(j), k
			}
		}
	}
	top, end := none, -1
	for j, b := range best[(n-1)*l:] {
		if b > top {
			top, end = b, j
		}
	}
	if end < 0 {
		return 0, false
	}
	if n == l {
		top += fuzzyWhole
	}
	if at != nil {
		*at = make([]int, n)
		for i := n - 1; i >= 0; i-- {
			(*at)[i] = end
			end = from[i*l+end]
		}
	}
	return top, true
}

func (m *Model) finderMove(d int) {
	f := &m.pal.find
	f.sel = min(max(f.sel+d, 0), len(f.matches)-1)
	f.moved = true
	m.findAt(f.sel)
}

// findAt works out where a row's letters matched once it scrolls into view.
func (m *Model) findAt(i int) {
	mt := &m.pal.find.matches[i]
	if mt.at != nil {
		return
	}
	var at []int
	fuzzy(foldQuery(m.pal.input.text), mt.t.fold, &at)
	mt.at = []int{}
	for _, j := range at {
		mt.at = append(mt.at, mt.t.owner[j])
	}
}

// finderEnter goes to the chosen target, or reports false when enter should run the line as typed.
func (m *Model) finderEnter() bool {
	f := &m.pal.find
	if !m.finding() || !f.moved && m.isCommand(strings.TrimSpace(m.pal.input.text)) {
		return false
	}
	t := f.matches[f.sel].t
	f.recent = slices.Insert(slices.DeleteFunc(f.recent, func(id string) bool { return id == t.id }), 0, t.id)
	f.recent = f.recent[:min(len(f.recent), finderRecent)]
	if t.fill != "" {
		m.pal.input.set(t.fill)
		m.refind()
		return true
	}
	m.pal.open = false
	m.queued = t.act(m)
	return true
}

func title(s string) string {
	if s == "" {
		return s
	}
	return s[:1] + strings.ToLower(s[1:])
}

func screenTargets(m *Model) []target {
	var out []target
	if m.repeaterMode() {
		for i, name := range paneNames {
			out = append(out, target{kind: "screen", label: name, id: "pane " + name,
				act: func(m *Model) tea.Cmd { return m.setPane(adminPane(i)) }})
		}
		return out
	}
	for i, name := range tabNames {
		out = append(out, target{kind: "screen", label: title(name), id: "screen " + name, act: func(m *Model) tea.Cmd {
			if i == 2 {
				m.closePath()
			}
			m.setTab(i)
			if i == 1 {
				return m.onSelectContact()
			}
			return nil
		}})
	}
	return out
}

func conversationTargets(m *Model) []target {
	if m.chat == nil || m.repeaterMode() {
		return nil
	}
	var out []target
	for _, info := range m.chats.infos {
		kind := "chat"
		if info.Kind == chat.KindChannel {
			kind = "channel"
		}
		cv := info.Convo
		out = append(out, target{kind: kind, label: info.Title, id: "convo " + string(cv),
			act: func(m *Model) tea.Cmd { return m.goToConvo(cv) }})
	}
	return out
}

func addChannelTargets(m *Model) []target {
	if m.chat == nil || m.repeaterMode() {
		return nil
	}
	return []target{{kind: "channel", label: "add a channel", id: "add channel", act: func(m *Model) tea.Cmd {
		m.startAddChannel("")
		return nil
	}}}
}

func (m *Model) goToConvo(cv store.Convo) tea.Cmd {
	m.setTab(0)
	c := &m.chats
	c.filter, c.filtering, c.writing, c.reading, c.onAdd = lineEdit{}, false, false, false, false
	if i := m.convoIndex(cv); i >= 0 {
		c.sel, c.scroll = i, 0
		m.refreshChats()
		return nil
	}
	m.fail("that conversation has gone")
	return nil
}

// contactTargets are people and sensors, picked on Contacts, and repeaters and room servers, opened in Admin.
func contactTargets(m *Model) []target {
	if m.chat == nil || m.repeaterMode() {
		return nil
	}
	var out []target
	for _, ct := range m.chat.Contacts() {
		key := ct.Key
		t := target{label: ct.Name, id: "contact " + radio.KeyHex(key[:])}
		switch ct.Type {
		case radio.TypeRepeater, radio.TypeRoom:
			t.kind = "repeater"
			if ct.Type == radio.TypeRoom {
				t.kind = "room"
			}
			t.act = func(m *Model) tea.Cmd {
				if ct, ok := m.chat.Contact(store.ContactConvo(key)); ok {
					return m.openInAdmin(ct)
				}
				m.fail("that node has gone from your contacts")
				return nil
			}
		default:
			t.kind = "contact"
			t.act = func(m *Model) tea.Cmd { return m.goToContact(key) }
		}
		out = append(out, t)
	}
	return out
}

func (m *Model) goToContact(key [32]byte) tea.Cmd {
	m.setTab(1)
	c := &m.contacts
	c.heardView, c.overlay = false, noOverlay
	find := func() bool {
		for i, row := range m.contactRows() {
			if row.Key == key {
				c.sel = i
				return true
			}
		}
		return false
	}
	if !find() {
		c.filter, c.filtering, c.typeFilter = lineEdit{}, false, 0 // the filters were hiding them
		if !find() {
			m.fail("that contact has gone")
			return nil
		}
	}
	return m.onSelectContact()
}

func commandTargets(m *Model) []target {
	var out []target
	for _, c := range m.commandSet() {
		if slices.ContainsFunc(tabNames, func(n string) bool { return strings.EqualFold(n, c.name) }) {
			continue // the screen is a target already
		}
		t := target{kind: "command", label: c.name, id: "command " + c.name}
		if c.args != "" {
			t.fill = c.name + " "
		} else {
			name := c.name
			t.act = func(m *Model) tea.Cmd {
				m.pal.history = append(m.pal.history, name)
				m.runCommand(name)
				return m.queued
			}
		}
		out = append(out, t)
	}
	return out
}

func themeTargets(*Model) []target {
	var out []target
	for _, th := range theme.All {
		out = append(out, target{kind: "theme", label: th.Name, id: "theme " + th.Name,
			act: func(m *Model) tea.Cmd { m.setTheme(th); return nil }})
	}
	return out
}

func motionTargets(*Model) []target {
	var out []target
	for _, l := range []motion.Level{motion.Full, motion.Reduced, motion.Off} {
		out = append(out, target{kind: "motion", label: l.String(), id: "motion " + l.String(),
			act: func(m *Model) tea.Cmd { m.setMotion(l); return nil }})
	}
	return out
}

// overlayFinder draws the drop-down just above the prompt, over whatever is there.
func (m *Model) overlayFinder(lines []string) {
	if !m.finding() || m.asking != nil || m.paneKey {
		return
	}
	th, f := m.th, &m.pal.find
	w := min(m.w, 64)
	if m.w-w < 8 {
		w = m.w // rather than a sliver of what's beneath
	}
	rows := min(len(f.matches), finderRows, len(lines)-5)
	if w < 24 || rows < 1 {
		return
	}
	start := min(max(f.sel-rows+1, 0), len(f.matches)-rows)
	edge := th.Fg(th.Magenta, true)
	head := " GO "
	count := fmt.Sprintf(" %d/%d ", f.sel+1, len(f.matches))
	box := []string{edge.Render("┏━") + th.Block(th.Bg, th.Magenta).Render(head) +
		edge.Render(strings.Repeat("━", max(w-3-len(head), 0))+"┓")}
	kindW := 9
	labelW := w - 4 - kindW - 1
	for i := start; i < start+rows; i++ {
		m.findAt(i)
		mt := f.matches[i]
		label := mt.t.label
		if visibleWidth(label) > labelW {
			label = truncate(label, labelW)
		}
		pad := strings.Repeat(" ", max(labelW-visibleWidth(label), 0))
		kind := col(mt.t.kind, kindW)
		if i == f.sel {
			box = append(box, edge.Render("┃")+th.Fg(th.Magenta, true).Render("▌")+
				m.raised(" "+label+pad+" "+kind)+edge.Render("┃"))
			continue
		}
		box = append(box, edge.Render("┃")+"  "+m.markMatch(label, mt.at)+pad+" "+th.Paint(th.Dim, false, kind)+edge.Render("┃"))
	}
	box = append(box, edge.Render("┗"+strings.Repeat("━", max(w-3-visibleWidth(count), 0)))+
		th.Fg(th.Muted, false).Render(count)+edge.Render("━┛"))
	top := len(lines) - 1 - len(box)
	for j, b := range box {
		lines[top+j] = b + skip(lines[top+j], w)
	}
}

// markMatch draws a label with the letters that matched brighter.
func (m *Model) markMatch(label string, at []int) string {
	th := m.th
	var b strings.Builder
	var run []rune
	hit := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		if hit {
			b.WriteString(th.Paint(th.Cyan, true, string(run)))
		} else {
			b.WriteString(th.Paint(th.Text, false, string(run)))
		}
		run = run[:0]
	}
	for i, r := range []rune(label) {
		h := slices.Contains(at, i)
		if h != hit {
			flush()
			hit = h
		}
		run = append(run, r)
	}
	flush()
	return b.String()
}
