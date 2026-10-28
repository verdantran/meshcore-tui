package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/verdantran/meshcore-tui/internal/golden"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

func (h *harness) find(q string) {
	h.t.Helper()
	h.keys(":")
	h.typeFast(q)
	if !h.m.finding() {
		h.t.Fatalf("nothing found for %q", q)
	}
}

func (h *harness) top() *target { return h.m.pal.find.matches[h.m.pal.find.sel].t }

func TestFinderGoesToEachKind(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.find("dund")
	if top := h.top(); top.label != "Dundry" || top.kind != "repeater" {
		t.Fatalf("dund found %s %s first", top.kind, top.label)
	}
	h.key(tea.KeyEnter)
	if h.m.pal.open || h.m.tab != 4 || h.m.adm.open != h.contact("Dundry").Key {
		t.Fatalf("Dundry didn't open in Admin: tab %d, palette %v", h.m.tab, h.m.pal.open)
	}
	h.key(tea.KeyEscape)

	h.find("#bri")
	h.key(tea.KeyEnter)
	if cur, _ := h.m.selectedConvo(); h.m.tab != 0 || cur.Title != "#bristol" {
		t.Fatalf("#bristol didn't open: tab %d, %q", h.m.tab, cur.Title)
	}

	h.find("bob")
	for h.top().kind != "contact" {
		h.ctrl('n')
	}
	h.key(tea.KeyEnter)
	if ct, ok := h.m.selectedContact(); h.m.tab != 1 || !ok || ct.Name != "Bob" {
		t.Fatalf("Bob isn't picked on Contacts: tab %d, %s", h.m.tab, ct.Name)
	}

	h.find("ice")
	if top := h.top(); top.kind != "theme" {
		t.Fatalf("ice found %s %s first", top.kind, top.label)
	}
	h.key(tea.KeyEnter)
	if h.m.Theme().Name != "ice" {
		t.Errorf("theme is %s", h.m.Theme().Name)
	}

	h.find("reduc")
	h.key(tea.KeyEnter)
	if h.m.level.String() != "reduced" {
		t.Errorf("motion is %s", h.m.level)
	}

	h.find("pack")
	h.key(tea.KeyEnter)
	if h.m.tab != 3 {
		t.Errorf("packets screen: tab %d", h.m.tab)
	}
}

func TestFinderFindsPeopleOnContactsThroughFilters(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.keys("2")
	h.keys("/alice")
	h.key(tea.KeyEnter)
	h.find("carol")
	for h.top().kind != "contact" {
		h.ctrl('n')
	}
	h.key(tea.KeyEnter)
	if ct, ok := h.m.selectedContact(); !ok || ct.Name != "Carol" {
		t.Fatalf("the filter hid Carol: %s", ct.Name)
	}
}

func TestFinderCommands(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.find("expo")
	h.key(tea.KeyEnter)
	if !h.m.pal.open || h.m.pal.input.text != "export " || h.m.finding() {
		t.Fatalf("a command taking arguments should fill the prompt: %q, finding %v", h.m.pal.input.text, h.m.finding())
	}
	if hint := ansi.Strip(h.m.paletteLine()); !strings.Contains(hint, "[json]") {
		t.Errorf("the arguments aren't hinted: %q", hint)
	}
	h.key(tea.KeyEscape)

	h.find("frams")
	h.key(tea.KeyEnter)
	if !h.m.debug {
		t.Error("frames, which takes nothing, should run")
	}
	h.keys(":")
	h.key(tea.KeyUp)
	if h.m.pal.input.text != "frames" {
		t.Errorf("a command run from the list goes into history: %q", h.m.pal.input.text)
	}
	h.key(tea.KeyEscape)

	before := h.m.Theme().Name
	h.keys(":theme")
	if !h.m.finding() {
		t.Fatal("theme alone is still a search")
	}
	h.key(tea.KeyEnter)
	if h.m.pal.open || h.m.Theme().Name == before {
		t.Error("theme typed in full still cycles the theme")
	}

	h.keys(":trace ")
	if h.m.finding() {
		t.Error("a command and a space shows its arguments, not the finder")
	}
	h.typeFast("Dundry")
	if h.m.finding() {
		t.Error("arguments aren't searched")
	}
	h.key(tea.KeyEnter)
	if h.m.tab != 1 {
		t.Errorf("trace Dundry should run as before: tab %d", h.m.tab)
	}
}

func TestFinderKeepsHistory(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, line := range []string{"ticker on", "help"} {
		h.keys(":")
		h.typeFast(line)
		h.key(tea.KeyEnter)
		h.key(tea.KeyEscape)
	}
	h.keys(":")
	h.key(tea.KeyUp)
	if h.m.pal.input.text != "help" || h.m.finding() {
		t.Fatalf("up recalls the last line, without the list: %q", h.m.pal.input.text)
	}
	h.key(tea.KeyUp)
	if h.m.pal.input.text != "ticker on" {
		t.Fatalf("up again goes further back, not into a list: %q", h.m.pal.input.text)
	}
	h.ctrl('n')
	if h.m.pal.input.text != "help" {
		t.Errorf("ctrl+n comes forward: %q", h.m.pal.input.text)
	}
	h.key(tea.KeyBackspace)
	if !h.m.finding() {
		t.Fatal("editing a recalled line searches")
	}
	h.key(tea.KeyDown)
	if h.m.pal.find.sel != 1 {
		t.Errorf("down moves through the list once it shows: %d", h.m.pal.find.sel)
	}
}

