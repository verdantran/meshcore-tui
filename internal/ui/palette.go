package ui

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
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
		{name: "beacon", args: "on|off", help: "show or hide the rings pulsing around you on the map",
			run: func(m *Model, a []string) error {
				m.beaconOn = len(a) == 0 && !m.beaconOn || len(a) > 0 && a[0] == "on"
				if m.opt.Config != nil {
					m.opt.Config.UI.Beacon = m.beaconOn
					m.saveConfig()
				}
				return nil
			},
			complete: func(*Model) []string { return []string{"on", "off"} }},
		{name: "mouse", args: "on|off", help: "let clicks pick screens and panes, or leave the mouse to the terminal for selecting text",
			run: func(m *Model, a []string) error {
				m.mouseOn = len(a) == 0 && !m.mouseOn || len(a) > 0 && a[0] == "on"
				if m.opt.Config != nil {
					m.opt.Config.UI.Mouse = m.mouseOn
					m.saveConfig()
				}
				return nil
			},
			complete: func(*Model) []string { return []string{"on", "off"} }},
		{name: "close", help: "close the focused pane", run: func(m *Model, _ []string) error { return m.closePane() }},
		{name: "frames", help: "show or hide the protocol frames pane", run: func(m *Model, _ []string) error { m.debug = !m.debug; return nil }},
		{name: "help", help: "list keys and commands", run: func(m *Model, _ []string) error { m.help = true; return nil }},
		{name: "version", help: "show which version of meshcore-tui this is", run: func(m *Model, _ []string) error {
			v := m.opt.Version
			if v == "" {
				v = "unknown version"
			}
			m.addLog(m.now, "VERSION  meshcore-tui "+v, radio.ToneInfo)
			return nil
		}},
		{name: "quit", help: "leave meshcore-tui", run: func(m *Model, _ []string) error { m.quitting = true; return nil }},
	}
	for i := range tabNames {
		if i != 2 { // :map has its own command, which also manages the street detail
			cs = append(cs, screenCommand(i))
		}
	}
	cs = append(cs, splitCommand())
	cs = append(cs, muteCommands()...)
	return cs
}

// commandSet is extended by later features (messaging adds msg), so it's built on the model.
func (m *Model) commandSet() []command {
	return append(commands(), m.extraCommands...)
}

type palette struct {
	open    bool
	input   lineEdit
	history []string
	hist    int // index into history while browsing; len(history) when not
}

func (m *Model) openPalette() {
	m.pal.open, m.pal.input, m.pal.hist = true, lineEdit{}, len(m.pal.history)
}

