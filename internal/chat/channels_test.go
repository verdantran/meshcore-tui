package chat

import (
	"strings"
	"testing"

	meshcore "github.com/OwlShack/meshcore-go"

	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/store"
)

func TestPlanChannel(t *testing.T) {
	s := newService(t, 3, true)
	p, err := s.PlanChannel("#Hilltop", "", 4)
	if err != nil || p.Kind != "hashtag" || p.Channel.Name != "#Hilltop" || p.Channel.Idx != 2 ||
		p.Channel.Secret != meshcore.DeriveHashtagPSK("#Hilltop") {
		t.Fatalf("hashtag = %+v %v", p, err)
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
	link := ChannelLink(radio.Channel{Name: "Club & Co", Secret: [16]byte{1, 2}})
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
	fake := radio.Channel{Name: "#bristol", Secret: [16]byte{1}} // named like a hashtag, but with a private secret
	for _, c := range []struct {
		ch   radio.Channel
		open bool
	}{{pub, true}, {tag, true}, {fake, false}, {radio.Channel{Name: "Family", Secret: [16]byte{2}}, false}} {
		if IsOpen(c.ch) != c.open {
			t.Errorf("%s open = %v", c.ch.Name, !c.open)
		}
	}
}
