package chat

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

// ChannelLinkPrefix starts a shared channel. Check it against the official app before relying on it elsewhere.
const ChannelLinkPrefix = "meshcore://channel/add?"

const maxChannelName = 31 // the firmware's name field is 32 bytes with its terminator

var ErrNoSlot = errors.New("every channel slot on the radio is in use; remove a channel first")

// ChannelPlan is a channel ready to write to the radio, and how it came about.
type ChannelPlan struct {
	Channel radio.Channel
	Kind    string // "hashtag", "private" or "new"
}

// PlanChannel works out the channel to add from what was typed. A name starting # is a hashtag
// channel, whose secret comes from its name; a link carries its own; a name with a secret joins a
// private channel; a name alone makes a new private channel with a random secret.
func (s *Service) PlanChannel(name, secret string, slots int) (ChannelPlan, error) {
	name, secret = strings.TrimSpace(name), strings.TrimSpace(secret)
	if !strings.HasPrefix(name, ChannelLinkPrefix) {
		name = untrusted.Name(name)
	}
	var p ChannelPlan
	switch {
	case strings.HasPrefix(name, ChannelLinkPrefix):
		n, sec, err := ParseChannelLink(name)
		if err != nil {
			return p, err
		}
		p = ChannelPlan{radio.Channel{Name: n, Secret: sec}, "private"}
		if strings.HasPrefix(n, "#") {
			if sec != meshcore.DeriveHashtagPSK(n) {
				return p, fmt.Errorf("that link's secret isn't %s's, so it would look open when it isn't", n)
			}
			p.Kind = "hashtag"
		} else if err := guessable(p.Channel); err != nil {
			return p, err
		}
	case strings.HasPrefix(name, "#"):
		if strings.ContainsAny(name[1:], " #") || len(name) < 2 {
			return p, errors.New("a hashtag channel is # and one word, like #bristol")
		}
		p = ChannelPlan{radio.Channel{Name: name, Secret: meshcore.DeriveHashtagPSK(name)}, "hashtag"}
	case name == "":
		return p, errors.New("a channel needs a name")
	case secret == "":
		var sec [16]byte
		rand.Read(sec[:])
		p = ChannelPlan{radio.Channel{Name: name, Secret: sec}, "new"}
	default:
		sec, err := ParseSecret(secret)
		if err != nil {
			return p, err
		}
		p = ChannelPlan{radio.Channel{Name: name, Secret: sec}, "private"}
		if err := guessable(p.Channel); err != nil {
			return p, err
		}
	}
	if len(p.Channel.Name) > maxChannelName {
		return p, fmt.Errorf("a channel name is at most %d bytes", maxChannelName)
	}
	for _, c := range s.channels {
		if c.Secret == p.Channel.Secret {
			return p, fmt.Errorf("you're already in that channel, as %s", c.Name)
		}
	}
	if c, ok := s.ChannelNamed(p.Channel.Name, -1); ok {
		return p, fmt.Errorf("you already have a channel called %s; give this one another name", c.Name)
	}
	idx, ok := s.freeSlot(slots)
	if !ok {
		return p, ErrNoSlot
	}
	p.Channel.Idx = idx
	return p, nil
}

// guessable refuses a private channel whose secret anyone could work out, since it would look private
// when it isn't.
func guessable(c radio.Channel) error {
	switch {
	case IsPublic(c):
		return errors.New("that secret is the Public channel's, which every radio has, so the channel wouldn't be private")
	case derived(c):
		return fmt.Errorf("that secret is #%s's, which anyone can work out from the name; join #%s instead, or use another secret", c.Name, c.Name)
	case IsOpen(c):
		return errors.New("that secret is too easy to guess, so anyone could read the channel; leave it empty for a random one")
	}
	return nil
}

// ChannelNamed finds a channel other than the one in slot except whose name reads the same as this one.
func (s *Service) ChannelNamed(name string, except int) (radio.Channel, bool) {
	n, sk := untrusted.Name(name), untrusted.Skeleton(name)
	for _, c := range s.channels {
		if c.Idx != except && (strings.EqualFold(untrusted.Name(c.Name), n) || untrusted.Skeleton(c.Name) == sk) {
			return c, true
		}
	}
	return radio.Channel{}, false
}

