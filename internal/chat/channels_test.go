package chat

import (
	"encoding/hex"
	"net/url"
	"strings"
	"testing"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
)

func TestPlanChannel(t *testing.T) {
	s := newService(t, 3, true)
	p, err := s.PlanChannel("#Hilltop", "", 4)
	if err != nil || p.Kind != "hashtag" || p.Channel.Name != "#Hilltop" || p.Channel.Idx != 2 ||
		p.Channel.Secret != meshcore.DeriveHashtagPSK("#Hilltop") {
		t.Fatalf("hashtag = %+v %v", p, err)
	}
	// An emoji sequence keeps its joiner, so the secret is the one other apps derive from the same name.
	if p, err := s.PlanChannel("#👩\u200d🚀crew", "", 4); err != nil || p.Channel.Name != "#👩\u200d🚀crew" ||
		p.Channel.Secret != meshcore.DeriveHashtagPSK("#👩\u200d🚀crew") {
		t.Errorf("joined emoji = %+v %v", p, err)
	}
	if p, _ := s.PlanChannel("Family", "", 4); p.Kind != "new" || p.Channel.Secret == ([16]byte{}) {
		t.Errorf("new = %+v", p)
	}
	shared := "000102030405060708090a0b0c0d0e0f"
	if p, err := s.PlanChannel("Club", shared, 4); err != nil || p.Kind != "private" || p.Channel.Secret[15] != 0x0f {
		t.Errorf("hex secret = %+v %v", p, err)
	}
	if p, err := s.PlanChannel("Club", "AAECAwQFBgcICQoLDA0ODw==", 4); err != nil || p.Channel.Secret[15] != 0x0f {
		t.Errorf("base64 secret = %+v %v", p, err)
	}
	link := ChannelLink(radio.Channel{Name: "Club & Co", Secret: strong})
	if !strings.HasPrefix(link, ChannelLinkPrefix) || !strings.Contains(link, "Club+%26+Co") {
		t.Errorf("link = %s", link)
	}
	if p, err := s.PlanChannel(link, "", 4); err != nil || p.Channel.Name != "Club & Co" || p.Kind != "private" {
		t.Errorf("from link = %+v %v", p, err)
	}
	hashLink := ChannelLink(radio.Channel{Name: "#bath", Secret: meshcore.DeriveHashtagPSK("#bath")})
	if p, _ := s.PlanChannel(hashLink, "", 4); p.Kind != "hashtag" {
		t.Errorf("hashtag link = %+v", p)
	}

	for _, c := range []struct{ name, secret, msg string }{
		{"", "", "needs a name"},
		{"#two words", "", "one word"},
		{"#", "", "one word"},
		{"Club", "zz", "32 hex characters"},
		{strings.Repeat("x", 32), "", "at most 31 bytes"},
		{ChannelLinkPrefix + "name=x", "", "needs a name and a secret"},
		{ChannelLinkPrefix + "name=x&secret=00", "", "32 hex"},
		{"Copy", "", ""},
		{"public", "000102030405060708090a0b0c0d0e0f", "already have a channel called Public"},
		{"#BRISTOL", "", "already have a channel called #bristol"},
		{ChannelLink(radio.Channel{Name: "#bath", Secret: [16]byte{9}}), "", "secret isn't #bath's"},
	} {
		_, err := s.PlanChannel(c.name, c.secret, 4)
		if c.msg == "" {
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%q %q: %v", c.name, c.secret, err)
		}
	}
	// #bristol is already slot 1 in the test radio, with this secret.
	s.channels[1].Secret = meshcore.DeriveHashtagPSK("#bristol")
	if _, err := s.PlanChannel("#bristol", "", 4); err == nil || !strings.Contains(err.Error(), "already in that channel, as #bristol") {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := s.PlanChannel("#extra", "", 2); err != ErrNoSlot {
		t.Errorf("full: %v", err)
	}
}

func TestChannelChanges(t *testing.T) {
	s := newService(t, 3, true)
	s.Incoming(radio.Incoming{At: t0, Channel: true, ChannelIdx: 1, Text: "Carol: hello"})
	if err := s.ForgetChannel(1); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := s.store.Messages(s.device, store.ChannelConvo(1), 10); len(msgs) != 0 {
		t.Error("history deleted")
	}
	s.ReplaceChannels([]radio.Channel{{Idx: 0, Name: "Public"}, {Idx: 3, Name: "Family"}})
	if ch, ok := s.ChannelFor(store.ChannelConvo(3)); !ok || ch.Name != "Family" {
		t.Error("channel for a convo")
	}
	if _, ok := s.ChannelFor(store.ChannelConvo(1)); ok {
		t.Error("removed")
	}
	if saved, _ := s.store.Channels(s.device); len(saved) != 2 {
		t.Error("saved")
	}
	var pub radio.Channel
	copy(pub.Secret[:], meshcore.PublicChannel().PSK)
	if !IsPublic(pub) || IsPublic(radio.Channel{}) {
		t.Error("public")
	}
}

func TestIsOpen(t *testing.T) {
	var pub radio.Channel
	copy(pub.Secret[:], meshcore.PublicChannel().PSK)
	pub.Name = "Public"
	tag := radio.Channel{Name: "#bristol", Secret: meshcore.DeriveHashtagPSK("#bristol")}
	fake := radio.Channel{Name: "#bristol", Secret: strong} // named like a hashtag, but with a private secret
	for _, c := range []struct {
		ch   radio.Channel
		open bool
	}{{pub, true}, {tag, true}, {fake, false}, {radio.Channel{Name: "Family", Secret: strong}, false}} {
		if IsOpen(c.ch) != c.open {
			t.Errorf("%s open = %v", c.ch.Name, !c.open)
		}
	}
}

// strong is a secret with nothing guessable about it.
var strong = [16]byte{0x3a, 0x91, 0x0c, 0xe7, 0x55, 0x12, 0xb8, 0x6f, 0xd4, 0x29, 0x80, 0x4e, 0xf3, 0x17, 0xaa, 0x6b}

func TestGuessableSecretsAreOpen(t *testing.T) {
	s := newService(t, 3, true)
	pub := hex.EncodeToString(meshcore.PublicChannel().PSK)
	tagged := meshcore.DeriveHashtagPSK("#test")
	for _, c := range []struct{ name, secret, msg string }{
		{ChannelLink(radio.Channel{Name: "Family", Secret: meshcore.DeriveHashtagPSK("#Family")}), "", "#Family's"},
		{"Family", hex.EncodeToString(tagged[:]), "too easy to guess"},
		{"Test", hex.EncodeToString(tagged[:]), "#Test's"},
		{"Family", pub, "Public channel's"},
		{"Family", "00000000000000000000000000000000", "too easy to guess"},
		{"Family", "01010101010101010101010101010102", "too easy to guess"},
	} {
		if _, err := s.PlanChannel(c.name, c.secret, 4); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%q %q: %v", c.name, c.secret, err)
		}
	}
	// One made elsewhere is judged open wherever it's shown.
	for _, ch := range []radio.Channel{
		{Name: "Family", Secret: meshcore.DeriveHashtagPSK("#family")},
		{Name: "Family", Secret: tagged},
		{Name: "#bristol", Secret: tagged},
		{Name: "Family", Secret: [16]byte{7, 7}},
	} {
		if !IsOpen(ch) || IsHashtag(ch) {
			t.Errorf("%s %x: open %v, hashtag %v", ch.Name, ch.Secret[:4], IsOpen(ch), IsHashtag(ch))
		}
	}
	if !IsHashtag(radio.Channel{Name: "#bath", Secret: meshcore.DeriveHashtagPSK("#bath")}) || IsHashtag(radio.Channel{Name: "#bath", Secret: strong}) {
		t.Error("hashtag")
	}
}

