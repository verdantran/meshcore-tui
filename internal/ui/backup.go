package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/backup"
	"github.com/verdantran/meshcore-tui/internal/chat"
	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

const deviceTab = 5

// backupState is a backup being chosen, or a backup file being restored.
type backupState struct {
	restoring bool
	file      *backup.Backup
	path      string
	chosen    map[backup.Part]bool
	pick      int
	plan      restorePlan
}

// restorePlan is what restoring would add, worked out against what the radio holds when the file is opened.
type restorePlan struct {
	channels    []radio.Channel
	chanHave    int
	chanRefused []string
	contacts    []radio.Contact
	contactHave int
}

type backupDoneMsg struct {
	path, what string
	key        bool
	err        error
}

type restoredMsg struct {
	done, failed []string
	key          bool
}

func (m *Model) exportDir() string {
	if m.opt.ExportDir != "" {
		return m.opt.ExportDir
	}
	return filepath.Join(config.DataDir(), "exports")
}

// startBackup opens the list of what to put in a backup; the private key is left out unless it's chosen.
func (m *Model) startBackup() error {
	if m.dev() == nil || m.chat == nil {
		return fmt.Errorf("only a companion radio can be backed up")
	}
	m.setTab(deviceTab)
	dm := &m.device
	dm.bak = backupState{chosen: map[backup.Part]bool{}}
	for _, p := range m.backupParts() {
		dm.bak.chosen[p] = p != backup.PartKey
	}
	dm.prompt = devBackup
	if !dm.loaded {
		m.queued = m.loadDevice()
	}
	return nil
}

// backupParts are the parts on offer: everything the radio has when backing up, what the file holds when restoring.
func (m *Model) backupParts() []backup.Part {
	b := &m.device.bak
	var out []backup.Part
	for _, p := range backup.Parts {
		switch {
		case b.restoring && !b.file.Has(p):
		case !b.restoring && p == backup.PartPin && m.device.s.Pin == 0:
		case !b.restoring && p == backup.PartChannels && len(m.chat.Channels()) == 0:
		case !b.restoring && p == backup.PartContacts && len(m.chat.Contacts()) == 0:
		default:
			out = append(out, p)
		}
	}
	return out
}

// newestBackup is the most recent backup in the exports folder, to offer when restoring.
func (m *Model) newestBackup() string {
	files, _ := filepath.Glob(filepath.Join(m.exportDir(), "radio-*.json"))
	newest, at := "", int64(0)
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().UnixNano() > at {
			newest, at = f, st.ModTime().UnixNano()
		}
	}
	return newest
}

// startRestore asks which backup file to restore, or opens the one given.
func (m *Model) startRestore(path string) error {
	if m.dev() == nil || m.chat == nil {
		return fmt.Errorf("a backup can only be restored to a companion radio")
	}
	m.setTab(deviceTab)
	dm := &m.device
	if !dm.loaded {
		m.queued = m.loadDevice()
	}
	if path == "" {
		dm.prompt, dm.input = devRestorePath, newLine(m.newestBackup())
		return nil
	}
	return m.openBackup(path)
}

func (m *Model) openBackup(path string) error {
	if len(path) > 1 && path[0] == '"' && path[len(path)-1] == '"' {
		path = path[1 : len(path)-1] // as Windows' Copy as path gives it
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, rest)
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("couldn't open %s: %v", filepath.Base(path), err)
	}
	defer f.Close()
	b, err := backup.Parse(f)
	if err != nil {
		return err
	}
	dm := &m.device
	dm.bak = backupState{restoring: true, file: b, path: path, chosen: map[backup.Part]bool{}}
	for _, p := range m.backupParts() {
		dm.bak.chosen[p] = p != backup.PartKey
	}
	dm.bak.plan = m.planRestore(b)
	dm.prompt = devBackup
	return nil
}

