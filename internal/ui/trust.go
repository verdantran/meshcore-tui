package ui

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

// The radio is handed every repeater password the app logs in with, and anything can claim another radio's
// name or key, so saved passwords only go to a radio that has just proved, by signing, that it holds the key
// saved for this connection.

type radioTrust int

const (
	trustChecking radioTrust = iota
	trustVerified
	trustUnproven // it can't sign, so it can't prove which radio it is
	trustChanged  // it isn't the radio saved for this connection
)

// checkDomain starts every challenge, so a signed one can never pass for a signed channel message.
const checkDomain = "meshcore-tui radio check v1 "

type radioCheckedMsg struct {
	key  [32]byte
	data []byte
	sig  [64]byte
	err  error
}

// checkRadio asks the radio to sign a fresh challenge with its own key.
func (m *Model) checkRadio() tea.Cmd {
	m.trust = trustChecking
	if m.ready.Console {
		m.trust = trustVerified // a repeater on its own console logs in locally; no passwords go to it
		return nil
	}
	signer, ok := m.radio.(radio.Signer)
	if !ok || m.canSign() != nil {
		m.radioUnproven()
		return nil
	}
	nonce := make([]byte, 32)
	rand.Read(nonce)
	data := append([]byte(checkDomain), nonce...)
	key := m.ready.Self.PubKey
	return m.radioCall(func(ctx context.Context) tea.Msg {
		sig, err := signer.Sign(ctx, data)
		return radioCheckedMsg{key: key, data: data, sig: sig, err: err}
	})
}

func (m *Model) radioChecked(msg radioCheckedMsg) {
	if msg.key != m.ready.Self.PubKey {
		return // an answer for a radio we've since moved on from
	}
	proved := msg.err == nil && ed25519.Verify(msg.key[:], msg.data, msg.sig[:])
	key := radio.KeyHex(msg.key[:])
	dev, saved := m.savedDevice()
	other, relayed := m.keySavedElsewhere(key)
	switch {
	case proved && relayed:
		m.radioChanged("its key belongs to " + other + ", saved at another Bluetooth address; something may be passing your radio's answers on")
	case proved && (!saved || dev.Key == "" || m.renewKey):
		m.trust, m.renewKey = trustVerified, false
		if saved && dev.Key != key {
			m.pinRadio(key)
		}
		m.addLog(m.now, "RADIO VERIFIED  it proved it holds key "+key[:8], radio.ToneOK)
	case proved && dev.Key == key:
		m.trust = trustVerified
		m.addLog(m.now, "RADIO VERIFIED  "+dev.Name+", key "+key[:8], radio.ToneOK)
	case proved:
		m.radioChanged(fmt.Sprintf("its key is %s, not the %s saved for %s", key[:8], dev.Key[:min(8, len(dev.Key))], dev.Name))
	case saved && dev.Key != "":
		m.radioChanged("it couldn't prove it's " + dev.Name)
	default:
		m.radioUnproven()
	}
}

// wifiNote says once that a WiFi link is plain TCP, which carries every channel secret on each connect.
func (m *Model) wifiNote() {
	if m.wifiNoted || m.radio == nil || m.radio.Transport() != "TCP" {
		return
	}
	m.wifiNoted = true
	m.addLog(m.now, "UNENCRYPTED  WiFi links aren't encrypted: anyone on this network can read and change what passes, channel secrets and passwords included", radio.ToneWarn)
}

func (m *Model) radioUnproven() {
	m.trust = trustUnproven
	m.addLog(m.now, "RADIO NOT VERIFIED  it can't sign, so saved passwords are asked for", radio.ToneWarn)
}

// keySavedElsewhere finds a Bluetooth device saved at another address that proved this key. A radio has one
// address, so the same key answering from a second one is something in between, passing its answers on.
func (m *Model) keySavedElsewhere(key string) (string, bool) {
	if m.opt.Config == nil || m.target.Kind != "BLE" {
		return "", false
	}
	for _, d := range m.opt.Config.Devices {
		if d.Kind == "BLE" && d.Key == key && d.Addr != m.target.Addr {
			return d.Name, true
		}
	}
	return "", false
}

func (m *Model) radioChanged(why string) {
	m.trust = trustChanged
	m.fail("DIFFERENT RADIO  " + why + ". Saved passwords won't be sent to it; :radio trust accepts it if it's yours")
	m.pushToast("⚠ Different radio", why)
}

// savedDevice is the saved device this connection was made to.
func (m *Model) savedDevice() (config.Device, bool) {
	if m.opt.Config == nil {
		return config.Device{}, false
	}
	for _, d := range m.opt.Config.Devices {
		if d.Kind == m.target.Kind && d.Addr == m.target.Addr {
			return d, true
		}
	}
	return config.Device{}, false
}

func (m *Model) pinRadio(key string) {
	for i, d := range m.opt.Config.Devices {
		if d.Kind == m.target.Kind && d.Addr == m.target.Addr {
			m.opt.Config.Devices[i].Key = key
			m.saveConfig()
		}
	}
}

// savedPasswordsOK reports whether saved passwords may go to the radio without asking, and why not.
func (m *Model) savedPasswordsOK() (bool, string) {
	switch {
	case m.trust == trustChanged:
		return false, "this isn't the radio they were saved with"
	case m.trust != trustVerified:
		return false, "this radio hasn't proved which radio it is"
	case m.target.Kind == "TCP":
		return false, "the WiFi link to the radio isn't encrypted"
	case m.target.Kind == "BLE" && !m.ready.Device.PinSet:
		return false, "the radio has no Bluetooth PIN, so the link may not be encrypted"
	}
	return true, ""
}

func (m *Model) trustText() string {
	switch m.trust {
	case trustVerified:
		return "verified · key " + radio.KeyHex(m.ready.Self.PubKey[:4])
	case trustUnproven:
		return "not verified: it can't sign"
	case trustChanged:
		return "⚠ not the radio saved for this connection"
	}
	return "checking…"
}

func (m *Model) identityRow() devRow {
	hint := "Saved repeater passwords only go to a radio that proves, by signing, that it's the one saved for this connection."
	if safe, why := m.savedPasswordsOK(); !safe {
		hint += " Right now they're asked for, not sent: " + why + "."
	}
	return devRow{id: "identity", group: "Security", label: "This radio", value: m.trustText(), kind: rowAction, hint: hint,
		press: func(m *Model) tea.Cmd {
			m.addLog(m.now, "RADIO  "+m.trustText()+" · key "+radio.KeyHex(m.ready.Self.PubKey[:]), radio.ToneInfo)
			return nil
		}}
}

func radioCommand() command {
	return command{name: "radio", args: "trust", help: "accept a radio that proved its key but isn't the one saved, after a factory reset or a new key",
		run: func(m *Model, a []string) error {
			if len(a) != 1 || a[0] != "trust" {
				return fmt.Errorf("radio trust is the only one")
			}
			if m.trust != trustChanged {
				return fmt.Errorf("this radio isn't flagged as different")
			}
			m.renewKey = true
			m.queued = m.checkRadio()
			return nil
		}}
}