func TestChannelNamesAreCleaned(t *testing.T) {
	s := newService(t, 3, true)
	secret := hex.EncodeToString(strong[:])
	for _, name := range []string{"Public​", "Public ", "Ｐublic", "Pub‮lic", "Рublic"} {
		link := ChannelLinkPrefix + "name=" + url.QueryEscape(name) + "&secret=" + secret
		if _, err := s.PlanChannel(link, "", 4); err == nil || !strings.Contains(err.Error(), "already have a channel called Public") {
			t.Errorf("link %q: %v", name, err)
		}
		if _, err := s.PlanChannel(name, secret, 4); err == nil || !strings.Contains(err.Error(), "already have a channel called Public") {
			t.Errorf("typed %q: %v", name, err)
		}
	}
	link := ChannelLinkPrefix + "name=" + url.QueryEscape("Club\x1b[2J\nJOINED  x") + "&secret=" + secret
	if p, err := s.PlanChannel(link, "", 4); err != nil || p.Channel.Name != "Club[2J JOINED x" {
		t.Errorf("cleaned = %q %v", p.Channel.Name, err)
	}
	if _, err := s.PlanChannel(ChannelLinkPrefix+"name=%E2%80%8B&secret="+secret, "", 4); err == nil {
		t.Error("a blank name is refused")
	}
}