// planRestore sorts the backup's channels and contacts into those to add and those the radio already has.
// A channel goes through the same checks as one added by hand, then into the first free slot.
func (m *Model) planRestore(b *backup.Backup) restorePlan {
	var p restorePlan
	used := map[int]bool{}
	for _, c := range m.chat.Channels() {
		used[c.Idx] = true
	}
	free := func() (int, bool) {
		for i := range m.slots() {
			if !used[i] {
				used[i] = true
				return i, true
			}
		}
		return 0, false
	}
	var names []string
	for _, bc := range b.Channels {
		c := bc.Radio()
		if slices.ContainsFunc(m.chat.Channels(), func(have radio.Channel) bool { return have.Secret == c.Secret }) {
			p.chanHave++
			continue
		}
		secret := fmt.Sprintf("%x", c.Secret)
		if strings.HasPrefix(c.Name, "#") {
			secret = "" // a hashtag's secret comes from its name, checked below
		}
		plan, err := m.chat.PlanChannel(c.Name, secret, m.slots())
		if err == nil && plan.Channel.Secret != c.Secret {
			err = fmt.Errorf("its secret isn't %s's, so it would look open when it isn't", c.Name)
		}
		if err != nil && err != chat.ErrNoSlot {
			p.chanRefused = append(p.chanRefused, c.Name+": "+err.Error())
			continue
		}
		if slices.Contains(names, strings.ToLower(plan.Channel.Name)) {
			p.chanRefused = append(p.chanRefused, c.Name+": the backup has it twice")
			continue
		}
		idx, ok := free()
		if !ok {
			p.chanRefused = append(p.chanRefused, c.Name+": "+chat.ErrNoSlot.Error())
			continue
		}
		names = append(names, strings.ToLower(plan.Channel.Name))
		p.channels = append(p.channels, radio.Channel{Idx: idx, Name: plan.Channel.Name, Secret: c.Secret})
	}
	have := map[[32]byte]bool{}
	for _, c := range m.chat.Contacts() {
		have[c.Key] = true
	}
	for _, bc := range b.Contacts {
		c, _ := bc.Radio() // Parse has checked it
		if have[c.Key] {
			p.contactHave++
			continue
		}
		have[c.Key] = true
		p.contacts = append(p.contacts, c)
	}
	return p
}

func (m *Model) backupKey(s string) tea.Cmd {
	dm := &m.device
	b := &dm.bak
	parts := m.backupParts()
	switch s {
	case "up", "k":
		b.pick = max(b.pick-1, 0)
	case "down", "j":
		b.pick = min(b.pick+1, len(parts)-1)
	case "space", "x":
		if b.pick < len(parts) {
			b.chosen[parts[b.pick]] = !b.chosen[parts[b.pick]]
		}
	case "a":
		all := !slices.ContainsFunc(parts, func(p backup.Part) bool { return p != backup.PartKey && b.chosen[p] })
		for _, p := range parts {
			if p != backup.PartKey {
				b.chosen[p] = all
			}
		}
	case "enter":
		if !slices.ContainsFunc(parts, func(p backup.Part) bool { return b.chosen[p] }) {
			m.fail("BACKUP  choose at least one thing, with space")
			return nil
		}
		if b.restoring {
			m.confirmRestore()
			return nil
		}
		if b.chosen[backup.PartKey] {
			warning := "The file will hold this radio's private key: anyone who gets it can read your messages and send as you. Keep it somewhere safe, and delete it when you're done."
			if m.radio.Transport() == "TCP" {
				warning += " This radio is connected over WiFi, so the key crosses your network unencrypted on its way here."
			}
			dm.prompt, dm.input, dm.confirm = devConfirm, lineEdit{}, devConfirmation{title: "PUT THE PRIVATE KEY IN THE BACKUP",
				warning: warning, after: func(m *Model) tea.Cmd { return m.writeBackup() }}
			return nil
		}
		dm.prompt = devNone
		return m.writeBackup()
	}
	return nil
}

