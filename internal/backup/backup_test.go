package backup

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

func sample() *Backup {
	pin := uint32(123456)
	scope := "uk"
	return &Backup{App: "meshcore-tui v0.2.0", Made: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC), RadioName: "MeshCore-k4",
		Identity: &Identity{Name: "MeshCore-k4", Lat: 51.45, Lon: -2.59},
		Radio:    &Radio{FreqKHz: 869618, BwHz: 62500, SF: 8, CR: 8, TxDBm: 22, RxDelay: 0, AirtimeFactor: 1},
		Options:  &Options{TelemetryBase: radio.SharePerContact, PathHashMode: 1, AutoAdd: AutoAdd{Chat: true, MaxHops: 3}, FloodScope: &scope},
		Pin:      &pin,
		Channels: []Channel{FromChannel(radio.Channel{Idx: 1, Name: "#bristol", Secret: [16]byte{1, 2, 3}})},
		Contacts: []Contact{
			FromContact(radio.Contact{Key: [32]byte{0xA3}, Name: "Dundry", Type: radio.TypeRepeater, PathLen: -1}),
			FromContact(radio.Contact{Key: [32]byte{0x3A}, Name: "Alice", Type: radio.TypeChat, PathLen: 2, Path: []byte{0xA3, 0x1E, 0x77, 0x08}, HashSize: 2, Lat: 51.4}),
		},
		PrivateKey: strings.Repeat("ab", 64),
	}
}

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := sample().Write(&buf); err != nil {
		t.Fatal(err)
	}
	b, err := Parse(&buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range Parts {
		if !b.Has(p) {
			t.Errorf("lost %v", p)
		}
	}
	alice, err := b.Contacts[1].Radio()
	if err != nil || alice.PathLen != 2 || alice.HashSize != 2 || !bytes.Equal(alice.Path, []byte{0xA3, 0x1E, 0x77, 0x08}) || alice.Type != radio.TypeChat {
		t.Errorf("alice = %+v, %v", alice, err)
	}
	if dundry, _ := b.Contacts[0].Radio(); dundry.PathLen != -1 || dundry.Path != nil {
		t.Errorf("a contact without a route floods: %+v", dundry)
	}
	if c := b.Channels[0].Radio(); c.Idx != 1 || c.Name != "#bristol" || c.Secret != [16]byte{1, 2, 3} {
		t.Errorf("channel = %+v", c)
	}
	if k, err := b.Key(); err != nil || k[0] != 0xAB {
		t.Errorf("key %x, %v", k[:2], err)
	}
}

func TestPartsAreOptional(t *testing.T) {
	var buf bytes.Buffer
	(&Backup{Made: time.Now(), Channels: sample().Channels}).Write(&buf)
	if strings.Contains(buf.String(), "private_key") || strings.Contains(buf.String(), "identity") {
		t.Errorf("parts left out aren't written:\n%s", buf.String())
	}
	b, err := Parse(&buf)
	if err != nil || b.Has(PartKey) || b.Has(PartRadio) || !b.Has(PartChannels) {
		t.Fatalf("%+v, %v", b, err)
	}
}

func TestParseRefuses(t *testing.T) {
	encode := func(change func(b *Backup)) string {
		b := sample()
		change(b)
		var buf bytes.Buffer
		b.Write(&buf)
		return buf.String()
	}
	for name, file := range map[string]string{
		"not JSON":        "hello",
		"another format":  `{"format":"something else","version":1}`,
		"a newer version": strings.Replace(encode(func(*Backup) {}), `"version": 1`, `"version": 2`, 1),
		"too big":         `{"format":"` + Format + `","version":1,"app":"` + strings.Repeat("x", MaxSize) + `"}`,
		"a long name":     encode(func(b *Backup) { b.Identity.Name = strings.Repeat("n", 32) }),
		"off the planet":  encode(func(b *Backup) { b.Identity.Lat = 91 }),
		"a frequency":     encode(func(b *Backup) { b.Radio.FreqKHz = 2_400_000 }),
		"a power":         encode(func(b *Backup) { b.Radio.TxDBm = 40 }),
		"a short secret":  encode(func(b *Backup) { b.Channels[0].Secret = "abcd" }),
		"a short key":     encode(func(b *Backup) { b.PrivateKey = "abcd" }),
		"a kind of node":  encode(func(b *Backup) { b.Contacts[0].Type = "toaster" }),
		"a long route":    encode(func(b *Backup) { r := strings.Repeat("aa", 65); b.Contacts[0].Route = &r }),
		"a ragged route":  encode(func(b *Backup) { r := "aabbcc"; b.Contacts[1].Route = &r }),
		"a PIN":           encode(func(b *Backup) { p := uint32(12); b.Pin = &p }),
		"a telemetry":     encode(func(b *Backup) { b.Options.TelemetryEnv = 7 }),
		"a channel slot":  encode(func(b *Backup) { b.Channels[0].Slot = 99 }),
		"too many":        encode(func(b *Backup) { b.Channels = make([]Channel, maxChannels+1) }),
	} {
		if _, err := Parse(strings.NewReader(file)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseCleansNames(t *testing.T) {
	var buf bytes.Buffer
	b := sample()
	b.Identity.Name = "Mesh\x1b[31mCore​"
	b.Contacts[0].Name = "Dun\ndry"
	b.Write(&buf)
	got, err := Parse(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(got.Identity.Name, "\x1b​") || strings.Contains(got.Contacts[0].Name, "\n") {
		t.Errorf("names from a file are cleaned: %q, %q", got.Identity.Name, got.Contacts[0].Name)
	}
}
