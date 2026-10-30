package ui

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/store"
)

// Preset is a regional radio setting. Check new ones against the official MeshCore app before adding them.
type Preset struct {
	Name   string
	Params radio.RadioParams
}

var presets = []Preset{
	{"EU/UK narrow (the current default)", radio.RadioParams{FreqKHz: 869618, BwHz: 62500, SF: 8, CR: 8}},
	{"EU/UK long range (the older default)", radio.RadioParams{FreqKHz: 869525, BwHz: 250000, SF: 11, CR: 5}},
	{"EU/UK medium range", radio.RadioParams{FreqKHz: 869525, BwHz: 250000, SF: 10, CR: 5}},
	{"USA/Canada narrow", radio.RadioParams{FreqKHz: 910525, BwHz: 62500, SF: 7, CR: 5}},
	{"Australia/NZ", radio.RadioParams{FreqKHz: 915800, BwHz: 250000, SF: 10, CR: 5}},
}

var loraBandwidths = []float64{7.8, 10.4, 15.6, 20.8, 31.25, 41.7, 62.5, 125, 250, 500}

var shareNames = []string{"nobody", "contacts given permission", "everyone"}

type devPrompt int

const (
	devNone devPrompt = iota
	devEdit
	devPresets
	devConfirm
	devShowKey
	devBackup
	devRestorePath
	devConsole
)

type rowKind int

const (
	rowEdit rowKind = iota
	rowToggle
	rowAction
)

type devOp func(ctx context.Context, d radio.Device) error

// devRow is one line of the Device screen. Edits are checked by apply before anything is sent.
type devRow struct {
	id, group, label, value, hint string
	kind                          rowKind
	initial                       string                         // what an edit starts from
	apply                         func(in string) (devOp, error) // rowEdit
	press                         func(m *Model) tea.Cmd         // rowToggle and rowAction
}

type devConfirmation struct {
	title, warning, word string // word is what must be typed; empty means y
	id, what             string
	op                   devOp
	after                func(m *Model) tea.Cmd
	back                 devPrompt // where cancelling returns to
}

type deviceModel struct {
	s        radio.Settings
	vars     []radio.Var
	storage  radio.Storage
	clock    time.Time // the radio's clock, as read at clockAt
	clockAt  time.Time
	loaded   bool
	loading  bool
	err      error
	sel      int
	prompt   devPrompt
	input    lineEdit
	pick     int
	confirm  devConfirmation
	key      string
	bak      backupState
	con      radioConsole
	saved    string // the file the last backup went to
	busy     map[string]time.Time
	doneAt   map[string]time.Time
	failedAt map[string]time.Time
}

func newDevice() deviceModel {
	return deviceModel{busy: map[string]time.Time{}, doneAt: map[string]time.Time{}, failedAt: map[string]time.Time{}}
}

type devLoadedMsg struct {
	s       radio.Settings
	vars    []radio.Var
	storage radio.Storage
	clock   time.Time
	err     error
}

type devOpMsg struct {
	id, what string
	err      error
	reload   bool
}

type keyExportedMsg struct {
	key [64]byte
	err error
}

// clipClearMsg empties the clipboard after a secret was copied, unless something else was copied since.
type clipClearMsg struct{ gen int }

const secretClipFor = 30 * time.Second

func (m *Model) dev() radio.Device {
	if m.radio == nil {
		return nil
	}
	return m.radio.Device()
}

func (m *Model) loadDevice() tea.Cmd {
	d := m.dev()
	if d == nil {
		return nil
	}
	m.device.loading = true
	return m.radioCall(func(ctx context.Context) tea.Msg {
		s, err := d.Settings(ctx)
		if err != nil {
			return devLoadedMsg{err: err}
		}
		vars, _ := d.CustomVars(ctx)
		storage, _ := d.Storage(ctx)
		clock, _ := d.Clock(ctx)
		return devLoadedMsg{s: s, vars: vars, storage: storage, clock: clock}
	})
}