// writeBackup reads what's chosen from the radio and saves it, readable only by you.
func (m *Model) writeBackup() tea.Cmd {
	dm := &m.device
	chosen := dm.bak.chosen
	d, r := m.dev(), m.radio
	name := m.ready.Self.Name
	f, path, err := m.createExport(fmt.Sprintf("radio-%s-%s.json", fileName(slug(name)), m.now.Format("2006-01-02-150405")))
	if err != nil {
		m.fail("BACKUP  " + err.Error())
		return nil
	}
	b := backup.Backup{App: m.chat.App, Made: m.now.UTC(), RadioName: name}
	var what []string
	if chosen[backup.PartChannels] {
		for _, c := range m.chat.Channels() {
			b.Channels = append(b.Channels, backup.FromChannel(c))
		}
		what = append(what, plural(len(b.Channels), "channel"))
	}
	if chosen[backup.PartContacts] {
		for _, c := range m.chat.Contacts() {
			b.Contacts = append(b.Contacts, backup.FromContact(c))
		}
		what = append(what, plural(len(b.Contacts), "contact"))
	}
	dm.busy["backup"] = m.now
	return m.radioCall(func(ctx context.Context) tea.Msg {
		done := func(err error) tea.Msg {
			f.Close()
			if err != nil {
				os.Remove(path)
			}
			return backupDoneMsg{path: path, what: strings.Join(what, ", "), key: chosen[backup.PartKey], err: err}
		}
		s, err := d.Settings(ctx)
		if err != nil {
			return done(err)
		}
		if chosen[backup.PartIdentity] {
			b.Identity = &backup.Identity{Name: s.Self.Name, Lat: s.Self.Lat, Lon: s.Self.Lon}
		}
		if chosen[backup.PartRadio] {
			b.Radio = &backup.Radio{FreqKHz: s.Radio.FreqKHz, BwHz: s.Radio.BwHz, SF: s.Radio.SF, CR: s.Radio.CR, Repeat: s.Radio.Repeat,
				TxDBm: int(s.Self.TxPower), RxDelay: s.Tuning.RxDelay, AirtimeFactor: s.Tuning.AirtimeFactor}
		}
		if chosen[backup.PartOptions] {
			aa, err := r.AutoAdd(ctx)
			if err != nil {
				return done(err)
			}
			o := s.Other
			b.Options = &backup.Options{ManualAdd: o.ManualAdd, ManualFlags: o.ManualFlags, TelemetryBase: o.TelemetryBase, TelemetryLoc: o.TelemetryLoc,
				TelemetryEnv: o.TelemetryEnv, AdvertLocPolicy: o.AdvertLocPolicy, MultiAcks: o.MultiAcks, PathHashMode: s.PathHashMode,
				AutoAdd: backup.AutoAdd{Chat: aa.Chat, Repeater: aa.Repeater, Room: aa.Room, Sensor: aa.Sensor, OverwriteOldest: aa.OverwriteOldest, MaxHops: aa.MaxHops}}
			if s.HasScope {
				b.Options.FloodScope = &s.Scope
			}
		}
		if chosen[backup.PartPin] {
			b.Pin = &s.Pin
		}
		if chosen[backup.PartKey] {
			k, err := d.ExportKey(ctx)
			if err != nil {
				return done(err)
			}
			b.PrivateKey = fmt.Sprintf("%x", k)
		}
		return done(b.Write(f))
	})
}

func (m *Model) confirmRestore() {
	dm := &m.device
	b := &dm.bak
	var what []string
	for _, p := range m.backupParts() {
		if b.chosen[p] {
			what = append(what, strings.ToLower(p.String()))
		}
	}
	warning := "This changes the radio straight away: " + strings.Join(what, ", ") + ". Channels and contacts you have are kept; only missing ones are added."
	if b.chosen[backup.PartRadio] {
		warning += " With new radio settings you'll only hear nodes that use them too."
	}
	if b.chosen[backup.PartKey] {
		warning += " The private key replaces this radio's identity: contacts will see a different node, and the current key is gone unless you've backed it up."
		if m.radio.Transport() == "TCP" {
			warning += " This radio is connected over WiFi, so the key crosses your network unencrypted on its way there."
		}
	}
	dm.prompt, dm.input, dm.confirm = devConfirm, lineEdit{}, devConfirmation{title: "RESTORE " + strings.ToUpper(filepath.Base(b.path)),
		warning: warning, word: "restore", after: func(m *Model) tea.Cmd { return m.applyRestore() }}
}