func (s *Service) freeSlot(slots int) (int, bool) {
	used := map[int]bool{}
	for _, c := range s.channels {
		used[c.Idx] = true
	}
	for i := 0; i < slots; i++ {
		if !used[i] {
			return i, true
		}
	}
	return 0, false
}

// ParseSecret reads a 16-byte channel secret written as 32 hex characters or as base64.
func ParseSecret(s string) ([16]byte, error) {
	var out [16]byte
	s = strings.TrimSpace(s)
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		b, err = base64.StdEncoding.DecodeString(s)
	}
	if err != nil || len(b) != 16 {
		return out, errors.New("a channel secret is 32 hex characters, or 16 bytes of base64")
	}
	copy(out[:], b)
	return out, nil
}

// ChannelLink is how a channel is shared: its name and secret.
func ChannelLink(c radio.Channel) string {
	return ChannelLinkPrefix + "name=" + url.QueryEscape(c.Name) + "&secret=" + hex.EncodeToString(c.Secret[:])
}

func ParseChannelLink(link string) (string, [16]byte, error) {
	var sec [16]byte
	q, err := url.ParseQuery(strings.TrimPrefix(strings.TrimSpace(link), ChannelLinkPrefix))
	if err != nil || q.Get("name") == "" || q.Get("secret") == "" {
		return "", sec, errors.New("that link isn't a channel: it needs a name and a secret")
	}
	name := untrusted.Name(q.Get("name"))
	if name == "" {
		return "", sec, errors.New("that link's channel name is blank")
	}
	sec, err = ParseSecret(q.Get("secret"))
	return name, sec, err
}

// IsPublic reports whether a channel is the well-known Public one, which anyone can read.
func IsPublic(c radio.Channel) bool {
	return bytes.Equal(c.Secret[:], meshcore.PublicChannel().PSK)
}

// IsOpen reports whether anyone can read a channel: Public, one whose secret is its name's as a hashtag,
// whether or not the name starts #, a well-known hashtag's, or one with too few different bytes to be secret.
func IsOpen(c radio.Channel) bool {
	if IsPublic(c) || derived(c) || commonHashtags[c.Secret] {
		return true
	}
	seen := map[byte]bool{}
	for _, b := range c.Secret {
		seen[b] = true
	}
	return len(seen) < 4
}

// IsHashtag reports whether a channel is a hashtag channel proper, named # and keyed by that name, so the
// name alone is enough to join it.
func IsHashtag(c radio.Channel) bool {
	return strings.HasPrefix(c.Name, "#") && c.Secret == meshcore.DeriveHashtagPSK(c.Name)
}

func derived(c radio.Channel) bool {
	return c.Secret == meshcore.DeriveHashtagPSK(c.Name) || c.Secret == meshcore.DeriveHashtagPSK(strings.ToLower(c.Name))
}

var commonHashtags = func() map[[16]byte]bool {
	out := map[[16]byte]bool{}
	for _, n := range []string{"test", "testing", "public", "general", "chat", "mesh", "meshcore", "local", "emergency",
		"help", "family", "friends", "private", "secret", "home", "team"} {
		out[meshcore.DeriveHashtagPSK(n)] = true
	}
	return out
}()

// ReplaceChannels takes the radio's channel list after a change.
func (s *Service) ReplaceChannels(cs []radio.Channel) error {
	s.channels = cs
	return s.store.SaveChannels(s.device, cs)
}

// ForgetChannel deletes a removed channel's history, so a channel added to the same slot later starts empty.
func (s *Service) ForgetChannel(idx int) error {
	return s.store.DeleteConvo(s.device, store.ChannelConvo(idx))
}

// ChannelFor finds the channel a conversation belongs to.
func (s *Service) ChannelFor(c store.Convo) (radio.Channel, bool) {
	for _, ch := range s.channels {
		if store.ChannelConvo(ch.Idx) == c {
			return ch, true
		}
	}
	return radio.Channel{}, false
}