// runOp sends one change; the settings are read again afterwards so the screen shows what the radio holds.
func (m *Model) runOp(id, what string, op devOp, reload bool) tea.Cmd {
	d := m.dev()
	if d == nil {
		return nil
	}
	m.device.busy[id] = m.now
	return m.radioCall(func(ctx context.Context) tea.Msg { return devOpMsg{id, what, op(ctx, d), reload} })
}

func (m *Model) deviceMsg(msg tea.Msg) tea.Cmd {
	dm := &m.device
	switch msg := msg.(type) {
	case devLoadedMsg:
		dm.loading = false
		if msg.err != nil {
			dm.err = msg.err
			m.fail("DEVICE  couldn't read the settings: " + radio.Explain(msg.err))
			return nil
		}
		dm.s, dm.vars, dm.storage, dm.loaded, dm.err = msg.s, msg.vars, msg.storage, true, nil
		dm.clock, dm.clockAt = msg.clock, m.now
	case devOpMsg:
		delete(dm.busy, msg.id)
		if msg.err != nil {
			dm.failedAt[msg.id] = m.now
			m.fail(msg.what + "  " + radio.Explain(msg.err))
			return nil
		}
		dm.doneAt[msg.id] = m.now
		m.addLog(m.now, msg.what, radio.ToneOK)
		if msg.id == "reset" || msg.id == "import" {
			m.expectNewKey()
		}
		if msg.reload {
			return m.loadDevice()
		}
	case keyExportedMsg:
		delete(dm.busy, "export")
		if msg.err != nil {
			dm.prompt = devNone
			m.fail("EXPORT KEY  " + radio.Explain(msg.err))
			return nil
		}
		dm.key = hex.EncodeToString(msg.key[:])
	case clipClearMsg:
		if msg.gen == m.clipGen {
			return m.copyText("")
		}
	}
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func mhz(khz uint32) string  { return strconv.FormatFloat(float64(khz)/1000, 'f', -1, 64) }
func khzBW(hz uint32) string { return strconv.FormatFloat(float64(hz)/1000, 'f', -1, 64) }

// radioRow edits one part of the radio settings and sends them all.
func (m *Model) radioRow(id, label, value, hint string, set func(p *radio.RadioParams, in string) error) devRow {
	cur := m.device.s.Radio
	return devRow{id: id, group: "Radio", label: label, value: value, hint: hint, kind: rowEdit, initial: strings.Fields(value)[0],
		apply: func(in string) (devOp, error) {
			p := cur
			if err := set(&p, strings.TrimSpace(in)); err != nil {
				return nil, err
			}
			if p.Repeat && !inRanges(m.device.s.RepeatFreqs, p.FreqKHz) {
				p.Repeat = false // the firmware refuses repeat off its allowed frequencies
			}
			return func(ctx context.Context, d radio.Device) error { return d.SetRadio(ctx, p) }, nil
		}}
}

func inRanges(rs []radio.FreqRange, khz uint32) bool {
	return slices.ContainsFunc(rs, func(r radio.FreqRange) bool { return khz >= r.LowKHz && khz <= r.HighKHz })
}

func (m *Model) otherOp(change func(o *radio.Other)) devOp {
	o := m.device.s.Other
	change(&o)
	return func(ctx context.Context, d radio.Device) error { return d.SetOther(ctx, o) }
}

func (m *Model) toggleRow(id, group, label string, on bool, hint, what string, op devOp) devRow {
	return devRow{id: id, group: group, label: label, value: onOff(on), hint: hint, kind: rowToggle,
		press: func(m *Model) tea.Cmd { return m.runOp(id, what, op, true) }}
}

func (m *Model) cycleRow(id, group, label string, cur int, names []string, hint, what string, set func(i int) devOp) devRow {
	next := (cur + 1) % len(names)
	name := fmt.Sprint(cur)
	if cur >= 0 && cur < len(names) {
		name = names[cur]
	}
	return devRow{id: id, group: group, label: label, value: name, hint: hint, kind: rowToggle,
		press: func(m *Model) tea.Cmd { return m.runOp(id, what+" "+names[next], set(next), true) }}
}

func parseFloat(in, what string, lo, hi float64) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(in), 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", what)
	}
	if !(f >= lo && f <= hi) {
		return 0, fmt.Errorf("%s must be between %s and %s", what, strconv.FormatFloat(lo, 'f', -1, 64), strconv.FormatFloat(hi, 'f', -1, 64))
	}
	return f, nil
}

