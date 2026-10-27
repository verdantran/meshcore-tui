package ui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/backup"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

func (h *harness) openDevice() {
	h.t.Helper()
	h.keys("6")
	h.until("settings read", func() bool { return h.m.device.loaded })
}

// makeBackup backs up the harness's radio, ticking the private key too if key is set.
func (h *harness) makeBackup(key bool) (string, *backup.Backup) {
	h.t.Helper()
	h.openDevice()
	h.moveTo("backup")
	h.key(tea.KeyEnter)
	if key {
		for range 10 {
			h.key(tea.KeyDown)
		}
		h.keys(" ")
	}
	h.key(tea.KeyEnter)
	if key {
		if !strings.Contains(h.screen(), "PUT THE PRIVATE KEY IN THE BACKUP") {
			h.t.Fatalf("the key is asked about first:\n%s", h.screen())
		}
		h.keys("y")
	}
	h.until("backed up", func() bool { return strings.Contains(plainLog(h), "BACKED UP") })
	files, _ := filepath.Glob(filepath.Join(h.m.opt.ExportDir, "radio-*.json"))
	if len(files) != 1 {
		h.t.Fatalf("files %v", files)
	}
	data, _ := os.ReadFile(files[0])
	b, err := backup.Parse(bytes.NewReader(data))
	if err != nil {
		h.t.Fatal(err)
	}
	return files[0], b
}

func TestBackupAndRestore(t *testing.T) {
	a := newHarness(t)
	a.keys("1")
	a.keys("n")
	a.typeLine("#bath")
	a.until("joined", func() bool { _, ok := a.channel("#bath"); return ok })

	a.openDevice()
	a.moveTo("backup")
	a.key(tea.KeyEnter)
	for _, want := range []string{"BACK UP THIS RADIO", "[x] Channels", "4 channels, with their secrets", "[ ] Private key"} {
		if !strings.Contains(a.screen(), want) {
			t.Fatalf("missing %q:\n%s", want, a.screen())
		}
	}
	a.key(tea.KeyEscape)
	path, b := a.makeBackup(false)
	if !strings.Contains(a.screen(), filepath.Base(path)) {
		t.Errorf("the backup row shows where it went:\n%s", a.screen())
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("a backup is %v; only you should read it", st.Mode().Perm())
	}
	if b.Has(backup.PartKey) || len(b.Channels) != 4 || len(b.Contacts) != len(a.m.chat.Contacts()) || b.Radio == nil || b.Identity == nil {
		t.Fatalf("backup: key %v, %d channels, %d contacts", b.Has(backup.PartKey), len(b.Channels), len(b.Contacts))
	}

	z := newHarness(t)
	keyBefore, _ := z.s.ExportKey(t0ctx)
	z.keys(":")
	z.typeFast("radio restore " + path)
	z.key(tea.KeyEnter)
	z.until("opened", func() bool { return z.m.device.loaded && z.m.device.prompt == devBackup })
	for _, want := range []string{"RESTORE RADIO-", "1 to add, 3 already here", fmt.Sprintf("0 to add, %d already here", len(b.Contacts))} {
		if !strings.Contains(z.screen(), want) {
			t.Fatalf("missing %q:\n%s", want, z.screen())
		}
	}
	if z.m.device.bak.chosen[backup.PartKey] {
		t.Error("the private key is never ticked to begin with")
	}
	z.key(tea.KeyEnter)
	z.typeLine("nope")
	if !strings.Contains(z.lastLog(), "NOT CONFIRMED") {
		t.Fatal("restoring needs the word")
	}
	z.clear()
	z.typeLine("restore")
	z.until("restored", func() bool { _, ok := z.channel("#bath"); return ok })
	if !strings.Contains(plainLog(z), "RESTORED") {
		t.Errorf("log:\n%s", plainLog(z))
	}
	if len(z.m.chat.Channels()) != 4 {
		t.Errorf("channels already there aren't added twice: %d", len(z.m.chat.Channels()))
	}
	if k, _ := z.s.ExportKey(t0ctx); k != keyBefore {
		t.Error("the key stays unless it's chosen")
	}
}

func TestBackupWithTheKey(t *testing.T) {
	a := newHarness(t)
	path, b := a.makeBackup(true)
	want, _ := a.s.ExportKey(t0ctx)
	if k, err := b.Key(); err != nil || k != want {
		t.Fatal("the key is in the backup when it's chosen")
	}
	if !strings.Contains(plainLog(a), "holds the private key") {
		t.Error("the log warns about it")
	}

	z := newHarness(t)
	z.openDevice()
	if err := z.m.startRestore(path); err != nil {
		t.Fatal(err)
	}
	if z.m.device.bak.chosen[backup.PartKey] || !strings.Contains(z.screen(), "[ ] Private key") {
		t.Fatalf("a key in the file still has to be ticked:\n%s", z.screen())
	}
	for range 10 {
		z.key(tea.KeyDown)
	}
	z.keys(" ")
	z.key(tea.KeyEnter)
	if !strings.Contains(z.screen(), "replaces this radio's identity") {
		t.Fatalf("restoring the key warns:\n%s", z.screen())
	}
	z.typeLine("restore")
	z.until("restored", func() bool { return strings.Contains(plainLog(z), "private key") })
	if k, _ := z.s.ExportKey(t0ctx); k != want || !z.m.renewKey {
		t.Error("the key is restored, and its next proof is taken as the radio's new identity")
	}
}