func TestCtrlKOpensThePalette(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for i := range tabNames {
		h.keys(fmt.Sprint(i + 1))
		h.ctrl('k')
		if !h.m.pal.open {
			t.Fatalf("ctrl+k on %s", tabNames[i])
		}
		h.key(tea.KeyEscape)
	}
	h.openConvo("Public")
	h.keys("i")
	h.typeFast("hello there")
	h.key(tea.KeyHome)
	h.ctrl('k')
	if h.m.pal.open || h.m.chats.input.text != "" {
		t.Errorf("ctrl+k deletes to the end of a text field: %q, palette %v", h.m.chats.input.text, h.m.pal.open)
	}
}

func TestFinderRecentFirst(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.find("c")
	first := h.top()
	for h.top() == first {
		h.ctrl('n')
	}
	chosen := h.top().id
	h.key(tea.KeyEnter)
	h.key(tea.KeyEscape)
	h.keys("1")
	h.find("c")
	if h.top().id != chosen {
		t.Errorf("%s was used last and should come first, not %s", chosen, h.top().id)
	}
}

func TestFuzzy(t *testing.T) {
	t.Parallel()
	var f finder
	score := func(q, label string) int {
		t.Helper()
		fold, _ := f.fold(label)
		s, ok := fuzzy(foldQuery(q), fold, nil)
		if !ok {
			t.Fatalf("%q doesn't find %q", q, label)
		}
		return s
	}
	f.folds = map[string][]rune{}
	if score("ct", "Cabot Tower") <= score("ct", "Contacts") {
		t.Error("word starts should win")
	}
	if score("dun", "Dundry") <= score("dun", "Dark Union") {
		t.Error("a run should win")
	}
	if score("ice", "ice") <= score("ice", "Ice Station") {
		t.Error("the whole label should win")
	}
	score("alice", "Аlicе") // Cyrillic А and е
	score("bob", "ＢＯＢ")     // full width
	score("cafe", "Café")   // accents
	score("weston_s", "Weston-super-Mare")
	for _, miss := range [][2]string{{"ab", "ba"}, {"xyz", "xy"}, {"aa", "a"}} {
		fold, _ := f.fold(miss[1])
		if _, ok := fuzzy(foldQuery(miss[0]), fold, nil); ok {
			t.Errorf("%q found %q", miss[0], miss[1])
		}
	}
	fold, owner := f.fold("Cabot Tower")
	var at []int
	fuzzy(foldQuery("ctow"), fold, &at)
	var got []int
	for _, j := range at {
		got = append(got, owner[j])
	}
	if fmt.Sprint(got) != "[0 6 7 8]" {
		t.Errorf("ctow placed at %v", got)
	}
}

// TestFinderScoresOnKeys: frames reuse the scoring a keystroke did.
func TestFinderScoresOnKeys(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var cs []radio.Contact
	for i := range 500 {
		cs = append(cs, radio.Contact{Key: [32]byte{byte(i), byte(i >> 8), 9}, Name: fmt.Sprintf("Node %d", i), Type: radio.TypeChat})
	}
	h.m.chat.ReplaceContacts(cs)
	h.find("node 4")
	if n := len(h.m.pal.find.matches); n < 100 {
		t.Errorf("only %d of the nodes", n)
	}
	h.m.pal.find.all = nil // scoring again now would find nothing
	h.advance(time.Second)
	if !strings.Contains(plain(screen(t, h.m)), "Node 4") {
		t.Error("drawing a frame scored again")
	}
}

func TestGoldenFinder(t *testing.T) {
	t.Parallel()
	m, c, _ := newChatModel(t)
	var frames []golden.Frame
	for _, w := range []int{100, 40} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		typeKeys(m, ":")
		typeKeys(m, "al")
		key(m, tea.KeyDown)
		tick(m, c, time.Second)
		frames = append(frames, golden.Frame{Label: fmt.Sprint(w, " wide"), Lines: screen(t, m)})
		key(m, tea.KeyEscape)
	}
	golden.Check(t, "finder", frames)
}

func TestFinderInASCII(t *testing.T) {
	t.Parallel()
	h := busyHarness(t)
	h.m.opt.ASCII = true
	h.find("a")
	h.advance(time.Second)
	for n, row := range screen(t, h.m) {
		plain := ansi.Strip(row)
		if j := strings.IndexFunc(plain, func(r rune) bool { return r > 0x7e }); j >= 0 {
			t.Errorf("row %d isn't ASCII at %d: %q", n, j, plain)
		}
	}
}

// Overlays drawn over wide characters leave every row its full width, at either parity of column.
func TestOverlaysOverWideText(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, w := range []int{200, 199} {
		h.m.Update(tea.WindowSizeMsg{Width: w, Height: 60})
		rows := func() []string {
			lines := make([]string, 60)
			for i := range lines {
				lines[i] = fit(strings.Repeat("東", i%2)+strings.Repeat("東京", w), w)
			}
			return lines
		}
		check := func(what string, lines []string) {
			t.Helper()
			for n, l := range lines {
				if got := visibleWidth(l); got != w {
					t.Errorf("%s at %d wide: row %d is %d cells: %q", what, w, n, got, ansi.Strip(l))
				}
			}
		}
		h.find("a")
		lines := rows()
		h.m.overlayFinder(lines)
		check("finder", lines)
		h.key(tea.KeyEscape)

		h.m.pushToast("👨‍👩‍👧‍👦 the whole family's on now", "東京からこんにちは！メッシュは最高です 1️⃣")
		for range 6 {
			h.advance(50 * time.Millisecond) // through the slide, so the toast's edge crosses both halves of a character
			lines = rows()
			h.m.overlayToasts(lines)
			check("toast", lines)
		}
	}
}