// applyRestore sends each chosen part to the radio in turn, the private key last; a part that fails doesn't
// stop the others.
func (m *Model) applyRestore() tea.Cmd {
	dm := &m.device
	b, chosen, plan := dm.bak.file, dm.bak.chosen, dm.bak.plan
	d, r, s := m.dev(), m.radio, dm.s
	dm.busy["restore"] = m.now
	limit := opTimeout + time.Duration(len(plan.channels)+len(plan.contacts))*restoreEach
	return m.radioCallFor(limit, func(ctx context.Context) tea.Msg {
		var msg restoredMsg
		step := func(what string, err error) {
			if err != nil {
				msg.failed = append(msg.failed, what+": "+radio.Explain(err))
				return
			}
			msg.done = append(msg.done, what)
		}
		if chosen[backup.PartIdentity] {
			id := b.Identity
			err := d.SetName(ctx, id.Name)
			if err == nil {
				err = d.SetLocation(ctx, id.Lat, id.Lon)
			}
			step("name and location", err)
		}
		if chosen[backup.PartRadio] {
			br := b.Radio
			p := radio.RadioParams{FreqKHz: br.FreqKHz, BwHz: br.BwHz, SF: br.SF, CR: br.CR, Repeat: br.Repeat && inRanges(s.RepeatFreqs, br.FreqKHz)}
			err := d.SetRadio(ctx, p)
			if err == nil {
				err = d.SetTxPower(ctx, min(br.TxDBm, max(s.MaxTxPower, 1)))
			}
			if err == nil {
				err = d.SetTuning(ctx, radio.Tuning{RxDelay: br.RxDelay, AirtimeFactor: br.AirtimeFactor})
			}
			step("radio settings", err)
		}
		if chosen[backup.PartOptions] {
			o := b.Options
			err := d.SetOther(ctx, radio.Other{ManualAdd: o.ManualAdd, ManualFlags: o.ManualFlags, TelemetryBase: o.TelemetryBase, TelemetryLoc: o.TelemetryLoc,
				TelemetryEnv: o.TelemetryEnv, AdvertLocPolicy: o.AdvertLocPolicy, MultiAcks: o.MultiAcks})
			if err == nil {
				err = d.SetPathHashMode(ctx, o.PathHashMode)
			}
			if err == nil {
				a := o.AutoAdd
				err = r.SetAutoAdd(ctx, radio.AutoAdd{Chat: a.Chat, Repeater: a.Repeater, Room: a.Room, Sensor: a.Sensor, OverwriteOldest: a.OverwriteOldest, MaxHops: a.MaxHops})
			}
			if err == nil && o.FloodScope != nil && s.HasScope {
				err = d.SetDefaultScope(ctx, *o.FloodScope)
			}
			step("options", err)
		}
		if chosen[backup.PartPin] {
			step("BLE PIN", d.SetPin(ctx, *b.Pin))
		}
		if chosen[backup.PartChannels] {
			n := 0
			for _, c := range plan.channels {
				if err := r.SetChannel(ctx, c); err != nil {
					step("channel "+c.Name, err)
					continue
				}
				n++
			}
			msg.done = append(msg.done, plural(n, "channel"))
		}
		if chosen[backup.PartContacts] {
			n := 0
			for _, c := range plan.contacts {
				if err := r.SaveContact(ctx, c); err != nil {
					step("contact "+c.Name, err)
					if len(msg.failed) > 5 {
						break // the radio's table is likely full; one reason is enough
					}
					continue
				}
				n++
			}
			msg.done = append(msg.done, plural(n, "contact"))
		}
		if chosen[backup.PartKey] {
			k, _ := b.Key()
			err := d.ImportKey(ctx, k)
			step("private key", err)
			msg.key = err == nil
		}
		return msg
	})
}

func (m *Model) backupMsg(msg tea.Msg) tea.Cmd {
	dm := &m.device
	switch msg := msg.(type) {
	case backupDoneMsg:
		delete(dm.busy, "backup")
		if msg.err != nil {
			dm.failedAt["backup"] = m.now
			m.fail("BACKUP  " + radio.Explain(msg.err))
			return nil
		}
		dm.doneAt["backup"], dm.saved = m.now, msg.path
		line := "BACKED UP  to " + msg.path
		if msg.what != "" {
			line = "BACKED UP  " + msg.what + ", to " + msg.path
		}
		m.addLog(m.now, line, radio.ToneOK)
		if msg.key {
			m.addLog(m.now, "BACKUP  it holds the private key; keep it safe", radio.ToneWarn)
		}
	case restoredMsg:
		delete(dm.busy, "restore")
		if msg.key {
			m.expectNewKey()
		}
		if len(msg.done) > 0 {
			dm.doneAt["restore"] = m.now
			m.addLog(m.now, "RESTORED  "+strings.Join(msg.done, ", "), radio.ToneOK)
		}
		for _, f := range msg.failed {
			dm.failedAt["restore"] = m.now
			m.fail("RESTORE  " + f)
		}
		r := m.radio
		return tea.Batch(m.loadDevice(), m.syncContacts(), m.radioCall(func(ctx context.Context) tea.Msg {
			list, err := r.Channels(ctx)
			return channelsMsg{list: list, err: err, what: "CHANNELS  read back after the restore"}
		}))
	}
	return nil
}