func parseInt(in, what string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(in))
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number", what)
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("%s must be between %d and %d", what, lo, hi)
	}
	return n, nil
}

// deviceRows lays out the screen from the settings last read.
func (m *Model) deviceRows() []devRow {
	dm := &m.device
	s := dm.s
	o := s.Other
	loc := fmt.Sprintf("%.5f, %.5f", s.Self.Lat, s.Self.Lon)
	rows := []devRow{
		{id: "battery", group: "Battery", label: "Battery type", value: m.chemistry(m.ready.Self.PubKey).String(), kind: rowToggle,
			hint:  "Sets the curve its charge is read from. Kept here, not on the radio.",
			press: func(m *Model) tea.Cmd { m.cycleBattery(m.ready.Self.PubKey, m.ready.Self.Name); return nil }},
		{id: "name", group: "Identity", label: "Name", value: s.Self.Name, kind: rowEdit, initial: s.Self.Name,
			hint: "What other nodes see in your adverts.",
			apply: func(in string) (devOp, error) {
				in = strings.TrimSpace(in)
				if in == "" || len(in) > 31 {
					return nil, errors.New("the name is 1 to 31 bytes")
				}
				return func(ctx context.Context, d radio.Device) error { return d.SetName(ctx, in) }, nil
			}},
		{id: "location", group: "Identity", label: "Location", value: loc, kind: rowEdit, initial: loc,
			hint: "Latitude, longitude. 0, 0 means none.",
			apply: func(in string) (devOp, error) {
				a, b, ok := strings.Cut(in, ",")
				if !ok {
					return nil, errors.New("location is latitude, longitude, e.g. 51.4545, -2.5879")
				}
				lat, err := parseFloat(a, "latitude", -90, 90)
				if err != nil {
					return nil, err
				}
				lon, err := parseFloat(b, "longitude", -180, 180)
				if err != nil {
					return nil, err
				}
				return func(ctx context.Context, d radio.Device) error { return d.SetLocation(ctx, lat, lon) }, nil
			}},
		m.toggleRow("locpolicy", "Identity", "Share location in adverts", o.AdvertLocPolicy == radio.LocShare, "", "LOCATION SHARING",
			m.otherOp(func(o *radio.Other) { o.AdvertLocPolicy = 1 - min(o.AdvertLocPolicy, 1) })),
		{id: "advert0", group: "Identity", label: "Send an advert to neighbours", value: "zero hop", kind: rowAction,
			hint: "Only nodes in direct range hear it.",
			press: func(m *Model) tea.Cmd {
				return m.runOp("advert0", "ADVERT SENT  zero hop", func(ctx context.Context, d radio.Device) error { return d.SendAdvert(ctx, false) }, false)
			}},
		{id: "advert1", group: "Identity", label: "Send an advert to the mesh", value: "flood", kind: rowAction,
			hint: "Repeaters pass it on, so the whole mesh hears it. Uses more airtime.",
			press: func(m *Model) tea.Cmd {
				return m.runOp("advert1", "ADVERT SENT  flood", func(ctx context.Context, d radio.Device) error { return d.SendAdvert(ctx, true) }, false)
			}},

		{id: "preset", group: "Radio", label: "Preset", value: presetName(s.Radio), kind: rowAction,
			hint:  "Every node you talk to must use the same radio settings.",
			press: func(m *Model) tea.Cmd { m.device.prompt, m.device.pick = devPresets, 0; return nil }},
		m.radioRow("freq", "Frequency", mhz(s.Radio.FreqKHz)+" MHz", "", func(p *radio.RadioParams, in string) error {
			f, err := parseFloat(in, "frequency", 137, 1020)
			p.FreqKHz = uint32(f*1000 + 0.5)
			return err
		}),
		m.radioRow("bw", "Bandwidth", khzBW(s.Radio.BwHz)+" kHz", "7.8, 10.4, 15.6, 20.8, 31.25, 41.7, 62.5, 125, 250 or 500", func(p *radio.RadioParams, in string) error {
			f, err := strconv.ParseFloat(in, 64)
			if err != nil || !slices.Contains(loraBandwidths, f) {
				return fmt.Errorf("bandwidth %s kHz isn't a LoRa bandwidth", in)
			}
			p.BwHz = uint32(f*1000 + 0.5)
			return nil
		}),
		m.radioRow("sf", "Spreading factor", fmt.Sprint(s.Radio.SF), "Higher reaches further but takes longer on air.", func(p *radio.RadioParams, in string) error {
			n, err := parseInt(in, "spreading factor", 5, 12)
			p.SF = byte(n)
			return err
		}),
		m.radioRow("cr", "Coding rate", fmt.Sprint(s.Radio.CR), "", func(p *radio.RadioParams, in string) error {
			n, err := parseInt(in, "coding rate", 5, 8)
			p.CR = byte(n)
			return err
		}),
		{id: "repeat", group: "Radio", label: "Repeat packets", value: onOff(s.Radio.Repeat), kind: rowToggle, hint: repeatHint(s),
			press: func(m *Model) tea.Cmd {
				p := m.device.s.Radio
				p.Repeat = !p.Repeat
				if p.Repeat && !inRanges(m.device.s.RepeatFreqs, p.FreqKHz) {
					m.fail("REPEAT  not allowed on " + mhz(p.FreqKHz) + " MHz; " + repeatHint(m.device.s))
					return nil
				}
				return m.runOp("repeat", "REPEAT "+strings.ToUpper(onOff(p.Repeat)), func(ctx context.Context, d radio.Device) error { return d.SetRadio(ctx, p) }, true)
			}},
		{id: "tx", group: "Radio", label: "Transmit power", value: fmt.Sprintf("%d dBm", s.Self.TxPower), kind: rowEdit, initial: fmt.Sprint(s.Self.TxPower),
			hint: fmt.Sprintf("Up to %d dBm on this board.", s.MaxTxPower),
			apply: func(in string) (devOp, error) {
				n, err := parseInt(in, "transmit power", 1, max(s.MaxTxPower, 1))
				return func(ctx context.Context, d radio.Device) error { return d.SetTxPower(ctx, n) }, err
			}},

		{id: "rxdelay", group: "Tuning", label: "Weak-signal receive delay", value: strconv.FormatFloat(s.Tuning.RxDelay, 'f', -1, 64), kind: rowEdit,
			initial: strconv.FormatFloat(s.Tuning.RxDelay, 'f', -1, 64), hint: "Waits a little before passing on weaker copies of a flood. 0 turns it off.",
			apply: func(in string) (devOp, error) {
				f, err := parseFloat(in, "receive delay", 0, 20)
				t := s.Tuning
				t.RxDelay = f
				return func(ctx context.Context, d radio.Device) error { return d.SetTuning(ctx, t) }, err
			}},
		{id: "af", group: "Tuning", label: "Airtime factor", value: strconv.FormatFloat(s.Tuning.AirtimeFactor, 'f', -1, 64), kind: rowEdit,
			initial: strconv.FormatFloat(s.Tuning.AirtimeFactor, 'f', -1, 64), hint: "How long to stay quiet after sending, as a multiple of the airtime used.",
			apply: func(in string) (devOp, error) {
				f, err := parseFloat(in, "airtime factor", 0, 9)
				t := s.Tuning
				t.AirtimeFactor = f
				return func(ctx context.Context, d radio.Device) error { return d.SetTuning(ctx, t) }, err
			}},

		m.toggleRow("manual", "Messaging", "Add contacts by hand only", o.ManualAdd, "Off lets the auto-add settings on the Contacts screen decide.", "MANUAL ADD",
			m.otherOp(func(o *radio.Other) { o.ManualAdd = !o.ManualAdd })),
		m.cycleRow("multiacks", "Messaging", "Extra acks", min(o.MultiAcks, 1), []string{"off", "on"}, "Sends each ack twice, so fewer messages look undelivered.", "EXTRA ACKS",
			func(i int) devOp { return m.otherOp(func(o *radio.Other) { o.MultiAcks = i }) }),
		m.cycleRow("hashmode", "Messaging", "Path hash size", s.PathHashMode, []string{"1 byte", "2 bytes", "3 bytes"},
			"Bigger hashes tell repeaters apart better. Older firmware only reads 1-byte paths.", "PATH HASH SIZE",
			func(i int) devOp {
				return func(ctx context.Context, d radio.Device) error { return d.SetPathHashMode(ctx, i) }
			}),
		m.cycleRow("telbase", "Messaging", "Share battery and basics with", o.TelemetryBase, shareNames, "", "TELEMETRY BASE",
			func(i int) devOp { return m.otherOp(func(o *radio.Other) { o.TelemetryBase = i }) }),
		m.cycleRow("telloc", "Messaging", "Share location with", o.TelemetryLoc, shareNames, "", "TELEMETRY LOCATION",
			func(i int) devOp { return m.otherOp(func(o *radio.Other) { o.TelemetryLoc = i }) }),
		m.cycleRow("telenv", "Messaging", "Share sensor readings with", o.TelemetryEnv, shareNames, "", "TELEMETRY ENVIRONMENT",
			func(i int) devOp { return m.otherOp(func(o *radio.Other) { o.TelemetryEnv = i }) }),
	}
	if s.HasScope {
		scope := s.Scope
		if scope == "" {
			scope = "none"
		}
		rows = append(rows, devRow{id: "scope", group: "Messaging", label: "Default flood scope", value: scope, kind: rowEdit, initial: s.Scope,
			hint: "Floods only reach repeaters in this region. Leave it empty to flood everywhere.",
			apply: func(in string) (devOp, error) {
				in = strings.TrimPrefix(strings.TrimSpace(in), "#")
				if strings.ContainsAny(in, " ,") {
					return nil, errors.New("a region name is one word")
				}
				return func(ctx context.Context, d radio.Device) error { return d.SetDefaultScope(ctx, in) }, nil
			}})
	}
	pin := fmt.Sprintf("%06d", s.Pin)
	rows = append(rows,
		devRow{id: "pin", group: "Security", label: "BLE PIN", value: "••••••", kind: rowEdit, initial: pin, hint: "Six digits, asked for when pairing over Bluetooth.",
			apply: func(in string) (devOp, error) {
				in = strings.TrimSpace(in)
				n, err := strconv.Atoi(in)
				if err != nil || len(in) != 6 || n < 100000 {
					return nil, errors.New("the PIN is six digits, not starting with 0")
				}
				return func(ctx context.Context, d radio.Device) error { return d.SetPin(ctx, uint32(n)) }, nil
			}},
		m.signingRow(),
		m.identityRow(),
		devRow{id: "export", group: "Security", label: "Export private key", value: "show", kind: rowAction,
			hint: "Anyone with this key can be you on the mesh.",
			press: func(m *Model) tea.Cmd {
				warning := "Anyone who sees it can read your messages and send as you. It's shown once, never logged or saved."
				if m.radio.Transport() == "TCP" {
					warning += " This radio is connected over WiFi, so the key crosses your network unencrypted on its way here."
				}
				m.device.prompt, m.device.confirm = devConfirm, devConfirmation{title: "SHOW THE PRIVATE KEY",
					warning: warning,
					after: func(m *Model) tea.Cmd {
						d := m.dev()
						m.device.prompt, m.device.key, m.device.busy["export"] = devShowKey, "", m.now
						return m.radioCall(func(ctx context.Context) tea.Msg { k, err := d.ExportKey(ctx); return keyExportedMsg{k, err} })
					}}
				return nil
			}},
		devRow{id: "import", group: "Security", label: "Import private key", value: "", kind: rowEdit,
			hint: "128 hex characters. Replaces this radio's identity: contacts will see a different node.",
			apply: func(in string) (devOp, error) {
				b, err := hex.DecodeString(strings.TrimSpace(in))
				if err != nil || len(b) != 64 {
					return nil, errors.New("a private key is 128 hex characters")
				}
				return func(ctx context.Context, d radio.Device) error { return d.ImportKey(ctx, [64]byte(b)) }, nil
			}},
		devRow{id: "reboot", group: "Security", label: "Reboot the radio", value: "", kind: rowAction,
			press: func(m *Model) tea.Cmd {
				m.device.prompt, m.device.input, m.device.confirm = devConfirm, lineEdit{}, devConfirmation{title: "REBOOT THE RADIO",
					warning: "The radio goes off the air and the link drops for a few seconds; it reconnects by itself.", word: "reboot", id: "reboot", what: "REBOOTING",
					op: func(ctx context.Context, d radio.Device) error { return d.Reboot(ctx) }}
				return nil
			}},
		devRow{id: "reset", group: "Security", label: "Factory reset", value: "", kind: rowAction,
			press: func(m *Model) tea.Cmd {
				m.device.prompt, m.device.input, m.device.confirm = devConfirm, lineEdit{}, devConfirmation{title: "FACTORY RESET",
					warning: "Erases every contact, channel and setting, and makes a new identity. Export the private key first if you want to keep it.",
					word:    "reset", id: "reset", what: "FACTORY RESET",
					op: func(ctx context.Context, d radio.Device) error { return d.FactoryReset(ctx) }}
				return nil
			}},
		m.radioConsoleRow(),
		devRow{id: "backup", group: "Backup", label: "Back up this radio", value: savedName(m.device.saved), kind: rowAction,
			hint: "Settings, channels and contacts to a file in " + m.exportDir() + "; you choose what goes in, and the private key only if you ask.",
			press: func(m *Model) tea.Cmd {
				if err := m.startBackup(); err != nil {
					m.fail("BACKUP  " + err.Error())
				}
				return nil
			}},
		devRow{id: "restore", group: "Backup", label: "Restore a backup", value: "", kind: rowAction,
			hint: "Pick what to bring back from a backup file; channels and contacts you have are kept.",
			press: func(m *Model) tea.Cmd {
				if err := m.startRestore(""); err != nil {
					m.fail("RESTORE  " + err.Error())
				}
				return nil
			}},
		devRow{id: "clock", group: "Clock", label: "Radio clock", value: m.clockText(), kind: rowAction, hint: "Messages carry the radio's time, so it should match yours.",
			press: func(m *Model) tea.Cmd {
				return m.runOp("clock", "CLOCK SYNCED", func(ctx context.Context, d radio.Device) error { return d.SyncClock(ctx) }, true)
			}},
	)
	for _, v := range dm.vars {
		rows = append(rows, devRow{id: "var:" + v.Name, group: "Custom variables", label: v.Name, value: v.Value, kind: rowEdit, initial: v.Value,
			apply: func(in string) (devOp, error) {
				in = strings.TrimSpace(in)
				return func(ctx context.Context, d radio.Device) error { return d.SetCustomVar(ctx, v.Name, in) }, nil
			}})
	}
	return rows
}

