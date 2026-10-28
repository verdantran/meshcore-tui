package ui

import (
	"context"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

type chanStage int

const (
	chanNone chanStage = iota
	chanName
	chanSecret
	chanShare
	chanRemove
	chanRename
)

type channelsMsg struct {
	list  []radio.Channel
	err   error
	what  string
	focus store.Convo
	share bool // open the share view on the channel just made, so it can be passed on
}

func (m *Model) slots() int {
	if n := m.ready.Device.MaxChannels; n > 0 {
		return n
	}
	return 8
}

// writeChannel stores a channel in its slot and reads the list back, so Chats shows what the radio holds.
func (m *Model) writeChannel(c radio.Channel, what string, focus store.Convo, share bool) tea.Cmd {
	r := m.radio
	return m.radioCall(func(ctx context.Context) tea.Msg {
		if err := r.SetChannel(ctx, c); err != nil {
			return channelsMsg{err: err, what: what}
		}
		list, err := r.Channels(ctx)
		return channelsMsg{list: list, err: err, what: what, focus: focus, share: share}
	})
}

func (m *Model) channelsMsg(msg channelsMsg) tea.Cmd {
	c := &m.chats
	if msg.err != nil {
		m.fail(msg.what + "  " + radio.Explain(msg.err))
		return nil
	}
	if err := m.chat.ReplaceChannels(msg.list); err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
	}
	m.addLog(m.now, msg.what, radio.ToneOK)
	m.refreshChats()
	if msg.focus != "" {
		c.onAdd = false
		for i, info := range c.infos {
			if info.Convo == msg.focus {
				c.sel = i
			}
		}
		m.refreshChats()
	}
	if msg.share {
		if ch, ok := m.chat.ChannelFor(msg.focus); ok {
			c.chanStage, c.shareCh = chanShare, ch
		}
	}
	return nil
}

// addChannel plans a channel from what was typed and, if it's sound, sends it to the radio.
func (m *Model) addChannel(name, secret string) (tea.Cmd, error) {
	p, err := m.chat.PlanChannel(name, secret, m.slots())
	if err != nil {
		return nil, err
	}
	what := map[string]string{"hashtag": "JOINED  ", "private": "JOINED  ", "new": "CREATED  "}[p.Kind] + p.Channel.Name
	return m.writeChannel(p.Channel, what, store.ChannelConvo(p.Channel.Idx), p.Kind == "new"), nil
}

func (m *Model) removeChannel(ch radio.Channel) tea.Cmd {
	if err := m.chat.ForgetChannel(ch.Idx); err != nil {
		m.addLog(m.now, "CHAT STORE  "+err.Error(), radio.ToneWarn)
	}
	m.forgetSigs(ch.Idx)
	return m.writeChannel(radio.Channel{Idx: ch.Idx}, "LEFT  "+ch.Name, "", false)
}

func (m *Model) selectedChannel() (radio.Channel, bool) {
	cur, ok := m.selectedConvo()
	if !ok || m.chat == nil {
		return radio.Channel{}, false
	}
	return m.chat.ChannelFor(cur.Convo)
}

// channelKey handles the channel keys and prompts on the Chats screen.
func (m *Model) channelKey(s string) (tea.Cmd, bool) {
	c := &m.chats
	typing := func() { c.chanInput.key(s) }
	switch c.chanStage {
	case chanNone:
		switch s {
		case "n":
			m.startAddChannel("")
			return nil, true
		case "e", "x":
			ch, ok := m.selectedChannel()
			if !ok {
				return nil, false
			}
			c.shareCh, c.chanStage = ch, map[string]chanStage{"e": chanShare, "x": chanRemove}[s]
			return nil, true
		}
		return nil, false
	case chanName, chanSecret:
		switch s {
		case "esc":
			c.chanStage = chanNone
		case "enter":
			in := strings.TrimSpace(c.chanInput.text)
			if c.chanStage == chanName && !strings.HasPrefix(in, "#") && !strings.HasPrefix(in, chat.ChannelLinkPrefix) && in != "" {
				if len(untrusted.Name(in)) > 31 {
					m.fail("CHANNEL  a channel name is at most 31 bytes") // said now, not after the secret's typed
					return nil, true
				}
				c.chanName, c.chanInput, c.chanStage = in, lineEdit{}, chanSecret
				return nil, true
			}
			name, secret := in, ""
			if c.chanStage == chanSecret {
				name, secret = c.chanName, in
			}
			cmd, err := m.addChannel(name, secret)
			if err != nil {
				m.fail("CHANNEL  " + err.Error())
				return nil, true
			}
			c.chanStage = chanNone
			return cmd, true
		default:
			typing()
		}
		return nil, true
	case chanRename:
		switch s {
		case "esc":
			c.chanStage = chanNone
		case "enter":
			name := untrusted.Name(c.chanInput.text)
			switch {
			case name == "" || strings.HasPrefix(name, "#"):
				m.fail("RENAME  a private channel's name can't be empty or start with #")
				return nil, true
			case len(name) > 31:
				m.fail("RENAME  a channel name is at most 31 bytes")
				return nil, true
			}
			if other, ok := m.chat.ChannelNamed(name, c.shareCh.Idx); ok {
				m.fail("RENAME  you already have a channel called " + other.Name)
				return nil, true
			}
			c.chanStage = chanNone
			ch := c.shareCh
			ch.Name = name
			return m.writeChannel(ch, "RENAMED  "+c.shareCh.Name+" → "+name, store.ChannelConvo(ch.Idx), false), true
		default:
			typing()
		}
		return nil, true
	case chanShare:
		if s == "y" {
			m.addLog(m.now, "COPIED  link for "+c.shareCh.Name+"; it's cleared in 30 s", radio.ToneOK)
			return m.copySecret(chat.ChannelLink(c.shareCh)), true
		}
		c.chanStage = chanNone
	case chanRemove:
		c.chanStage = chanNone
		if s == "y" {
			return m.removeChannel(c.shareCh), true
		}
	}
	return nil, true
}

