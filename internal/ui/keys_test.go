package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/config"
)

func (h *harness) ctrl(r rune) {
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl})
	h.start(cmd)
	h.settle()
}

func TestTextFieldsKeepTheirKeys(t *testing.T) {
	h := newHarness(t)
	h.openConvo("Public")
	h.keys("i")
	h.keys("hello wo")
	h.ctrl('w')
	if h.m.paneKey || h.m.chats.input.text != "hello " {
		t.Errorf("ctrl+w deletes a word while writing: %q, chord %v", h.m.chats.input.text, h.m.paneKey)
	}
	h.ctrl('f')
	if h.m.search.open {
		t.Error("ctrl+f stays in the message")
	}
	h.key(tea.KeyEscape)
	h.ctrl('f')
	if !h.m.search.open {
		t.Fatal("ctrl+f searches once nothing's being typed")
	}
	h.keys("hello there")
	h.ctrl('w')
	if h.m.search.query.text != "hello " {
		t.Errorf("search is a text field too: %q", h.m.search.query.text)
	}
	h.key(tea.KeyEscape)

	h.keys("2/")
	h.m.paste("foo bar")
	h.settle()
	if h.m.contacts.filter.text != "foo bar" {
		t.Errorf("a pasted filter keeps its spaces: %q", h.m.contacts.filter.text)
	}
}

