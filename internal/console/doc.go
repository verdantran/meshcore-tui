package console

import (
	"fmt"
	"slices"
	"strings"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

// Markdown is docs/console.md, written from the same tables the consoles complete from, so the two can't
// drift apart: go run ./scripts/consoledoc rewrites it, and a test fails while it's out of date.
func Markdown() string {
	var b strings.Builder
	b.WriteString(`# Console commands

<!-- Written by go run ./scripts/consoledoc from internal/console and internal/admin/settings.toml; edit those, not this. -->

Two consoles take commands as you'd type them on a radio's serial console:

- **Your own radio:** ` + "`c`" + ` on the Device screen, or ` + "`:radio console`" + `. It needs companion firmware v1.18.0 or newer.
- **A repeater or room server:** the Console pane on the Admin screen, once you're logged in as admin, or a repeater on USB with ` + "`--repeater`" + `.

As you type, the rest of a command shows dimmed after the cursor, and ` + "`tab`" + ` adds it. When several commands match, they're listed under the prompt, and ` + "`tab`" + ` and ` + "`shift+tab`" + ` step through them. Once a command is complete, the line underneath says what it does, and what it takes shows after the cursor. Suggestions come only from these lists, never from what you've typed before; ` + "`↑`" + ` and ` + "`↓`" + ` go through your history, which is kept for each node separately and forgotten when the app quits.

Commands that take a radio away, could lock you out or change what you can hear ask for confirmation first, and commands carrying a password or PIN are left out of the history.

## Before changing radio settings

Radio rules where you are limit the frequencies you may use, how much power you may transmit and how much of the time you may transmit. **Check your local regulations before changing** any of these, marked *check local regulations* below:

- **Frequency and bandwidth** (` + "`set radio`" + `, ` + "`tempradio`" + `, and the presets and radio rows on the Device screen): a signal takes up its frequency plus or minus half the bandwidth, so a wider bandwidth near the edge of a band can spill outside it.
- **Transmit power** (` + "`set tx`" + `, and the Device screen's TX power): limits usually apply to radiated power, which is the setting plus your antenna's gain.
- **The front-end amplifier** (` + "`set radio.fem.txgain`" + `, on boards that have one): it adds power on top of the TX setting.
- **Duty cycle and airtime** (` + "`set dutycycle`" + `, ` + "`set af`" + `): some bands limit how much of the time you may transmit.
- **Listening before transmitting** (` + "`set cad`" + `): some rules accept it in place of a duty cycle limit.
- **Repeating** (` + "`set repeat`" + ` on a repeater): passing on other nodes' packets adds to your airtime.

The app asks before some of these changes, but it doesn't know your local rules.

## The commands

These are the commands of MeshCore firmware v1.18.0, read from its source. Some depend on the board: WiFi commands only answer on WiFi builds, and the FEM gain only on boards with a front-end module. [MeshCore's CLI reference](https://github.com/meshcore-dev/MeshCore/blob/main/docs/cli_commands.md) has more detail on the shared settings.

### Your own radio

`)
	writeTable(&b, Companion())
	b.WriteString("\n### Repeaters and room servers\n\nMarked *repeater* where a room server doesn't have it, and *USB only* where it only answers on the node's own USB console, not over the mesh.\n\n")
	rep := Node(radio.TypeRepeater)
	writeTable(&b, rep)
	var roomOnly []Command
	for _, c := range Node(radio.TypeRoom) {
		if !slices.ContainsFunc(rep, func(r Command) bool { return r.Words == c.Words }) {
			c.Group = "Room servers only"
			roomOnly = append(roomOnly, c)
		}
	}
	writeTable(&b, roomOnly)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// writeTable writes a table per group, in the order groups first appear, with a get and set of the same key
// on one row.
func writeTable(b *strings.Builder, cmds []Command) {
	var groups []string
	for _, c := range cmds {
		if !slices.Contains(groups, c.Group) {
			groups = append(groups, c.Group)
		}
	}
	for _, g := range groups {
		fmt.Fprintf(b, "#### %s\n\n| Command | Takes | What it does |\n|---|---|---|\n", g)
		var in []Command
		for _, c := range cmds {
			if c.Group == g {
				in = append(in, c)
			}
		}
		for i := 0; i < len(in); i++ {
			c := in[i]
			words := "`" + c.Words + "`"
			if key, ok := strings.CutPrefix(c.Words, "get "); ok && i+1 < len(in) && in[i+1].Words == "set "+key {
				c, words = in[i+1], "`get` / `set "+key+"`"
				i++
			}
			takes := ""
			if c.Args != "" {
				takes = "`" + strings.ReplaceAll(c.Args, "|", `\|`) + "`"
			}
			help := strings.ReplaceAll(c.Help, "|", `\|`)
			var marks []string
			if len(c.Roles) == 1 && c.Roles[0] == "repeater" {
				marks = append(marks, "*repeater*")
			}
			if c.Serial {
				marks = append(marks, "*USB only*")
			}
			if regulated(c.Words) {
				marks = append(marks, "*check local regulations*")
			}
			if len(marks) > 0 {
				help = strings.Join(marks, ", ") + ". " + help
			}
			fmt.Fprintf(b, "| %s | %s | %s |\n", words, takes, help)
		}
		b.WriteString("\n")
	}
}

// regulated reports whether a command changes something radio rules limit: frequency, bandwidth, power or airtime.
func regulated(words string) bool {
	key, ok := strings.CutPrefix(words, "set ")
	if !ok {
		return words == "tempradio"
	}
	switch key {
	case "radio", "freq", "bw", "tx", "radio.fem.txgain", "dutycycle", "af", "cad", "repeat":
		return true
	}
	return false
}
