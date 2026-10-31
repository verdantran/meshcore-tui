package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/console"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

// radioConsole is a console on your own radio, for companion firmware that runs console commands.
type radioConsole struct {
	input   lineEdit
	lines   []admin.Line
	history []string
	histAt  int
	busy    bool
	tab     completer
}

type cliRanMsg struct {
	cmd, reply string
	err        error
}

const consoleKeep = 500

func (m *Model) hasConsole() bool { return m.ready.Device.Code >= radio.ConsoleCode }

// openConsole opens the console, or says why it can't.
func (m *Model) openConsole() error {
	if m.dev() == nil {
		return fmt.Errorf("a repeater's console is on the Admin screen")
	}
	if !m.hasConsole() {
		return fmt.Errorf("the radio console needs companion firmware v1.18.0 or newer; this radio has %s", m.ready.Device.Firmware)
	}
	m.setTab(deviceTab)
	c := &m.device.con
	c.histAt = len(c.history)
	m.device.prompt = devConsole
	return nil
}

func (m *Model) radioConsoleRow() devRow {
	r := devRow{id: "console", group: "Console", label: "Radio console", kind: rowAction,
		hint: "Commands as you'd type them on the radio's serial console, such as set radio.fem.rxgain on.",
		press: func(m *Model) tea.Cmd {
			if err := m.openConsole(); err != nil {
				m.fail("CONSOLE  " + err.Error())
			}
			return nil
		}}
	if !m.hasConsole() {
		r.value, r.hint = "needs firmware v1.18.0", "Companion firmware v1.18.0 and newer run console commands; update the radio to use it."
	}
	return r
}

// consoleRisk is why a command needs a y first: it takes the radio away, can lock you out, or changes what
// you can hear. Matched by prefix, as the firmware matches.
func consoleRisk(cmd string, transport string) string {
	line := consoleLine(cmd)
	has := func(p string) bool { return strings.HasPrefix(line, p) }
	switch {
	case has("reboot"), has("poweroff"), has("shutdown"):
		return "The radio goes off the air and the link drops. After a reboot it reconnects by itself; after a power off, someone has to switch it back on."
	case has("set pin"):
		return "This changes the Bluetooth PIN. Anything paired with the old one will have to pair again."
	case has("set wifi."):
		if transport == "TCP" {
			return "This changes the radio's WiFi, which is how this app is connected to it, so the link may drop."
		}
		return "This changes the radio's WiFi settings."
	case has("set radio "), has("set freq"), has("set bw"), has("set sf"), has("set cr"):
		return "You'll only hear nodes that use the same frequency, bandwidth, spreading factor and coding rate."
	case has("set prv.key"), has("erase"), has("start ota"):
		return "This replaces or wipes what makes this radio yours, and it can't be undone from here."
	}
	return ""
}