func TestARestoredKeyIsAcceptedAfterARestart(t *testing.T) {
	a := newHarness(t)
	path, _ := a.makeBackup(true)

	z := newHarness(t)
	z.until("checked", func() bool { return z.m.trust != trustChecking })
	z.onDevice("BLE", "AA:BB", radio.KeyHex(z.m.ready.Self.PubKey[:]))
	z.openDevice()
	if err := z.m.startRestore(path); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		z.key(tea.KeyDown)
	}
	z.keys(" ")
	z.key(tea.KeyEnter)
	z.typeLine("restore")
	z.until("restored", func() bool { return strings.Contains(plainLog(z), "private key") })

	// The app is closed before the radio proves its new key: it isn't flagged when it does.
	z.m.renewKey = false
	if z.m.opt.Config.Devices[0].Key != "" {
		t.Fatalf("the old key is forgotten: %q", z.m.opt.Config.Devices[0].Key)
	}
	z.m.ready.Self.PubKey = a.m.ready.Self.PubKey
	z.s = a.s // the radio now signs as the one backed up
	z.prove(true)
	if z.m.trust != trustVerified || z.m.opt.Config.Devices[0].Key != radio.KeyHex(a.m.ready.Self.PubKey[:]) {
		t.Errorf("the restored key is taken as the radio's identity: %v %q", z.m.trust, z.m.opt.Config.Devices[0].Key)
	}
}

func TestRestoreRefusesWhatWouldMislead(t *testing.T) {
	h := newHarness(t)
	h.openDevice()
	b := backup.Backup{Made: time.Now(), RadioName: "Mallory",
		Channels: []backup.Channel{
			backup.FromChannel(radio.Channel{Idx: 5, Name: "#evil", Secret: [16]byte{9, 9, 9}}),                // a hashtag that isn't
			backup.FromChannel(radio.Channel{Idx: 6, Name: "Club", Secret: meshcore.DeriveHashtagPSK("club")}), // private, with a secret anyone can work out
		}}
	path := filepath.Join(t.TempDir(), "evil.json")
	f, _ := os.Create(path)
	b.Write(f)
	f.Close()
	if err := h.m.startRestore(path); err != nil {
		t.Fatal(err)
	}
	refused := strings.Join(h.m.device.bak.plan.chanRefused, "; ")
	if !strings.Contains(refused, "#evil") || !strings.Contains(refused, "Club") || len(h.m.device.bak.plan.channels) != 0 {
		t.Fatalf("a fake hashtag and a guessable secret are refused: %q", refused)
	}
	if !strings.Contains(h.screen(), "Not restored: #evil") {
		t.Errorf("and the screen says so:\n%s", h.screen())
	}

	os.WriteFile(path, []byte(`{"format":"something else"}`), 0o600)
	h.key(tea.KeyEscape)
	if err := h.m.startRestore(path); err == nil || h.m.device.prompt == devBackup {
		t.Error("a file that isn't a backup is refused")
	}
}

func TestRestorePathTakesAPaste(t *testing.T) {
	a := newHarness(t)
	path, _ := a.makeBackup(false)
	a.moveTo("restore")
	a.key(tea.KeyEnter)
	if a.m.device.prompt != devRestorePath || a.m.device.input.text != path {
		t.Fatalf("the newest backup is filled in: %q", a.m.device.input.text)
	}
	a.paste(`"` + path + `"`) // as Windows' Copy as path gives it
	if a.m.device.input.text != `"`+path+`"` {
		t.Fatalf("a paste replaces what was filled in: %q", a.m.device.input.text)
	}
	a.key(tea.KeyEnter)
	a.until("opened", func() bool { return a.m.device.prompt == devBackup })
}

func hasContact(h *harness, key [32]byte) bool {
	return slices.ContainsFunc(h.m.chat.Contacts(), func(c radio.Contact) bool { return c.Key == key })
}

func TestRestoreBringsBackAContact(t *testing.T) {
	a := newHarness(t)
	path, b := a.makeBackup(false)
	gone := a.m.chat.Contacts()[0]
	if err := a.s.RemoveContact(t0ctx, gone.Key); err != nil {
		t.Fatal(err)
	}
	a.start(a.m.syncContacts())
	a.until("removed", func() bool { return !hasContact(a, gone.Key) })

	a.keys(":")
	a.typeFast("radio restore " + path)
	a.key(tea.KeyEnter)
	a.until("opened", func() bool { return a.m.device.prompt == devBackup })
	if !strings.Contains(a.screen(), fmt.Sprintf("1 to add, %d already here", len(b.Contacts)-1)) {
		t.Fatalf("plan:\n%s", a.screen())
	}
	a.key(tea.KeyEnter)
	a.typeLine("restore")
	a.until("restored", func() bool { return hasContact(a, gone.Key) })
	if a.keys("2"); !strings.Contains(a.screen(), gone.Name) {
		t.Errorf("%s isn't on the Contacts screen:\n%s", gone.Name, a.screen())
	}
}
