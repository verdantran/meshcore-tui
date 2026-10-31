// Package console is what the radio consoles accept: a companion radio's commands, and a repeater's or room
// server's with the settings it can get and set. One table drives tab completion, the help shown as you type,
// and docs/console.md.
package console

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

// Command is one thing a console takes: the words typed, what follows them, and what it does.
type Command struct {
	Group  string   `toml:"group"`
	Words  string   `toml:"words"`
	Args   string   `toml:"args"`   // what follows, as shown: <dBm>, on|off
	Values []string `toml:"values"` // what the argument can be, when it's a short list
	Help   string   `toml:"help"`
	Roles  []string `toml:"roles"`  // repeater, room; empty means both
	Serial bool     `toml:"serial"` // only on the node's own USB console
}

//go:embed companion.toml
var companionTOML string

//go:embed node.toml
var nodeTOML string

func load(src, name string) []Command {
	var doc struct {
		Command []Command `toml:"command"`
	}
	if _, err := toml.Decode(src, &doc); err != nil {
		panic("console: " + name + ": " + err.Error())
	}
	return doc.Command
}

var companion, node = load(companionTOML, "companion.toml"), load(nodeTOML, "node.toml")

// Companion is what a companion radio's console takes.
func Companion() []Command { return companion }

// Node is what a repeater's or room server's console takes: its commands, then a get and a set for each setting.
func Node(t radio.ContactType) []Command {
	role := map[radio.ContactType]string{radio.TypeRepeater: "repeater", radio.TypeRoom: "room"}[t]
	var out []Command
	for _, c := range node {
		if len(c.Roles) == 0 || slices.Contains(c.Roles, role) {
			out = append(out, c)
		}
	}
	for _, s := range admin.Settings(t) {
		out = append(out, settingCommands(s, role)...)
	}
	return out
}

func settingCommands(s admin.Setting, role string) []Command {
	what := s.Label
	if s.Help != "" {
		what += ": " + s.Help
	}
	group := "Settings: " + s.Group
	get := Command{Group: group, Words: "get " + s.Key, Help: what, Roles: s.Roles}
	if s.ReadOnly {
		return []Command{get}
	}
	set := Command{Group: group, Words: "set " + s.Key, Help: what, Roles: s.Roles}
	switch s.Kind {
	case admin.KindBool:
		set.Values = []string{s.Wire("on"), s.Wire("off")}
	case admin.KindChoice:
		for _, o := range s.Options {
			set.Values = append(set.Values, s.Wire(o))
		}
	}
	switch {
	case len(set.Values) > 0:
		set.Args = strings.Join(set.Values, "|")
	case s.Kind == admin.KindRadio:
		set.Args = "<freq>,<bw>,<sf>,<cr>"
	case s.Kind == admin.KindPassword:
		set.Args = "<password>"
	case s.Min != nil && s.Max != nil:
		set.Args = fmt.Sprintf("<%s–%s%s>", num(*s.Min), num(*s.Max), unit(s.Unit))
	case s.Kind == admin.KindInt || s.Kind == admin.KindFloat:
		set.Args = "<n" + unit(s.Unit) + ">"
	default:
		set.Args = "<text>"
	}
	return []Command{get, set}
}

func num(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.3f", f), "0"), ".")
}

func unit(u string) string {
	if u == "" {
		return ""
	}
	return " " + u
}

// Hint is what the console shows for the line being typed.
type Hint struct {
	Ghost   string   // what tab adds, shown dim after the line
	Args    string   // the arguments still to type, shown dim; tab never adds these
	Matches []string // the lines it could become, when there's more than one
	Help    string   // what the command typed does
}

// Suggest looks at a line as it's typed: the command's words first, then its argument when it takes one of a
// few values. Nothing from the console's history is used, so nothing typed before is offered back.
func Suggest(cmds []Command, line string) Hint {
	in := strings.ToLower(strings.TrimLeft(line, " "))
	if in == "" {
		return Hint{}
	}
	// Typing a command's argument: the longest command whose words and a space start the line.
	var arg *Command
	for i := range cmds {
		c := &cmds[i]
		if strings.HasPrefix(in, c.Words+" ") && c.Args != "" && (arg == nil || len(c.Words) > len(arg.Words)) {
			arg = c
		}
	}
	if arg != nil {
		h := Hint{Help: helpOf(*arg)}
		typed := in[len(arg.Words)+1:]
		var vals []string
		for _, v := range arg.Values {
			if strings.HasPrefix(v, typed) {
				vals = append(vals, v)
			}
		}
		if typed == "" {
			h.Args = arg.Args // the choices, rather than the letters they happen to share
		} else if len(vals) > 0 {
			h.Ghost = common(vals)[len(typed):]
		}
		if len(vals) > 1 {
			for _, v := range vals {
				h.Matches = append(h.Matches, arg.Words+" "+v)
			}
		}
		return h
	}
	var words []string
	exact := -1
	for i, c := range cmds {
		if strings.HasPrefix(c.Words, in) && !slices.Contains(words, c.Words) {
			words = append(words, c.Words)
		}
		if c.Words == in && (exact < 0 || cmds[exact].Args == "") {
			exact = i
		}
	}
	if len(words) == 0 {
		return Hint{}
	}
	h := Hint{Ghost: common(words)[len(in):]}
	if len(words) > 1 {
		h.Matches = words
	}
	if exact >= 0 {
		h.Help = helpOf(cmds[exact])
		if h.Ghost == "" {
			h.Args = cmds[exact].Args
		}
	} else if len(words) == 1 {
		for _, c := range cmds {
			if c.Words == words[0] {
				h.Help = helpOf(c)
			}
		}
	}
	return h
}

func helpOf(c Command) string {
	if c.Serial {
		return c.Help + " Only on the node's own USB console."
	}
	return c.Help
}

// Takes reports whether the command written exactly so goes on to an argument.
func Takes(cmds []Command, words string) bool {
	return slices.ContainsFunc(cmds, func(c Command) bool { return c.Words == words && c.Args != "" })
}

func common(ss []string) string {
	c := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, c) {
			c = c[:len(c)-1]
		}
	}
	return c
}
