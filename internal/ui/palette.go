package ui

import (
	"fmt"
	"sort"
	"strings"

	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/store"
	"meshcore-tui/internal/theme"
)

type command struct {
	name, args, help string
	run              func(m *Model, args []string) error
	complete         func(m *Model) []string
}

func screenCommand(i int) command {
	name := strings.ToLower(tabNames[i])
	return command{name: name, help: "go to " + name, run: func(m *Model, _ []string) error { m.setTab(i); return nil }}
}

func commands() []command {
	cs := []command{
		{name: "theme", args: "[name]", help: "switch theme, or cycle with no name",
			run: func(m *Model, a []string) error {
				if len(a) == 0 {
					m.cycleTheme()
					return nil
				}
				for _, t := range theme.All {
					if t.Name == a[0] {
						m.setTheme(t)
						return nil
					}
				}
				return fmt.Errorf("no theme %q; try %s", a[0], strings.Join(theme.Names(), ", "))
			},
			complete: func(*Model) []string { return theme.Names() }},
		{name: "motion", args: "full|reduced|off", help: "set how much animates",
			run: func(m *Model, a []string) error {
				if len(a) == 0 {
					return fmt.Errorf("motion needs full, reduced or off")
				}
				l, ok := motion.ParseLevel(a[0])
				if !ok {
					return fmt.Errorf("no motion level %q", a[0])
				}
				m.setMotion(l)
				return nil
			},
			complete: func(*Model) []string { return []string{"full", "reduced", "off"} }},
		{name: "ticker", args: "on|off", help: "show or hide the packet ticker",
			run: func(m *Model, a []string) error {
				m.tickerOn = len(a) == 0 && !m.tickerOn || len(a) > 0 && a[0] == "on"
				if m.opt.Config != nil {
					m.opt.Config.UI.PacketTicker = m.tickerOn
					m.saveConfig()
				}
				return nil
			},
			complete: func(*Model) []string { return []string{"on", "off"} }},
		{name: "close", help: "close the focused pane", run: func(m *Model, _ []string) error { return m.closePane() }},
		{name: "frames", help: "show or hide the protocol frames pane", run: func(m *Model, _ []string) error { m.debug = !m.debug; return nil }},
		{name: "help", help: "list keys and commands", run: func(m *Model, _ []string) error { m.help = true; return nil }},
		{name: "quit", help: "leave meshcore-tui", run: func(m *Model, _ []string) error { m.quitting = true; return nil }},
	}
	for i := range tabNames {
		if i != 2 { // :map has its own command, which also manages the street detail
			cs = append(cs, screenCommand(i))
		}
	}
	cs = append(cs, splitCommand())
	return cs
}

// commandSet is extended by later features (messaging adds msg), so it's built on the model.
func (m *Model) commandSet() []command {
	return append(commands(), m.extraCommands...)
}

type palette struct {
	open    bool
	input   string
	history []string
	hist    int // index into history while browsing; len(history) when not
}

func (m *Model) openPalette() {
	m.pal.open, m.pal.input, m.pal.hist = true, "", len(m.pal.history)
}

func (m *Model) paletteKey(s string) {
	p := &m.pal
	switch s {
	case "esc":
		p.open = false
	case "enter":
		line := strings.TrimSpace(p.input)
		p.open = false
		if line == "" {
			return
		}
		p.history = append(p.history, line)
		m.runCommand(line)
	case "backspace":
		p.input = dropLast(p.input)
	case "tab":
		p.input = m.completeInput(p.input)
	case "up":
		if p.hist > 0 {
			p.hist--
			p.input = p.history[p.hist]
		}
	case "down":
		if p.hist < len(p.history)-1 {
			p.hist++
			p.input = p.history[p.hist]
		} else {
			p.hist, p.input = len(p.history), ""
		}
	case "space":
		p.input += " "
	default:
		if len([]rune(s)) == 1 {
			p.input += s
		}
	}
}

func (m *Model) runCommand(line string) {
	fields := strings.Fields(line)
	for _, c := range m.commandSet() {
		if c.name == fields[0] {
			if err := c.run(m, fields[1:]); err != nil {
				m.fail(err.Error())
			}
			return
		}
	}
	m.fail(fmt.Sprintf("unknown command %q; type help for a list", fields[0]))
}

func (m *Model) fail(text string) {
	m.addLog(m.now, text, radio.ToneFail)
	m.glitch = m.now
}

// completeInput extends the word being typed to the longest prefix all its candidates share.
func (m *Model) completeInput(in string) string {
	fields := strings.Fields(in)
	trailing := strings.HasSuffix(in, " ")
	var options []string
	prefix := ""
	switch {
	case len(fields) == 0 || len(fields) == 1 && !trailing:
		for _, c := range m.commandSet() {
			options = append(options, c.name)
		}
		if len(fields) == 1 {
			prefix = fields[0]
		}
	default:
		for _, c := range m.commandSet() {
			if c.name == fields[0] && c.complete != nil {
				options = c.complete(m)
			}
		}
		if !trailing {
			prefix = fields[len(fields)-1]
		}
	}
	var match []string
	for _, o := range options {
		if strings.HasPrefix(o, prefix) {
			match = append(match, o)
		}
	}
	if len(match) == 0 {
		return in
	}
	common := match[0]
	for _, o := range match[1:] {
		for !strings.HasPrefix(o, common) {
			common = common[:len(common)-1]
		}
	}
	head := strings.TrimSuffix(in, prefix)
	out := head + common
	if len(match) == 1 {
		out += " "
	}
	return out
}