func presetName(p radio.RadioParams) string {
	for _, pr := range presets {
		q := pr.Params
		if q.FreqKHz == p.FreqKHz && q.BwHz == p.BwHz && q.SF == p.SF && q.CR == p.CR {
			return pr.Name
		}
	}
	return "custom"
}

func repeatHint(s radio.Settings) string {
	if len(s.RepeatFreqs) == 0 {
		return "This firmware doesn't allow the companion to repeat."
	}
	var rs []string
	for _, r := range s.RepeatFreqs {
		rs = append(rs, mhz(r.LowKHz)+"–"+mhz(r.HighKHz))
	}
	return "Allowed on " + strings.Join(rs, " and ") + " MHz."
}

// clockText compares the radio's clock with ours, carried forward from when it was read.
func (m *Model) clockText() string {
	dm := &m.device
	if dm.clock.IsZero() {
		return "unknown"
	}
	now := dm.clock.Add(m.now.Sub(dm.clockAt))
	drift := dm.clock.Sub(dm.clockAt).Round(time.Second)
	switch {
	case drift.Abs() < 2*time.Second:
		return now.Format("15:04:05") + " · in sync"
	case drift < 0:
		return now.Format("15:04:05") + " · " + (-drift).String() + " behind"
	}
	return now.Format("15:04:05") + " · " + drift.String() + " ahead"
}

