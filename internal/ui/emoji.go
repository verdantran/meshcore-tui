package ui

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/kyokomi/emoji/v2"
)

const emojiShown = 6

type shortcode struct{ name, emoji string } // name without its colons

var shortcodes = sync.OnceValue(func() []shortcode {
	var out []shortcode
	for code, e := range emoji.CodeMap() {
		out = append(out, shortcode{strings.Trim(code, ":"), e})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
})

// emojiPick is the list of emoji offered for the :shortcode being typed in the compose box.
type emojiPick struct {
	at      int // where the : is in the input
	matches []shortcode
	sel     int
}

func shortcodeChar(r byte) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '+' || r == '-'
}

// shortcodeAt finds a :shortcode being typed just before the cursor: a colon at the start or after a space, so
// times and links don't count, then at least two of the characters shortcodes use.
func shortcodeAt(before string) (at int, partial string, ok bool) {
	i := len(before)
	for i > 0 && shortcodeChar(before[i-1]) {
		i--
	}
	if i == 0 || before[i-1] != ':' || len(before)-i < 2 {
		return 0, "", false
	}
	if at = i - 1; at > 0 && before[at-1] != ' ' {
		return 0, "", false
	}
	return at, before[i:], true
}

// emojiMatches ranks the emoji for a partial shortcode: names starting with it, then names with a word
// starting with it, shorter names first, each emoji once.
func emojiMatches(partial string) []shortcode {
	var first, word []shortcode
	for _, sc := range shortcodes() {
		switch {
		case strings.HasPrefix(sc.name, partial):
			first = append(first, sc)
		case strings.Contains(sc.name, "_"+partial):
			word = append(word, sc)
		}
	}
	byLength := func(s []shortcode) {
		sort.SliceStable(s, func(i, j int) bool { return len(s[i].name) < len(s[j].name) })
	}
	byLength(first)
	byLength(word)
	seen := map[string]bool{}
	var out []shortcode
	for _, sc := range append(first, word...) {
		if !seen[sc.emoji] {
			seen[sc.emoji] = true
			out = append(out, sc)
		}
	}
	return out
}

// updateEmoji offers emoji for the shortcode being typed, and turns a finished one, :joy:, into its emoji.
func (m *Model) updateEmoji() {
	c := &m.chats
	c.emoji = nil
	before := c.input.before()
	if strings.HasSuffix(before, ":") {
		if at, partial, ok := shortcodeAt(before[:len(before)-1]); ok {
			if e, known := emoji.CodeMap()[":"+partial+":"]; known {
				c.input.replace(at, e)
				return
			}
		}
	}
	at, partial, ok := shortcodeAt(before)
	if !ok {
		c.emojiOff = 0
		return
	}
	if at+1 == c.emojiOff {
		return
	}
	if matches := emojiMatches(partial); len(matches) > 0 {
		c.emoji = &emojiPick{at: at, matches: matches}
	}
}

// emojiKey handles the keys the emoji list takes while it's showing, reporting whether it took this one.
func (m *Model) emojiKey(s string) bool {
	c := &m.chats
	p := c.emoji
	if p == nil {
		return false
	}
	switch s {
	case "up", "ctrl+p":
		p.sel = (p.sel + len(p.matches) - 1) % len(p.matches)
	case "down", "ctrl+n":
		p.sel = (p.sel + 1) % len(p.matches)
	case "tab", "enter":
		e := p.matches[p.sel].emoji
		if !strings.HasPrefix(c.input.text[c.input.pos:], " ") {
			e += " "
		}
		c.input.replace(p.at, e)
		c.emoji = nil
	case "esc":
		c.emoji, c.emojiOff = nil, p.at+1
	default:
		return false
	}
	return true
}

// emojiLines draws the list above the compose line, the chosen one raised, scrolled to keep it in view.
func (m *Model) emojiLines(w int) []string {
	th, p := m.th, m.chats.emoji
	start := min(max(p.sel-emojiShown+1, 0), max(len(p.matches)-emojiShown, 0))
	var out []string
	for i := start; i < len(p.matches) && i < start+emojiShown; i++ {
		sc := p.matches[i]
		line := " " + sc.emoji + "  :" + sc.name + ":"
		if i == p.sel {
			out = append(out, th.Fg(th.Magenta, true).Render("▌")+m.raised(fit(line, min(w-1, 40))))
			continue
		}
		out = append(out, " "+th.Fg(th.Text, false).Render(line))
	}
	more := ""
	if len(p.matches) > emojiShown {
		more = fmt.Sprintf(" · %d of %d", p.sel+1, len(p.matches))
	}
	return append(out, th.Fg(th.Dim, false).Render(" ↑↓ choose · tab or enter insert · esc close"+more))
}
