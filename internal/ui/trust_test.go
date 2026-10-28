package ui

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

// onDevice makes the harness's connection a saved device, with the key it last proved.
func (h *harness) onDevice(kind, addr, key string, others ...config.Device) {
	h.m.opt.Config = &config.Config{}
	h.m.target = Target{Kind: kind, Addr: addr}
	h.m.opt.Config.Devices = append([]config.Device{{Name: "MeshCore-k4", Kind: kind, Addr: addr, Key: key}}, others...)
}

// prove answers the radio check with the radio's own signature, or a forged one.
func (h *harness) prove(real bool) {
	data := []byte(checkDomain + "challenge")
	var sig [64]byte
	if real {
		sig, _ = h.s.Sign(context.Background(), data)
	} else {
		copy(sig[:], ed25519.Sign(newSigner("impostor").priv, data))
	}
	h.m.Update(radioCheckedMsg{key: h.m.ready.Self.PubKey, data: data, sig: sig})
	h.settle()
}

func TestTheDemoRadioProvesItself(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.until("checked", func() bool { return h.m.trust != trustChecking })
	if h.m.trust != trustVerified || !strings.Contains(plainLog(h), "RADIO VERIFIED") {
		t.Fatalf("the simulator signs with its own key: %v\n%s", h.m.trust, plainLog(h))
	}
	h.m.Update(eventMsg{radio.Restored{}})
	if h.m.trust != trustChecking {
		t.Error("after the link drops, the radio proves itself again")
	}
}

func TestAnImpostorGetsNoPasswords(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.until("checked", func() bool { return h.m.trust != trustChecking })
	real := radio.KeyHex(h.m.ready.Self.PubKey[:])

	// It claims the saved radio's key but can't sign with it.
	h.onDevice("USB", "/dev/cu.test", real)
	h.prove(false)
	if h.m.trust != trustChanged || !strings.Contains(plainLog(h), "DIFFERENT RADIO") {
		t.Fatalf("a radio that can't sign for its key is flagged: %v", h.m.trust)
	}
	if safe, _ := h.m.savedPasswordsOK(); safe {
		t.Error("no saved passwords to an impostor")
	}

	// The watch doesn't log in, and says why.
	dundry := h.contact("Dundry")
	h.m.opt.Keychain.Set(dundry.Key, "password")
	h.m.opt.Config.Admin.Watch = []string{radio.KeyHex(dundry.Key[:])}
	h.m.pollWatched()
	if why := h.m.adm.noPassword[dundry.Key]; !strings.Contains(why, "isn't the radio they were saved with") {
		t.Errorf("watch note: %q", why)
	}
	if sess, ok := h.m.admin.Find(dundry.Key); ok && sess.State != 0 {
		t.Errorf("no login was started: %v", sess.State)
	}

	// Pressing enter on the node opens the login instead of sending the saved password, with a warning.
	h.keys("5")
	h.choose("Dundry")
	h.key(tea.KeyEnter)
	if h.m.adm.prompt != promptPassword || !strings.Contains(h.screen(), "didn't prove it's the one saved") {
		t.Fatalf("the login is typed, with a warning:\n%s", h.screen())
	}
}

func TestACheckThatFailsIsntADifferentRadio(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.until("checked", func() bool { return h.m.trust != trustChecking })
	h.onDevice("BLE", "AA:BB", radio.KeyHex(h.m.ready.Self.PubKey[:]))
	h.m.Update(radioCheckedMsg{key: h.m.ready.Self.PubKey, err: errors.New("a 61-byte frame is more than this Bluetooth link carries")})
	h.settle()
	if h.m.trust != trustUnproven || strings.Contains(plainLog(h), "DIFFERENT RADIO") || !strings.Contains(h.m.trustText(), "didn't go through") {
		t.Errorf("a check that couldn't run leaves it unverified, saying why: %v %q", h.m.trust, h.m.trustText())
	}
	if safe, _ := h.m.savedPasswordsOK(); safe {
		t.Error("an unverified radio still gets no saved passwords unasked")
	}
}