// suggestions lists what tab would complete to, for the dim hint beside the prompt.
func (m *Model) suggestions() []string {
	in := m.pal.input
	fields := strings.Fields(in)
	var out []string
	if len(fields) <= 1 && !strings.HasSuffix(in, " ") {
		prefix := ""
		if len(fields) == 1 {
			prefix = fields[0]
		}
		for _, c := range m.commandSet() {
			if strings.HasPrefix(c.name, prefix) {
				out = append(out, c.name)
			}
		}
		sort.Strings(out)
		return out
	}
	for _, c := range m.commandSet() {
		if c.name == fields[0] {
			if c.args != "" {
				out = append(out, c.args)
			}
			out = append(out, "— "+c.help)
		}
	}
	return out
}

func (m *Model) paletteLine() string {
	th := m.th
	prompt := th.Fg(th.Magenta, true).Render(" mc▸ ") + th.Fg(th.Text, false).Render(m.pal.input) + th.Fg(th.Text, true).Render("█")
	hint := strings.Join(m.suggestions(), "  ")
	return prompt + "  " + th.Fg(th.Dim, false).Render(truncate(hint, max(m.w-visibleWidth(prompt)-3, 0)))
}

// helpLines is the ? overlay: keys first, then every command.
func (m *Model) helpLines() []string {
	th := m.th
	key := func(k, what string) string {
		return th.Fg(th.Cyan, true).Render(fmt.Sprintf("%-12s", k)) + th.Fg(th.Muted, false).Render(what)
	}
	lines := []string{th.Fg(th.Muted, true).Render("KEYS")}
	lines = append(lines, m.screenKeys()...)
	lines = append(lines,
		key("1-7", "switch screen"), key("tab", "next screen"), key(":", "command palette"), "",
		th.Fg(th.Muted, true).Render("PANES"),
		key("ctrl+w v", "split right"), key("ctrl+w s", "split down"), key("ctrl+w ←↑↓→", "move between panes; h j k l and w too"),
		key("ctrl+w q", "close the pane"), key("ctrl+w < >", "narrower · wider"), key("ctrl+w - +", "shorter · taller"),
		key("ctrl+w =", "even out the panes"), key("ctrl+w z", "zoom the pane, and back"), "",
		key("T", "next theme"), key("v", "cycle motion: full, reduced, off"), key("F12", "protocol frames"),
		key("?", "this help"), key("q", "quit"), "",
		th.Fg(th.Muted, true).Render("COMMANDS"))
	for _, c := range m.commandSet() {
		lines = append(lines, key(c.name, strings.TrimSpace(c.args+"  "+c.help)))
	}
	return lines
}

// slug is how a conversation is typed in the palette: lower case, spaces as underscores, no leading #.
func slug(title string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(title, "#"), " ", "_"))
}

func msgCommand() command {
	return command{name: "msg", args: "<conversation> <text>", help: "send a message without leaving this screen",
		run: func(m *Model, a []string) error {
			if m.chat == nil {
				return fmt.Errorf("no conversations yet")
			}
			if len(a) < 2 {
				return fmt.Errorf("msg needs a conversation and some text")
			}
			want := strings.ToLower(strings.TrimPrefix(a[0], "#"))
			for i, info := range m.chats.infos {
				if slug(info.Title) == want {
					m.chats.sel = i
					m.chats.input = strings.Join(a[1:], " ")
					m.refreshChats()
					m.queued = m.composeSend()
					return nil
				}
			}
			// Someone you haven't messaged yet: start their conversation, as Contacts does.
			for _, ct := range m.chat.Contacts() {
				if slug(ct.Name) == want && ct.Type == radio.TypeChat {
					m.openDraft(store.ContactConvo(ct.Key))
					m.chats.input = strings.Join(a[1:], " ")
					m.refreshChats()
					m.queued = m.composeSend()
					return nil
				}
			}
			return fmt.Errorf("no conversation or contact called %q", a[0])
		},
		complete: func(m *Model) []string {
			var out []string
			seen := map[string]bool{}
			for _, info := range m.chats.infos {
				out, seen[slug(info.Title)] = append(out, slug(info.Title)), true
			}
			if m.chat != nil {
				for _, ct := range m.chat.Contacts() {
					if ct.Type == radio.TypeChat && !seen[slug(ct.Name)] {
						out = append(out, slug(ct.Name))
					}
				}
			}
			return out
		}}
}

func traceCommand() command {
	return command{name: "trace", args: "<contact>", help: "trace the route to a contact, showing signal at each hop",
		run: func(m *Model, a []string) error {
			if m.chat == nil || len(a) == 0 {
				return fmt.Errorf("trace needs a contact")
			}
			for _, ct := range m.chat.Contacts() {
				if slug(ct.Name) == strings.ToLower(a[0]) {
					m.setTab(1)
					for i, row := range m.contactRows() {
						if row.Key == ct.Key {
							m.contacts.sel = i
						}
					}
					m.queued = m.startTrace(ct)
					return nil
				}
			}
			return fmt.Errorf("no contact called %q", a[0])
		},
		complete: func(m *Model) []string {
			var out []string
			if m.chat != nil {
				for _, ct := range m.chat.Contacts() {
					out = append(out, slug(ct.Name))
				}
			}
			return out
		}}
}