func (m *Model) paletteKey(s string) {
	p := &m.pal
	switch s {
	case "esc":
		p.open = false
	case "enter":
		line := strings.TrimSpace(p.input.text)
		p.open = false
		if line == "" {
			return
		}
		p.history = append(p.history, line)
		m.runCommand(line)
	case "tab":
		p.input.set(m.completeInput(p.input.text))
	case "up":
		if p.hist > 0 {
			p.hist--
			p.input.set(p.history[p.hist])
		}
	case "down":
		if p.hist < len(p.history)-1 {
			p.hist++
			p.input.set(p.history[p.hist])
		} else {
			p.hist, p.input = len(p.history), lineEdit{}
		}
	default:
		p.input.key(s)
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
	in := m.pal.input.text
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
	prompt := th.Fg(th.Magenta, true).Render(" mc▸ ") + m.pal.input.render(th, m.w-6, false)
	hint := strings.Join(m.suggestions(), "  ")
	return prompt + "  " + th.Fg(th.Dim, false).Render(truncate(hint, max(m.w-visibleWidth(prompt)-3, 0)))
}

// helpKey scrolls the ? overlay; esc, ?, q and enter close it.
func (m *Model) helpKey(s string) {
	room := m.mainHeight(m.bodyHeight()) - 2
	last := max(len(m.helpLines())-room, 0)
	switch s {
	case "esc", "?", "q", "enter":
		m.help = false
	case "down", "j":
		m.helpScroll++
	case "up", "k":
		m.helpScroll--
	case "pgdown", "space", "ctrl+d":
		m.helpScroll += room / 2
	case "pgup", "ctrl+u":
		m.helpScroll -= room / 2
	case "g", "home":
		m.helpScroll = 0
	case "G", "end":
		m.helpScroll = last
	}
	m.helpScroll = min(max(m.helpScroll, 0), last)
}

// helpRow is a key and what it does, as the ? overlay lists them.
func helpRow(th theme.Theme, k, what string) string {
	const col = 16
	pad := col - visibleWidth(k)
	if pad < 2 {
		pad = 2
	}
	return th.Fg(th.Cyan, true).Render(k+strings.Repeat(" ", pad)) + th.Fg(th.Muted, false).Render(what)
}

// helpLines is the ? overlay: keys first, then every command.
func (m *Model) helpLines() []string {
	th := m.th
	key := func(k, what string) string { return helpRow(th, k, what) }
	lines := []string{th.Fg(th.Muted, true).Render("KEYS")}
	lines = append(lines, m.screenKeys()...)
	if mine := m.myKeys(); len(mine) > 0 {
		lines = append(lines, th.Fg(th.Muted, true).Render("YOUR KEYS")+th.Fg(th.Dim, false).Render("  from keys.toml"))
		for _, b := range mine {
			what := "does what " + b[1] + " does"
			if b[1] == "" {
				what = "does nothing here"
			}
			lines = append(lines, key(b[0], what))
		}
		lines = append(lines, "")
	}
	lines = append(lines, th.Fg(th.Muted, true).Render("EVERYWHERE"))
	if m.repeaterMode() {
		lines = append(lines, key("1-9", "switch screen"), key("tab shift+tab", "next screen · previous screen; ←→ too"))
	} else {
		lines = append(lines, key("1-7", "switch screen"), key("tab shift+tab", "next screen · previous screen"),
			key("M", "open the map; on a packet, message or contact, its path"))
	}
	lines = append(lines, key(":", "command palette"), key("?", "this help: j k scroll, esc closes"))
	if !m.repeaterMode() {
		lines = append(lines, key("ctrl+f", "search every conversation"))
	}
	lines = append(lines,
		key("T", "next theme"), key("v", "cycle motion: full, reduced, off"), key("F12", "protocol frames; :frames too"),
		key("q", "quit; asks first if a message or a settings change isn't sent yet"), key("ctrl+c", "quit, always"),
		key("click", "a screen's name in the tab bar goes to it; a click in a pane focuses it; the wheel scrolls"), "",
		th.Fg(th.Muted, true).Render("TEXT FIELDS"),
		key("enter esc", "commit · cancel"), key("←→ home end", "move the cursor; ctrl+←→ or alt+b alt+f by word"),
		key("ctrl+w", "delete a word; alt+backspace too"), key("ctrl+u ctrl+k", "delete to the start · to the end"), "")
	if !m.repeaterMode() {
		lines = append(lines, th.Fg(th.Muted, true).Render("PANES"),
			key("ctrl+w v", "split right"), key("ctrl+w s", "split down"), key("ctrl+w ←↑↓→", "move between panes; h j k l and w too"),
			key("ctrl+w q", "close the pane"), key("ctrl+w < >", "narrower · wider"), key("ctrl+w - +", "shorter · taller"),
			key("ctrl+w =", "even out the panes"), key("ctrl+w z", "zoom the pane, and back"), "")
	}
	lines = append(lines, th.Fg(th.Muted, true).Render("COMMANDS"))
	for _, c := range m.commandSet() {
		lines = append(lines, key(c.name, strings.TrimSpace(c.args+"  "+c.help)))
	}
	return lines
}

// unfinished is why quitting should wait: something typed or staged that hasn't been sent.
func (m *Model) unfinished() string {
	if strings.TrimSpace(m.chats.input.text) != "" {
		return "your message isn't sent"
	}
	if m.admin != nil && len(m.admin.Changes(m.adm.open)) > 0 {
		return "settings changes aren't applied"
	}
	return ""
}

// convoLabel names a conversation so that two with the same title can be told apart.
func (m *Model) convoLabel(c store.Convo, title string) string {
	if c.IsChannel() {
		return "the channel " + title
	}
	if ct, ok := m.chat.Contact(c); ok {
		return fmt.Sprintf("%s (%s)", title, radio.KeyHex(ct.Key[:4]))
	}
	return title
}

func (m *Model) convoIndex(c store.Convo) int {
	for i, info := range m.chats.infos {
		if info.Convo == c {
			return i
		}
	}
	return -1
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
			onlyChannels := strings.HasPrefix(a[0], "#")
			// Someone you haven't messaged yet is a candidate too: their conversation starts, as on Contacts.
			var found []store.Convo
			var names []string
			for _, info := range m.chats.infos {
				if slug(info.Title) == want && (!onlyChannels || info.Kind == chat.KindChannel) {
					found, names = append(found, info.Convo), append(names, m.convoLabel(info.Convo, info.Title))
				}
			}
			for _, ct := range m.chat.Contacts() {
				c := store.ContactConvo(ct.Key)
				if slug(ct.Name) == want && ct.Type == radio.TypeChat && !onlyChannels && !slices.Contains(found, c) {
					found, names = append(found, c), append(names, m.convoLabel(c, ct.Name))
				}
			}
			switch {
			case len(found) > 1:
				return fmt.Errorf("%q could be %s; open the one you mean in Chats", a[0], strings.Join(names, " or "))
			case len(found) == 1:
				if i := m.convoIndex(found[0]); i >= 0 {
					m.chats.sel = i
				} else {
					m.openDraft(found[0])
				}
				m.chats.input.set(strings.Join(a[1:], " "))
				m.refreshChats()
				m.queued = m.composeSend()
				return nil
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

// keyScopes are the keys.toml sections for what has the keyboard now, most particular first.
func (m *Model) keyScopes() []string {
	switch {
	case m.paneKey:
		return []string{"panes"}
	case m.phase == phaseConnect:
		return []string{"connect", "all"}
	case m.phase != phaseMain:
		return []string{"all"}
	}
	screen := strings.ToLower(tabNames[m.tab])
	if v := m.keyView(); v != "" {
		return []string{screen + "." + v, screen, "all"}
	}
	return []string{screen, "all"}
}

// keyView is the view within the screen that keys.toml can rebind by itself, if any.
func (m *Model) keyView() string {
	switch {
	case m.tab == 0 && m.chats.reading:
		return "reading"
	case m.tab == 1 && m.contacts.heardView:
		return "heard"
	case m.tab == 2 && m.mapv.path != nil:
		return "path"
	case m.tab == 4 && !m.adm.opened && !m.adm.local:
		return "list"
	case m.tab == 4:
		return []string{"status", "health", "settings", "neighbours", "access", "regions", "telemetry", "log", ""}[m.adm.pane]
	}
	return ""
}

// rebound is what a pressed key stands for, from the most particular section that binds it.
func (m *Model) rebound(s string) (string, bool) {
	for _, scope := range m.keyScopes() {
		if to, ok := m.opt.Keys[scope][s]; ok {
			return to, true
		}
	}
	return "", false
}

// myKeys are the rebindings in force here, as key and what it stands for, sorted.
func (m *Model) myKeys() [][2]string {
	seen := map[string]bool{}
	var out [][2]string
	for _, scope := range m.keyScopes() {
		for from, to := range m.opt.Keys[scope] {
			if !seen[from] {
				seen[from] = true
				out = append(out, [2]string{from, to})
			}
		}
	}
	slices.SortFunc(out, func(a, b [2]string) int { return strings.Compare(a[0], b[0]) })
	return out
}