// consoleSecret reports whether a command carries a secret, so it's shown masked and kept out of the history.
func consoleSecret(cmd string) bool {
	line := consoleLine(cmd)
	for _, p := range []string{"set pin ", "set wifi.pwd ", "set prv.key ", "password "} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

func maskSecret(cmd string) string {
	f := strings.Fields(cmd)
	keep := 2
	if strings.HasPrefix(consoleLine(cmd), "password") {
		keep = 1
	}
	if len(f) <= keep {
		return cmd
	}
	return strings.Join(f[:keep], " ") + " ••••••"
}

func (m *Model) radioConsoleKey(s string) tea.Cmd {
	c := &m.device.con
	if s != "tab" && s != "shift+tab" {
		c.tab.reset()
	}
	switch s {
	case "enter":
		cmd := strings.TrimSpace(c.input.text)
		if cmd == "" || c.busy {
			return nil
		}
		if !consoleSecret(cmd) {
			c.history = append(c.history, cmd)
		}
		c.histAt, c.input = len(c.history), lineEdit{}
		if why := consoleRisk(cmd, m.radio.Transport()); why != "" {
			dm := &m.device
			dm.prompt, dm.input, dm.confirm = devConfirm, lineEdit{}, devConfirmation{title: strings.ToUpper(maskSecret(cmd)), warning: why, back: devConsole,
				after: func(m *Model) tea.Cmd { m.device.prompt = devConsole; return m.runConsole(cmd) }}
			return nil
		}
		return m.runConsole(cmd)
	case "up":
		if c.histAt > 0 {
			c.histAt--
			c.input.set(c.history[c.histAt])
		}
	case "down":
		if c.histAt < len(c.history)-1 {
			c.histAt++
			c.input.set(c.history[c.histAt])
		} else {
			c.histAt, c.input = len(c.history), lineEdit{}
		}
	case "tab", "shift+tab":
		c.tab.tab(console.Companion(), &c.input, s == "shift+tab")
	default:
		c.input.key(s)
	}
	return nil
}

func (m *Model) runConsole(cmd string) tea.Cmd {
	c := &m.device.con
	shown := cmd
	if consoleSecret(cmd) {
		shown = maskSecret(cmd)
	}
	m.consoleAdd(admin.Line{At: m.now, Out: true, Text: shown})
	c.busy = true
	d := m.dev()
	limit := opTimeout
	if line := consoleLine(cmd); strings.HasPrefix(line, "reboot") || strings.HasPrefix(line, "poweroff") || strings.HasPrefix(line, "shutdown") {
		limit = 2 * time.Second // the radio goes away without replying
	}
	return m.radioCallFor(limit, func(ctx context.Context) tea.Msg {
		reply, err := d.RunCLI(ctx, cmd)
		return cliRanMsg{cmd, reply, err}
	})
}

func (m *Model) consoleAdd(l admin.Line) {
	c := &m.device.con
	c.lines = append(c.lines, l)
	if len(c.lines) > consoleKeep {
		c.lines = append(c.lines[:0], c.lines[len(c.lines)-consoleKeep:]...)
	}
}

// consoleMsg shows a reply. Replies stay in the console and never reach the log, since some, like
// get wifi.pwd, are secrets.
func (m *Model) consoleMsg(msg cliRanMsg) tea.Cmd {
	m.device.con.busy = false
	line := consoleLine(msg.cmd)
	switch {
	case msg.err != nil && (strings.HasPrefix(line, "reboot") || strings.HasPrefix(line, "poweroff") || strings.HasPrefix(line, "shutdown")):
		m.consoleAdd(admin.Line{At: m.now, Text: "The radio has gone off the air, as asked."})
		return nil
	case msg.err != nil:
		m.consoleAdd(admin.Line{At: m.now, Error: true, Text: radio.Explain(msg.err)})
		return nil
	}
	reply := strings.TrimRight(msg.reply, "\r\n ")
	if reply == "" {
		reply = "(no reply)"
	}
	bad := strings.HasPrefix(reply, "Error") || strings.HasPrefix(reply, "Unknown")
	m.consoleAdd(admin.Line{At: m.now, Error: bad, Text: reply})
	if !bad && strings.HasPrefix(line, "set ") {
		return m.loadDevice() // the Device screen shows what the radio now holds
	}
	return nil
}

func (m *Model) devConsoleLines(w, rows int) []string {
	th, c := m.th, &m.device.con
	out := []string{th.Fg(th.Muted, true).Render("RADIO CONSOLE") + th.Fg(th.Dim, false).Render(clip("  ·  "+m.ready.Device.Firmware+" · enter send · ↑↓ history · tab complete · esc close", w-13)), ""}
	var lines []string
	for _, l := range c.lines {
		for i, part := range strings.Split(l.Text, "\n") {
			if strings.TrimSpace(part) == "" && i > 0 {
				continue
			}
			ll := l
			ll.Text = part
			if i > 0 {
				ll.Out = false
			}
			lines = append(lines, m.consoleRow(ll, w))
		}
	}
	if room := rows - 6; len(lines) > room {
		lines = lines[len(lines)-max(room, 0):]
	}
	if len(lines) == 0 {
		hint := "Commands go to your radio as you'd type them on its serial console. Try ver, get radio.fem.rxgain, or set tx 20."
		for _, l := range strings.Split(renderWidth.Wrap(hint, w, " "), "\n") {
			lines = append(lines, th.Fg(th.Dim, false).Render(l))
		}
	}
	out = append(out, lines...)
	wait := ""
	if c.busy {
		wait = m.waiting("waiting for a reply")
	}
	return append(append(out, ""), m.consolePrompt(th.Fg(th.Magenta, true).Render("radio▸ "), c.input, console.Companion(), c.tab, wait, w)...)
}