func (m *Model) deviceKey(s string) (tea.Cmd, bool) {
	dm := &m.device
	if dm.prompt != devNone {
		return m.devicePromptKey(s), true
	}
	rows := m.deviceRows()
	switch s {
	case "up", "k":
		dm.sel = max(dm.sel-1, 0)
	case "down", "j":
		dm.sel = min(dm.sel+1, len(rows)-1)
	case "g", "home":
		dm.sel = 0
	case "G", "end":
		dm.sel = len(rows) - 1
	case "u":
		return m.loadDevice(), true
	case "c":
		if err := m.openConsole(); err != nil {
			m.fail("CONSOLE  " + err.Error())
		}
		return nil, true
	case "enter", "space":
		if !dm.loaded {
			return nil, true
		}
		r := rows[min(dm.sel, len(rows)-1)]
		if _, busy := dm.busy[r.id]; busy {
			return nil, true
		}
		if r.kind == rowEdit {
			dm.prompt, dm.input = devEdit, newLine(r.initial)
			return nil, true
		}
		return r.press(m), true
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) devicePromptKey(s string) tea.Cmd {
	dm := &m.device
	if s == "esc" {
		back := devNone
		if dm.prompt == devConfirm {
			back = dm.confirm.back
		}
		dm.prompt, dm.key = back, ""
		return nil
	}
	typing := func() {
		dm.input.key(s)
	}
	switch dm.prompt {
	case devEdit:
		if s != "enter" {
			typing()
			return nil
		}
		rows := m.deviceRows()
		r := rows[min(dm.sel, len(rows)-1)]
		op, err := r.apply(dm.input.text)
		if err != nil {
			m.fail("INVALID  " + err.Error())
			return nil
		}
		dm.prompt = devNone
		what := strings.ToUpper(r.label) + "  " + strings.TrimSpace(dm.input.text)
		switch {
		case r.id == "pin":
			what = "BLE PIN CHANGED"
		case r.id == "import":
			what = "PRIVATE KEY IMPORTED"
			warning := "Your contacts will see a different node, and the current private key is gone unless you exported it."
			if m.radio.Transport() == "TCP" {
				warning += " This radio is connected over WiFi, so the key crosses your network unencrypted on its way there."
			}
			dm.prompt, dm.input, dm.confirm = devConfirm, lineEdit{}, devConfirmation{title: "REPLACE THIS RADIO'S IDENTITY",
				warning: warning, word: "import", id: "import", what: what, op: op}
			return nil
		case r.group == "Radio" && r.id != "tx":
			dm.prompt, dm.input, dm.confirm = devConfirm, lineEdit{}, devConfirmation{title: "CHANGE THE RADIO SETTINGS",
				warning: "You'll only hear nodes using the same frequency, bandwidth, spreading factor and coding rate.",
				id:      r.id, what: what, op: op}
			return nil
		}
		return m.runOp(r.id, what, op, true)
	case devPresets:
		switch s {
		case "up", "k":
			dm.pick = max(dm.pick-1, 0)
		case "down", "j":
			dm.pick = min(dm.pick+1, len(presets)-1)
		case "enter":
			p := presets[dm.pick]
			params := p.Params
			params.Repeat = dm.s.Radio.Repeat && inRanges(dm.s.RepeatFreqs, params.FreqKHz)
			dm.prompt, dm.input, dm.confirm = devConfirm, lineEdit{}, devConfirmation{title: "SWITCH TO " + strings.ToUpper(p.Name),
				warning: "You'll only hear nodes using the same settings: " + paramsText(params) + ".", id: "preset", what: "RADIO  " + p.Name,
				op: func(ctx context.Context, d radio.Device) error { return d.SetRadio(ctx, params) }}
		}
	case devConfirm:
		c := dm.confirm
		if c.word == "" {
			if s != "y" {
				dm.prompt = c.back
				return nil
			}
		} else {
			if s != "enter" {
				typing()
				return nil
			}
			if !strings.EqualFold(strings.TrimSpace(dm.input.text), c.word) {
				m.fail("NOT CONFIRMED  type " + c.word + " to go ahead")
				return nil
			}
		}
		dm.prompt = devNone
		if c.after != nil {
			return c.after(m)
		}
		return m.runOp(c.id, c.what, c.op, c.id != "reboot" && c.id != "reset")
	case devBackup:
		return m.backupKey(s)
	case devConsole:
		return m.radioConsoleKey(s)
	case devRestorePath:
		if s != "enter" {
			typing()
			return nil
		}
		dm.prompt = devNone
		if err := m.openBackup(strings.TrimSpace(dm.input.text)); err != nil {
			m.fail("RESTORE  " + err.Error())
		}
	case devShowKey:
		if s == "y" && dm.key != "" {
			m.addLog(m.now, "COPIED  private key to the clipboard; it's cleared in 30 s", radio.ToneWarn)
			return m.copySecret(dm.key)
		}
		dm.prompt, dm.key = devNone, ""
	}
	return nil
}

func paramsText(p radio.RadioParams) string {
	return fmt.Sprintf("%s MHz, %s kHz, SF%d, CR%d", mhz(p.FreqKHz), khzBW(p.BwHz), p.SF, p.CR)
}

func (m *Model) deviceAnimating() bool {
	if len(m.device.busy) > 0 || m.device.loading {
		return true
	}
	for _, at := range m.device.doneAt {
		if m.now.Sub(at) < lampDur*2 {
			return true
		}
	}
	return false
}

// signingRow says whether this radio can sign channel messages. Signing is chosen per channel in Chats, since
// the radio has no setting of its own for it.
func (m *Model) signingRow() devRow {
	value, hint := "supported", "The radio signs with its own key, which never leaves it. Turn it on per channel with S in Chats; ctrl+s while writing flips it for one message. Each signed message is two packets."
	if err := m.canSign(); err != nil {
		value, hint = "not available", err.Error()+". Other people's signatures are still checked."
	}
	return devRow{id: "signing", group: "Security", label: "Message signing", value: value, kind: rowAction, hint: hint,
		press: func(m *Model) tea.Cmd {
			var signed []string
			if m.chat != nil {
				for _, ch := range m.chat.Channels() {
					if m.signedChannel(store.ChannelConvo(ch.Idx)) {
						signed = append(signed, ch.Name)
					}
				}
			}
			if len(signed) == 0 {
				m.addLog(m.now, "SIGNING  no channels sign your messages yet: open one in Chats and press S", radio.ToneInfo)
				return nil
			}
			m.addLog(m.now, "SIGNING  your messages are signed in "+strings.Join(signed, ", "), radio.ToneInfo)
			return nil
		}}
}