// partDetail says what a part holds, or when restoring, what it would change.
func (m *Model) partDetail(p backup.Part) string {
	dm := &m.device
	b, s := &dm.bak, dm.s
	if !b.restoring {
		switch p {
		case backup.PartIdentity:
			return fmt.Sprintf("%s, %.4f, %.4f", untrusted.Line(s.Self.Name), s.Self.Lat, s.Self.Lon)
		case backup.PartRadio:
			return fmt.Sprintf("%s, %d dBm", paramsText(s.Radio), int(s.Self.TxPower))
		case backup.PartOptions:
			return "telemetry sharing, auto-add, path hash size, extra acks"
		case backup.PartPin:
			return "the Bluetooth pairing PIN"
		case backup.PartChannels:
			return plural(len(m.chat.Channels()), "channel") + ", with their secrets"
		case backup.PartContacts:
			return plural(len(m.chat.Contacts()), "contact") + ", with their routes"
		case backup.PartKey:
			return "the radio's identity; anyone with the file can be you"
		}
	}
	f := b.file
	switch p {
	case backup.PartIdentity:
		if f.Identity.Name == s.Self.Name {
			return fmt.Sprintf("%s, at %.4f, %.4f", f.Identity.Name, f.Identity.Lat, f.Identity.Lon)
		}
		return fmt.Sprintf("%s → %s", untrusted.Line(s.Self.Name), f.Identity.Name)
	case backup.PartRadio:
		br := f.Radio
		to := paramsText(radio.RadioParams{FreqKHz: br.FreqKHz, BwHz: br.BwHz, SF: br.SF, CR: br.CR})
		if from := paramsText(s.Radio); from != to {
			return from + " → " + to
		}
		return to + ", as now"
	case backup.PartOptions:
		return "telemetry sharing, auto-add, path hash size, extra acks"
	case backup.PartPin:
		return "the Bluetooth pairing PIN"
	case backup.PartChannels:
		out := fmt.Sprintf("%d to add", len(b.plan.channels))
		if b.plan.chanHave > 0 {
			out += fmt.Sprintf(", %d already here", b.plan.chanHave)
		}
		if n := len(b.plan.chanRefused); n > 0 {
			out += fmt.Sprintf(", %d refused", n)
		}
		return out
	case backup.PartContacts:
		out := fmt.Sprintf("%d to add", len(b.plan.contacts))
		if b.plan.contactHave > 0 {
			out += fmt.Sprintf(", %d already here", b.plan.contactHave)
		}
		return out
	case backup.PartKey:
		return "replaces this radio's identity"
	}
	return ""
}

func (m *Model) backupLines(w int) []string {
	th, b := m.th, &m.device.bak
	title := "BACK UP THIS RADIO"
	if b.restoring {
		title = "RESTORE " + strings.ToUpper(filepath.Base(b.path))
	}
	out := []string{th.Fg(th.Muted, true).Render(title) + th.Fg(th.Dim, false).Render("  ·  ↑↓ choose · space tick · a all · enter go ahead · esc cancel"), ""}
	if b.restoring {
		from := "from " + b.file.RadioName
		if b.file.App != "" {
			from += " by " + b.file.App
		}
		out = append(out, th.Fg(th.Dim, false).Render(clip(from+", "+b.file.Made.Local().Format("2 Jan 2006 15:04"), w)), "")
	}
	parts := m.backupParts()
	b.pick = min(b.pick, max(len(parts)-1, 0))
	for i, p := range parts {
		box := "[ ]"
		if b.chosen[p] {
			box = "[x]"
		}
		line := fmt.Sprintf("%s %-18s %s", box, p.String(), m.partDetail(p))
		col := th.Text
		if p == backup.PartKey {
			col = th.Yellow
		}
		if i == b.pick {
			out = append(out, clip(th.Fg(th.Magenta, true).Render("▌")+m.raised(line), w))
			continue
		}
		out = append(out, clip(" "+th.Fg(col, false).Render(line), w))
	}
	out = append(out, "")
	note := "Saved to your exports folder, readable only by you."
	if b.chosen[backup.PartChannels] && !b.restoring {
		note += " Channel secrets are in it, so anyone with the file can read those channels."
	}
	if b.restoring {
		note = "Channels and contacts you already have are kept; only missing ones are added."
		for _, r := range b.plan.chanRefused {
			note += " Not restored: " + untrusted.Line(r) + "."
		}
	}
	for _, l := range strings.Split(renderWidth.Wrap(note, w, " "), "\n") {
		out = append(out, th.Fg(th.Dim, false).Render(l))
	}
	if b.chosen[backup.PartKey] {
		out = append(out, "", th.Fg(th.Yellow, true).Render(clip("⚠ The private key is ticked.", w)))
	}
	return out
}

func (m *Model) restorePathLines(w int) []string {
	th, dm := m.th, &m.device
	out := []string{th.Fg(th.Muted, true).Render("RESTORE A BACKUP"), "", th.Fg(th.Magenta, true).Render("▸ ") + dm.input.render(th, w-2, false), ""}
	hint := "The path of a backup file. The newest in your exports folder is filled in, and pasting a path replaces it; nothing changes until you've chosen what to restore."
	for _, l := range strings.Split(renderWidth.Wrap(hint, w, " "), "\n") {
		out = append(out, th.Fg(th.Dim, false).Render(l))
	}
	return append(out, "", th.Fg(th.Dim, false).Render("enter open it · ctrl+u clear · esc cancel"))
}
