package ui

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/packets"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/sim"
	"github.com/verdantran/meshcore-tui/internal/store"
)

func (h *harness) paste(text string) {
	_, cmd := h.m.Update(tea.PasteMsg{Content: text})
	h.start(cmd)
	h.settle()
}

func TestPasteNeverAnswersAQuestion(t *testing.T) {
	h := newHarness(t)
	dropped := func(what string) {
		t.Helper()
		from := len(h.m.log)
		h.paste("yes")
		logged := false
		for _, l := range h.m.log[from:] {
			logged = logged || strings.Contains(l.text, "PASTE  dropped")
		}
		if h.m.confirming() || !logged {
			t.Errorf("%s: a paste should cancel it, and say so", what)
		}
	}

	ran := false
	h.m.ask("do it?", "do", func() tea.Cmd { ran = true; return nil })
	dropped("a question")
	if ran {
		t.Error("a paste answered the question")
	}

	h.openConvo("Public")
	ch, _ := h.m.selectedChannel()
	channels := len(h.m.chat.Channels())
	h.m.chats.chanStage, h.m.chats.shareCh = chanRemove, ch
	dropped("leaving a channel")
	h.m.chats.chanStage, h.m.chats.shareCh = chanShare, ch
	dropped("sharing a channel")
	if len(h.m.chat.Channels()) != channels || h.m.chats.writing {
		t.Error("the channel was left, or the paste started a message")
	}

	h.keys("2")
	h.pick("Carol")
	contacts := len(h.m.chat.Contacts())
	h.keys("x")
	dropped("removing a contact")
	h.m.contacts.overlay, h.m.contacts.prune = pruneOverlay, [][32]byte{h.contact("Carol").Key}
	dropped("pruning")
	h.m.contacts.overlay = importConfirmOverlay
	dropped("importing a link")
	if len(h.m.chat.Contacts()) != contacts {
		t.Error("a contact was removed or added")
	}

	h.keys("6")
	h.until("settings", func() bool { return h.m.device.loaded })
	h.moveTo("export")
	h.key(tea.KeyEnter)
	dropped("showing the private key")
	if _, busy := h.m.device.busy["export"]; busy || h.m.device.prompt != devNone {
		t.Error("the key was read")
	}
	h.edit("sf", "11")
	if h.m.device.prompt != devConfirm {
		t.Fatal("a radio change asks for y")
	}
	dropped("changing the radio")
	if h.settings().Radio.SF == 11 {
		t.Error("the radio was changed")
	}
	h.m.device.prompt, h.m.device.key = devShowKey, "5ec27a"
	dropped("copying the private key")
	if h.m.device.key != "" {
		t.Error("the key stays on screen")
	}

	// Fields that take a typed word still take a paste, which only fills them in.
	h.moveTo("reboot")
	h.key(tea.KeyEnter)
	h.paste("reboot")
	if h.m.device.prompt != devConfirm || h.m.device.input.text != "reboot" {
		t.Errorf("typed confirmation: %q", h.m.device.input.text)
	}
	h.key(tea.KeyEscape)

	h.keys("5")
	ct, sess := h.login("Dundry", "password")
	h.idle(sess)
	sess.Pending["tx"] = "20"
	h.m.adm.pane, h.m.adm.prompt = paneSettings, promptDiff
	dropped("applying settings")
	h.settle()
	if sess.Pending["tx"] != "20" {
		t.Errorf("settings applied to %s", ct.Name)
	}
}

func TestConsoleKeepsSecretsOut(t *testing.T) {
	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Dundry", "password")
	h.idle(sess)
	h.start(h.m.setPane(paneConsole))
	h.typeLine("password hunter2")
	if h.m.adm.prompt != promptConfirm {
		t.Fatal("a new password asks for the name")
	}
	h.typeLine("Dundry")
	h.idle(sess)
	h.typeLine("ver")
	h.idle(sess)
	for _, c := range h.m.adm.history {
		if strings.Contains(c, "hunter2") {
			t.Errorf("history keeps %q", c)
		}
	}
	if len(h.m.adm.history) != 1 {
		t.Errorf("history = %q", h.m.adm.history)
	}
	if scr := h.screen(); strings.Contains(scr, "hunter2") || strings.Contains(plainConsole(sess), "hunter2") {
		t.Errorf("the password shows:\n%s", scr)
	}
}