func TestQuitAsksAboutUnsentWork(t *testing.T) {
	h := newHarness(t)
	h.openConvo("Public")
	h.keys("i")
	h.keys("half a thought")
	h.key(tea.KeyEscape)
	if _, quit := h.m.key(tea.KeyPressMsg{Code: 'q', Text: "q"}); quit || !strings.Contains(h.screen(), "QUIT? YOUR MESSAGE ISN'T SENT") {
		t.Fatalf("q asks with a message unsent:\n%s", h.screen())
	}
	if _, quit := h.m.key(tea.KeyPressMsg{Code: 'n', Text: "n"}); quit || h.m.asking != nil {
		t.Fatal("any other key stays")
	}
	h.m.key(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if _, quit := h.m.key(tea.KeyPressMsg{Code: 'y', Text: "y"}); !quit {
		t.Error("y quits")
	}
}

func TestEnterActsOnTheRow(t *testing.T) {
	h := newHarness(t)
	h.keys("2")
	h.m.contacts.typeFilter = 0
	for i, ct := range h.m.contactRows() {
		if ct.Name == "Alice" {
			h.m.contacts.sel = i
		}
	}
	h.key(tea.KeyEnter)
	if cur, _ := h.m.selectedConvo(); h.m.tab != 0 || cur.Title != "Alice" {
		t.Errorf("enter on a contact messages them: tab %d, %s", h.m.tab, cur.Title)
	}
	h.keys("2")
	for i, ct := range h.m.contactRows() {
		if ct.Name == "Dundry" {
			h.m.contacts.sel = i
		}
	}
	h.key(tea.KeyEnter)
	if h.m.tab != 4 || h.m.adm.prompt != promptPassword || !strings.Contains(h.screen(), "LOG IN TO DUNDRY") {
		t.Errorf("enter on a repeater opens it in Admin:\n%s", h.screen())
	}
	h.key(tea.KeyEscape)

	h.keys("4")
	h.hear(advertPacket(t, "Purdown", []byte{0xA3}), 5)
	h.hear(advertPacket(t, "Kelston", []byte{0xA3}), 5)
	h.keys("G")
	if h.m.inspector.sel != len(h.m.visiblePackets())-1 {
		t.Error("G goes to the oldest packet, at the bottom")
	}
	h.key(tea.KeyEnter)
	if h.m.tab != 2 || h.m.mapv.path == nil {
		t.Error("enter on a packet shows its path")
	}
}

func TestChatsListKeys(t *testing.T) {
	h := newHarness(t)
	h.keys("1") // the list loads before G can go to its end
	h.keys("G")
	if h.m.chats.sel != len(h.m.chats.infos)-1 {
		t.Error("G goes to the last conversation")
	}
	h.keys("g")
	if h.m.chats.sel != 0 {
		t.Error("g goes to the first")
	}
	h.openConvo("#bristol")
	h.ctrl('u')
	if h.m.chats.scroll == 0 {
		t.Error("ctrl+u scrolls the history up")
	}
	h.ctrl('d')
	if h.m.chats.scroll != 0 {
		t.Error("ctrl+d scrolls it back")
	}
	h.keys("r")
	if c := &h.m.chats; !c.writing || !strings.HasPrefix(c.input.text, "@[") {
		t.Errorf("r replies to the newest message received: %q", c.input.text)
	}
}

func TestRemovingAsksFirst(t *testing.T) {
	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Dundry", "password")
	h.idle(sess)
	for h.m.adm.pane != paneACL {
		h.key(tea.KeyRight)
	}
	h.idle(sess)
	n := len(sess.ACL)
	h.keys("x")
	if !strings.Contains(h.screen(), "ACCESS TO DUNDRY AWAY?") {
		t.Fatalf("x asks:\n%s", h.screen())
	}
	h.keys("n")
	h.idle(sess)
	if len(sess.ACL) != n {
		t.Error("any other key keeps them")
	}

	h.key(tea.KeyLeft)
	h.key(tea.KeyLeft)
	h.idle(sess)
	if h.m.adm.pane != paneSettings {
		t.Fatalf("pane %d", h.m.adm.pane)
	}
	h.keys("G")
	if rows := h.m.settingRows(sess); h.m.adm.setSel != len(rows)-1 {
		t.Error("G goes to the last setting")
	}
	h.keys("g")
	if h.m.adm.setSel != 0 {
		t.Error("g to the first")
	}
	h.keys("?")
	if scr := h.screen(); !strings.Contains(scr, "SETTINGS") || !strings.Contains(scr, "discard edit") || !strings.Contains(scr, "read all") {
		t.Errorf("help shows the open pane's keys:\n%s", scr)
	}
	h.key(tea.KeyEscape)
	h.key(tea.KeyEscape)
	h.keys("L")
	h.settle()
	if sess.State != admin.LoggedOut {
		t.Error("L logs out from the list")
	}
	h.keys("L")
	if h.m.adm.prompt != promptPassword {
		t.Error("L again logs in")
	}
}

func TestKeysByView(t *testing.T) {
	h := newHarness(t)
	h.m.opt.Keys = config.Keys{"admin": {"z": "j"}, "admin.list": {"z": "k"}, "panes": {"b": "s"}}
	h.keys("5")
	h.m.adm.sel = 1
	h.keys("z")
	if h.m.adm.sel != 0 {
		t.Error("[admin.list] wins over [admin] on the node list")
	}
	h.keys("1")
	h.ctrl('w')
	h.keys("b")
	if !h.m.lay.split() {
		t.Error("[panes] rebinds the key after ctrl+w")
	}
}

func TestMouseWheelAndOverlays(t *testing.T) {
	h := newHarness(t)
	h.openConvo("#bristol")
	h.m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if h.m.chats.scroll == 0 {
		t.Error("the wheel scrolls the history")
	}
	h.keys("2")
	sel := h.m.contacts.sel
	h.m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if h.m.contacts.sel != sel+1 {
		t.Error("the wheel moves through a list")
	}
	h.keys("x")
	h.m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: 1})
	if h.m.tab != 1 || h.m.contacts.overlay != removeOverlay {
		t.Error("a click leaves the screen alone while it asks something")
	}
	h.key(tea.KeyEscape)

	h.keys("5")
	_, sess := h.login("Dundry", "password")
	h.idle(sess)
	x := 2 + len(" Status ") + 1 + len(" Health ") + 1 + 2 // inside " Settings "
	h.m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: 3})
	if h.m.adm.pane != paneSettings {
		t.Errorf("clicking a pane's name opens it: %d", h.m.adm.pane)
	}
}

func TestBindingsCantAnswerYes(t *testing.T) {
	h := newHarness(t)
	h.m.opt.Keys = config.Keys{"all": {"n": "y", "esc": "y", "z": "y"}}
	h.keys("2")
	before := len(h.m.chat.Contacts())
	h.keys("x")
	h.keys("n")
	h.settle()
	if len(h.m.chat.Contacts()) != before || h.m.contacts.overlay != noOverlay {
		t.Fatal("n bound to y removed a contact")
	}
	h.keys("x")
	h.key(tea.KeyEscape)
	h.settle()
	if len(h.m.chat.Contacts()) != before {
		t.Fatal("esc bound to y removed a contact")
	}

	ran := false
	h.m.ask("erase everything?", "erase", func() tea.Cmd { ran = true; return nil })
	h.keys("z")
	if ran || h.m.asking != nil {
		t.Fatal("z bound to y answered a question")
	}
	h.m.ask("erase everything?", "erase", func() tea.Cmd { ran = true; return nil })
	h.keys("y")
	if !ran {
		t.Fatal("y itself still answers")
	}
}
