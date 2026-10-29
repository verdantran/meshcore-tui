package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func (h *harness) consoleSays(want string) {
	h.t.Helper()
	h.until("console shows "+want, func() bool {
		for _, l := range h.m.device.con.lines {
			if strings.Contains(l.Text, want) {
				return true
			}
		}
		return false
	})
}

func TestRadioConsole(t *testing.T) {
	h := newHarness(t)
	h.openDevice()
	h.moveTo("console")
	h.key(tea.KeyEnter)
	if h.m.device.prompt != devConsole || !strings.Contains(h.screen(), "RADIO CONSOLE") {
		t.Fatalf("enter opens the console:\n%s", h.screen())
	}
	h.typeLine("ver")
	h.consoleSays("v1.18.0")
	h.typeLine("get radio.fem.rxgain")
	h.consoleSays("> on")
	h.keys("set radio.fem.r")
	h.key(tea.KeyTab)
	if got := h.m.device.con.input.text; got != "set radio.fem.rxgain o" {
		t.Errorf("tab completes to %q", got)
	}
	h.keys("ff")
	h.key(tea.KeyEnter)
	h.consoleSays("RX gain off")
	h.typeLine("frobnicate")
	h.consoleSays("Unknown command")
	if l := h.m.device.con.lines[len(h.m.device.con.lines)-1]; !l.Error {
		t.Error("an unknown command is shown as an error")
	}

	h.key(tea.KeyUp)
	if h.m.device.con.input.text != "frobnicate" {
		t.Errorf("up recalls the last command, not %q", h.m.device.con.input.text)
	}
	h.key(tea.KeyEscape)
	if h.m.device.prompt != devNone {
		t.Error("esc closes the console")
	}
	if !strings.Contains(h.screen(), "c console") {
		t.Error("the hint line offers c")
	}
	h.keys("c")
	if h.m.device.prompt != devConsole || !strings.Contains(h.screen(), "RX gain off") {
		t.Error("c opens the console again, as it was left")
	}
}

func TestRadioConsoleAsksFirst(t *testing.T) {
	h := newHarness(t)
	h.openDevice()
	h.m.openConsole()
	h.typeLine("set pin 654321")
	if h.m.device.prompt != devConfirm || !strings.Contains(h.screen(), "pair again") {
		t.Fatalf("a new PIN asks first:\n%s", h.screen())
	}
	if strings.Contains(h.screen(), "654321") {
		t.Error("the PIN isn't shown")
	}
	h.keys("n")
	if h.m.device.prompt != devConsole || len(h.m.device.con.lines) != 0 {
		t.Fatal("anything but y goes back to the console, sending nothing")
	}
	h.typeLine("set pin 654321")
	h.keys("y")
	h.consoleSays("pin is now")
	if pin := h.settings().Pin; pin != 654321 {
		t.Errorf("pin %d", pin)
	}
	if slices.ContainsFunc(h.m.device.con.history, func(c string) bool { return strings.Contains(c, "654321") }) {
		t.Error("a command with a secret isn't kept in the history")
	}
	if l := h.m.device.con.lines[0]; strings.Contains(l.Text, "654321") || !strings.Contains(l.Text, "••••") {
		t.Errorf("and it's masked in the console: %q", l.Text)
	}
	if strings.Contains(plainLog(h), "pin is now") {
		t.Error("replies stay out of the log")
	}

	h.typeLine("reboot")
	h.keys("y")
	h.consoleSays("gone off the air")
}

func (h *harness) clearLog() { h.m.log = h.m.log[:0] }

func TestRadioConsoleNeedsNewFirmware(t *testing.T) {
	h := newHarness(t)
	h.openDevice()
	h.m.ready.Device.Code, h.m.ready.Device.Firmware = 13, "v1.17.1"
	r := h.moveTo("console")
	if r.value != "needs firmware v1.18.0" {
		t.Errorf("the row says what it needs: %q", r.value)
	}
	for name, press := range map[string]func(){"enter": func() { h.key(tea.KeyEnter) }, "c": func() { h.keys("c") }} {
		h.clearLog()
		press()
		if h.m.device.prompt == devConsole || !strings.Contains(h.lastLog(), "v1.18.0 or newer; this radio has v1.17.1") {
			t.Errorf("%s: older firmware is told why: %s", name, h.lastLog())
		}
	}
}