func TestPasswordSettingsAreMasked(t *testing.T) {
	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Dundry", "password")
	h.idle(sess)
	h.start(h.m.setPane(paneSettings))
	h.idle(sess)
	for i, st := range h.m.settingRows(sess) {
		if st.Key == "guest.password" {
			h.m.adm.setSel = i
		}
	}
	h.key(tea.KeyEnter)
	for range 20 {
		h.key(tea.KeyBackspace)
	}
	h.keys("s3cret")
	if scr := h.screen(); strings.Contains(scr, "s3cret") || !strings.Contains(scr, "••••••") {
		t.Errorf("while editing:\n%s", scr)
	}
	h.key(tea.KeyEnter)
	if scr := h.screen(); strings.Contains(scr, "s3cret") || !strings.Contains(scr, "1 CHANGES STAGED") {
		t.Errorf("staged:\n%s", scr)
	}
	h.keys("s")
	if scr := h.screen(); strings.Contains(scr, "s3cret") || !strings.Contains(scr, "REVIEW CHANGES") {
		t.Errorf("review:\n%s", scr)
	}
	h.keys("y")
	h.idle(sess)
	h.advance(5 * time.Second)
	if scr := h.screen(); strings.Contains(scr, "s3cret") {
		t.Errorf("applied:\n%s", scr)
	}
	secret, _ := admin.Lookup("bridge.secret")
	if shown(secret, "abc") == "abc" || shown(secret, "") != "" {
		t.Error("the bridge secret is a password too")
	}
}

func TestRebootFromTheReviewNeedsTheName(t *testing.T) {
	h := newHarness(t)
	h.keys("5")
	_, sess := h.login("Dundry", "password")
	h.idle(sess)
	h.start(h.m.setPane(paneSettings))
	h.idle(sess)

	// Nothing staged needs a reboot, so Y isn't offered and does nothing.
	h.m.admin.Stage(sess.Key, "tx", "20")
	h.keys("s")
	h.keys("Y")
	h.settle()
	if h.m.adm.prompt != noPrompt || sess.Pending["tx"] != "20" {
		t.Fatalf("Y without a reboot to do: prompt %v pending %v", h.m.adm.prompt, sess.Pending)
	}

	h.m.admin.Stage(sess.Key, "radio", "869.525,250,11,5")
	h.keys("s")
	h.keys("Y")
	if h.m.adm.prompt != promptConfirm || !strings.Contains(h.screen(), "AND REBOOT IT") {
		t.Fatalf("Y asks for the name:\n%s", h.screen())
	}
	h.typeLine("Dundy")
	if len(sess.Pending) != 2 || !strings.Contains(h.lastLog(), "NOT CONFIRMED") {
		t.Fatal("a misspelt name applies nothing")
	}
	h.key(tea.KeyEscape)
}

func TestRememberIsOffWheneverTheLoginOpens(t *testing.T) {
	h := newHarness(t)
	h.keys("5")
	a := &h.m.adm
	opened := func(how string) {
		t.Helper()
		if a.prompt != promptPassword || a.remember {
			t.Errorf("%s: prompt %v remember %v", how, a.prompt, a.remember)
		}
		a.prompt, a.remember = noPrompt, true
	}
	a.remember = true
	ct := h.choose("Dundry")
	h.key(tea.KeyEnter)
	opened("enter on the list")
	h.keys("L")
	opened("L on the dashboard")
	h.key(tea.KeyEnter)
	opened("enter on the dashboard")
	a.opened = false
	h.keys("L")
	opened("L on the list")
	h.m.loginFromChats(ct)
	opened("from Chats")
}

type tcpSim struct{ *sim.Sim }

func (tcpSim) Transport() string { return "TCP" }

func TestPrivateKeyCopyAndPin(t *testing.T) {
	h := newHarness(t)
	h.keys("6")
	h.until("settings", func() bool { return h.m.device.loaded })

	h.edit("pin", "246810")
	h.reloaded()
	if strings.Contains(plainLog(h), "246810") || !strings.Contains(plainLog(h), "BLE PIN CHANGED") {
		t.Errorf("the PIN reaches the log:\n%s", plainLog(h))
	}
	if strings.Contains(h.screen(), "246810") {
		t.Error("the PIN shows on the Device screen")
	}
	h.moveTo("pin")
	h.key(tea.KeyEnter)
	if !strings.Contains(h.screen(), "246810") {
		t.Error("the PIN shows while it's edited")
	}
	h.key(tea.KeyEscape)

	h.moveTo("export")
	h.key(tea.KeyEnter)
	h.keys("y")
	h.until("key", func() bool { return h.m.device.key != "" })
	if !strings.Contains(h.screen(), "cleared after 30 s") {
		t.Errorf("the hint says it's cleared:\n%s", h.screen())
	}
	h.m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	gen := h.m.clipGen
	if !strings.Contains(h.lastLog(), "cleared in 30 s") {
		t.Errorf("log: %q", h.lastLog())
	}
	empty := fmt.Sprint(tea.SetClipboard("")())
	if cmd := h.m.deviceMsg(clipClearMsg{gen}); cmd == nil || fmt.Sprint(cmd()) != empty {
		t.Error("the clipboard is emptied after the key was copied")
	}
	h.m.copyText("something else")
	if h.m.deviceMsg(clipClearMsg{gen}) != nil {
		t.Error("something copied since is left alone")
	}
	h.keys("x")

	h.m.radio = tcpSim{h.s}
	h.moveTo("export")
	h.key(tea.KeyEnter)
	if scr := h.screen(); !strings.Contains(scr, "crosses your network unencrypted") || !strings.Contains(scr, "y go ahead") {
		t.Errorf("over WiFi it warns first:\n%s", scr)
	}
	h.key(tea.KeyEscape)
}