var hashtagWord = regexp.MustCompile(`(?:^|[^\p{L}\p{N}_&/#])(#[\p{L}\p{N}_-]*[\p{L}\p{N}_])`)

// joinable reports whether name is a hashtag channel, well formed and not one we're in already.
func (m *Model) joinable(name string) bool {
	if m.chat == nil || len(name) < 2 || name[0] != '#' || strings.ContainsAny(name[1:], " #") {
		return false
	}
	if strings.Trim(name[1:], "0123456789") == "" {
		return false // #1 is a number, not a channel
	}
	_, in := m.chat.ChannelNamed(name, -1)
	return !in
}

// hashtagIn is the first hashtag channel a message mentions that we're not in, or "".
func (m *Model) hashtagIn(text string) string {
	for _, sm := range hashtagWord.FindAllStringSubmatch(untrusted.Text(text), -1) {
		if m.joinable(sm[1]) {
			return sm[1]
		}
	}
	return ""
}

// joinHashtag joins a hashtag channel straight away; anyone can, so there's nothing to ask.
func (m *Model) joinHashtag(name string) tea.Cmd {
	cmd, err := m.addChannel(name, "")
	if err != nil {
		m.fail("CHANNEL  " + err.Error())
		return nil
	}
	m.setTab(0)
	return cmd
}

// startAddChannel opens the add-a-channel form on Chats, with name already typed in.
func (m *Model) startAddChannel(name string) {
	m.setTab(0)
	c := &m.chats
	c.writing, c.reading = false, false
	c.chanStage, c.chanInput, c.chanName = chanName, newLine(name), ""
}

// addPreview fills the conversation pane while the add row is picked, saying what adding a channel does.
func (m *Model) addPreview(w int) []string {
	th := m.th
	out := []string{th.Fg(th.Muted, true).Render("ADD A CHANNEL") + th.Fg(th.Dim, false).Render(fmt.Sprintf("  ·  %d of %d slots used", len(m.chat.Channels()), m.slots())), ""}
	for _, text := range []string{"Join a public hashtag channel by its name, like #bristol, join a private one with its secret or a link someone shared, or make a new private channel to share.",
		"Picking a message that mentions a #channel you're not in offers J to join it, and ctrl+k finds #names too."} {
		for _, l := range strings.Split(renderWidth.Wrap(text, w, " "), "\n") {
			out = append(out, th.Fg(th.Dim, false).Render(l))
		}
		out = append(out, "")
	}
	return append(out, th.Paint(th.Dim, false, "▸ ")+th.Paint(th.Cyan, true, "enter")+th.Paint(th.Dim, false, " add a channel · ↑↓ conversations"))
}