func TestADifferentKeyIsFlagged(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.until("checked", func() bool { return h.m.trust != trustChecking })
	h.onDevice("USB", "/dev/cu.test", strings.Repeat("ab", 32))
	h.prove(true)
	if h.m.trust != trustChanged {
		t.Fatalf("a radio proving a different key from the saved one: %v", h.m.trust)
	}

	// :radio trust accepts it once it proves its key.
	h.keys(":")
	h.typeLine("radio trust")
	h.until("rechecked", func() bool { return h.m.trust != trustChecking })
	if h.m.trust != trustVerified || h.m.opt.Config.Devices[0].Key != radio.KeyHex(h.m.ready.Self.PubKey[:]) {
		t.Errorf("trusted and saved: %v %s", h.m.trust, h.m.opt.Config.Devices[0].Key)
	}
}

func TestFirstConnectionSavesTheKey(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.until("checked", func() bool { return h.m.trust != trustChecking })
	h.onDevice("USB", "/dev/cu.test", "")
	h.prove(true)
	if h.m.trust != trustVerified || h.m.opt.Config.Devices[0].Key != radio.KeyHex(h.m.ready.Self.PubKey[:]) {
		t.Errorf("the key it proved is saved: %v %q", h.m.trust, h.m.opt.Config.Devices[0].Key)
	}
}

func TestLinksThatMayNotBeEncrypted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.until("checked", func() bool { return h.m.trust != trustChecking })
	key := radio.KeyHex(h.m.ready.Self.PubKey[:])

	h.onDevice("TCP", "10.0.0.4:5000", key)
	h.prove(true)
	if safe, why := h.m.savedPasswordsOK(); safe || !strings.Contains(why, "WiFi") {
		t.Errorf("over WiFi saved passwords aren't sent unasked: %v %q", safe, why)
	}
	dundry := h.contact("Dundry")
	h.m.opt.Keychain.Set(dundry.Key, "password")
	h.keys("5")
	h.choose("Dundry")
	h.key(tea.KeyEnter)
	if h.m.asking == nil || !strings.Contains(h.screen(), "WIFI LINK") {
		t.Errorf("enter asks before sending it over WiFi:\n%s", h.screen())
	}
	h.key(tea.KeyEscape)

	// A PIN setting of 0 means the radio makes one up, not that it pairs without one: companion
	// firmware won't talk over Bluetooth until it's paired, so the link is encrypted.
	h.onDevice("BLE", "AA:BB", key)
	h.prove(true)
	if safe, why := h.m.savedPasswordsOK(); !safe {
		t.Errorf("a verified radio over Bluetooth gets its saved passwords: %q", why)
	}
}

func TestTheSameKeyAtAnotherBluetoothAddress(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.until("checked", func() bool { return h.m.trust != trustChecking })
	key := radio.KeyHex(h.m.ready.Self.PubKey[:])
	h.onDevice("BLE", "CC:DD", "", config.Device{Name: "MeshCore-k4", Kind: "BLE", Addr: "AA:BB", Key: key})
	h.prove(true)
	if h.m.trust != trustChanged || !strings.Contains(plainLog(h), "passing your radio's answers on") {
		t.Errorf("a second address answering for a saved radio is something in between: %v\n%s", h.m.trust, plainLog(h))
	}
	if h.m.opt.Config.Devices[0].Key != "" {
		t.Error("its key isn't saved for the new address")
	}
}

func TestAutoConnectNeedsAUniqueName(t *testing.T) {
	t.Parallel()
	c := &config.Config{Devices: []config.Device{{Name: "Shed", Kind: "BLE", Addr: "AA"}, {Name: "Shed", Kind: "BLE", Addr: "BB"}, {Name: "Desk", Kind: "USB", Addr: "x"}}}
	if _, ok := c.Device("Shed"); ok {
		t.Error("two devices with one name: neither is chosen")
	}
	if d, ok := c.Device("Desk"); !ok || d.Addr != "x" {
		t.Error("a unique name is found")
	}
}