func TestImportShowsWhoTheLinkIsFor(t *testing.T) {
	h := newHarness(t)
	h.keys("2")
	link := radio.LinkPrefix + hex.EncodeToString(advertPacket(t, "Carol", nil))
	h.keys("I")
	h.paste(link)
	h.key(tea.KeyEnter)
	scr := h.screen()
	if h.m.contacts.overlay != importConfirmOverlay || !strings.Contains(scr, "Carol") || !strings.Contains(scr, "key "+radio.KeyHex(h.m.contacts.importing.key[:6])) {
		t.Fatalf("the link's name and key are shown first:\n%s", scr)
	}
	if !strings.Contains(scr, "already have a Carol with a different key") {
		t.Errorf("a name you already have is flagged:\n%s", scr)
	}
	contacts := len(h.m.chat.Contacts())
	h.keys("n")
	if h.m.contacts.overlay != noOverlay || len(h.m.chat.Contacts()) != contacts {
		t.Error("any other key cancels")
	}

	link = radio.LinkPrefix + hex.EncodeToString(advertPacket(t, "Newcomer", nil))
	h.keys("I")
	h.paste(link)
	h.key(tea.KeyEnter)
	if scr := h.screen(); !strings.Contains(scr, "Newcomer") || strings.Contains(scr, "already have") {
		t.Errorf("a new name:\n%s", scr)
	}
	h.key(tea.KeyEscape)
}

func TestSmallTablesAreTrimmed(t *testing.T) {
	marks := map[int64]time.Time{}
	t0 := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	for i := range 100 {
		flash(marks, int64(i), t0.Add(time.Duration(i)*time.Second), popDur)
	}
	if len(marks) != 1 {
		t.Errorf("finished animations are kept: %d", len(marks))
	}

	tally := newMeshTally()
	for i := range 48 {
		at := t0.Add(time.Duration(i*2) * time.Hour) // every other hour, so a plain h-24h delete misses
		tally.add(decodedAdvert(fmt.Sprintf("n%d", i)), 0, true, false, at)
	}
	if len(tally.adverts) > 25 {
		t.Errorf("advert hours kept: %d", len(tally.adverts))
	}
	for i := range maxAdvertNames + 50 {
		tally.add(decodedAdvert(fmt.Sprintf("x%d", i)), 0, true, false, t0.Add(200*time.Hour))
	}
	if n := len(tally.adverts[t0.Add(200*time.Hour)]); n > maxAdvertNames {
		t.Errorf("names in an hour: %d", n)
	}

	h := newHarness(t)
	old := h.m.now.Add(-2 * time.Hour)
	for i := range maxSignedMsgs + 10 {
		h.m.chats.signedMsgs[fmt.Sprint(i)] = store.Message{RecvAt: old}
	}
	h.m.trimSigned()
	if len(h.m.chats.signedMsgs) != 0 {
		t.Errorf("old signatures kept: %d", len(h.m.chats.signedMsgs))
	}
}

func TestTraceWithFewerHashesThanHops(t *testing.T) {
	h := newHarness(t)
	h.keys("2")
	ct := h.contact("Carol")
	h.m.contacts.traces[7] = &traceRun{key: ct.Key, started: h.m.now, at: h.m.now.Add(-time.Hour),
		result: &radio.TraceResult{SNRs: []float32{1, 2, 3}, Hashes: []byte{0xAB}}}
	h.m.contacts.lastTrace[ct.Key] = 7
	if line := plain([]string{h.m.traceLine(ct)}); !strings.Contains(line, "AB") || !strings.Contains(line, "??") {
		t.Errorf("trace: %s", line)
	}
}

func decodedAdvert(name string) packets.Decoded { return packets.Decoded{Type: "ADVERT", Name: name} }