func (m *Model) channelLines(w, rows int) []string {
	th, c := m.th, &m.chats
	field := func(label string) string {
		return th.Fg(th.Magenta, true).Render(label+" ▸ ") + c.chanInput.render(th, w-len(label)-3, false)
	}
	wrap := func(text string) []string {
		var out []string
		for _, l := range strings.Split(renderWidth.Wrap(text, w, " "), "\n") {
			out = append(out, th.Fg(th.Dim, false).Render(l))
		}
		return out
	}
	switch c.chanStage {
	case chanName:
		out := []string{th.Fg(th.Muted, true).Render("ADD A CHANNEL") + th.Fg(th.Dim, false).Render(fmt.Sprintf("  ·  %d of %d slots used", len(m.chat.Channels()), m.slots())), "", field("channel"), ""}
		out = append(out, wrap("#name joins a public hashtag channel: everyone who types the same name meets there, so it's open to anyone who guesses it.")...)
		out = append(out, "")
		out = append(out, wrap("A plain name joins or makes a private channel; you'll be asked for its secret next. A pasted "+chat.ChannelLinkPrefix+"… link joins straight away.")...)
		return append(out, "", th.Fg(th.Dim, false).Render("enter continue · esc cancel"))
	case chanSecret:
		out := []string{th.Fg(th.Muted, true).Render("SECRET FOR " + strings.ToUpper(c.chanName)), "", field("secret"), ""}
		out = append(out, wrap("Paste the secret someone shared with you, as 32 hex characters or base64. Leave it empty to make a new private channel with a random secret, which you can then share.")...)
		return append(out, "", th.Fg(th.Dim, false).Render("enter add it · esc cancel"))
	case chanRename:
		out := []string{th.Fg(th.Muted, true).Render("RENAME " + strings.ToUpper(c.shareCh.Name)), "", field("name"), ""}
		out = append(out, wrap("The name is only for you: the secret is what decides who's in the channel, so others can call it something else.")...)
		return append(out, "", th.Fg(th.Dim, false).Render("enter rename it · esc cancel"))
	case chanRemove:
		return []string{th.Fg(th.Red, true).Render("LEAVE " + strings.ToUpper(c.shareCh.Name)), "",
			th.Fg(th.Text, false).Render("Remove it from the radio? Its message history here is deleted too."), "",
			th.Fg(th.Dim, false).Render("You can join again later with its name or secret. y leave · any other key cancel")}
	}
	return m.channelShareLines(w, rows)
}

func (m *Model) channelShareLines(w, rows int) []string {
	th, ch := m.th, m.chats.shareCh
	link := chat.ChannelLink(ch)
	out := []string{th.Fg(th.Muted, true).Render("SHARE "+strings.ToUpper(ch.Name)) + th.Fg(th.Dim, false).Render("  ·  y copy the link · any other key close"), ""}
	note := "Anyone with this secret can read and post in the channel."
	switch {
	case chat.IsPublic(ch):
		note = "This is the Public channel, which every MeshCore radio starts with; its secret is no secret."
	case chat.IsHashtag(ch):
		note = "A hashtag channel's secret comes from its name, so sharing the name " + ch.Name + " is enough."
	case chat.IsOpen(ch):
		note = "Anyone can work out this secret, so anyone can read this channel, whoever you share it with."
	}
	code := qrLines(link)
	linkRows := strings.Split(renderWidth.Hardwrap(link, w, false), "\n")
	if len(code) > 0 && len(code)+len(linkRows)+6 <= rows && visibleWidth(code[0]) <= w {
		pad := strings.Repeat(" ", max((w-visibleWidth(code[0]))/2, 0))
		for _, l := range code {
			out = append(out, pad+l)
		}
		out = append(out, "")
	}
	for _, l := range linkRows {
		out = append(out, th.Fg(th.Cyan, false).Render(l))
	}
	return append(out, "", th.Fg(th.Dim, false).Render("secret ")+th.Fg(th.Yellow, false).Render(hex.EncodeToString(ch.Secret[:])), "",
		th.Fg(th.Dim, false).Render(clip(note, w)))
}

func channelCommand() command {
	return command{name: "channel", args: "add|new|rename|remove|share <name> [secret]", help: "join, make, rename, leave or share a channel",
		run: func(m *Model, a []string) error {
			if m.chat == nil || len(a) < 2 {
				return fmt.Errorf("channel needs add, new, rename, remove or share, and a name")
			}
			name := strings.Join(a[1:], " ")
			switch a[0] {
			case "add", "new":
				secret := ""
				if a[0] == "add" && len(a) > 2 {
					name, secret = strings.Join(a[1:len(a)-1], " "), a[len(a)-1]
				}
				if a[0] == "add" && secret == "" && !strings.HasPrefix(name, "#") && !strings.HasPrefix(name, chat.ChannelLinkPrefix) {
					return fmt.Errorf("a private channel needs its secret; use channel new %s to make one", name)
				}
				cmd, err := m.addChannel(name, secret)
				if err != nil {
					return err
				}
				m.setTab(0)
				m.queued = cmd
				return nil
			case "rename", "remove", "share":
				for _, ch := range m.chat.Channels() {
					if strings.EqualFold(ch.Name, name) {
						if a[0] == "rename" && strings.HasPrefix(ch.Name, "#") {
							return fmt.Errorf("a hashtag channel's name is its secret, so %s can't be renamed", ch.Name)
						}
						m.jumpToConvo(store.ChannelConvo(ch.Idx))
						m.chats.shareCh, m.chats.chanInput = ch, newLine(ch.Name)
						m.chats.chanStage = map[string]chanStage{"rename": chanRename, "remove": chanRemove, "share": chanShare}[a[0]]
						return nil
					}
				}
				return fmt.Errorf("no channel called %q", name)
			}
			return fmt.Errorf("channel %s? try add, new, rename, remove or share", a[0])
		},
		complete: func(m *Model) []string {
			out := []string{"add", "new", "rename", "remove", "share"}
			if m.chat != nil {
				for _, ch := range m.chat.Channels() {
					out = append(out, ch.Name)
				}
			}
			return out
		}}
}
