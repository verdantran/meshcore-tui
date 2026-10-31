package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRoundTripAndRemember(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.UI.Theme != "neon" || c.Messages.DMRetries != 3 {
		t.Fatalf("defaults not applied: %+v", c)
	}
	c.UI.Theme = "ice"
	c.SetBattery("A3", "lifepo4")
	c.SetBattery("5F", "lipo")
	c.SetBattery("5F", "li-ion")
	c.SetLocation("A3", 51.41, -2.68, true)
	c.SetLocation("5F", 1, 1, true)
	c.SetLocation("5F", 0, 0, false)
	at := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	c.Remember(Device{Name: "MeshCore-k4", Kind: "USB", Addr: "/dev/cu.a", LastUsed: at})
	c.Remember(Device{Name: "Shed", Kind: "TCP", Addr: "10.0.0.4:5000", LastUsed: at})
	c.Remember(Device{Name: "MeshCore-k4", Kind: "USB", Addr: "/dev/cu.a", LastUsed: at.Add(time.Hour)})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Batteries) != 1 || back.Batteries["A3"] != "lifepo4" {
		t.Errorf("batteries = %v, want only A3's, since li-ion is the default", back.Batteries)
	}
	if len(back.Locations) != 1 || back.Locations["A3"] != [2]float64{51.41, -2.68} {
		t.Errorf("locations = %v", back.Locations)
	}
	if back.UI.Theme != "ice" {
		t.Errorf("theme = %q", back.UI.Theme)
	}
	if len(back.Devices) != 2 || back.Devices[0].Name != "MeshCore-k4" || !back.Devices[0].LastUsed.Equal(at.Add(time.Hour)) {
		t.Errorf("devices = %+v", back.Devices)
	}
	back.Forget("TCP", "10.0.0.4:5000")
	if _, ok := back.Device("Shed"); ok {
		t.Error("forgotten device still there")
	}
}

func TestPathsAndFailures(t *testing.T) {
	saved := goos
	t.Cleanup(func() { goos = saved })
	goos = "linux"
	t.Setenv("XDG_CONFIG_HOME", "/x/config")
	t.Setenv("XDG_DATA_HOME", "/x/data")
	if Dir() != filepath.Join("/x/config", "meshcore-tui") || DataDir() != filepath.Join("/x/data", "meshcore-tui") {
		t.Errorf("XDG paths: %s %s", Dir(), DataDir())
	}
	goos = "windows"
	t.Setenv("APPDATA", `C:\Users\sam\AppData\Roaming`)
	t.Setenv("LOCALAPPDATA", `C:\Users\sam\AppData\Local`)
	if Dir() != filepath.Join("/x/config", "meshcore-tui") {
		t.Errorf("XDG wins on Windows too: %s", Dir())
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	if Dir() != filepath.Join(`C:\Users\sam\AppData\Roaming`, "meshcore-tui") || DataDir() != filepath.Join(`C:\Users\sam\AppData\Local`, "meshcore-tui") {
		t.Errorf("Windows paths: %s %s", Dir(), DataDir())
	}
	goos = "linux"
	home, _ := os.UserHomeDir()
	if Dir() != filepath.Join(home, ".config", "meshcore-tui") || DataDir() != filepath.Join(home, ".local", "share", "meshcore-tui") {
		t.Errorf("home paths: %s %s", Dir(), DataDir())
	}

	if err := Default().Save(); err != nil {
		t.Error("a config with no path saves nowhere, quietly")
	}
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o644)
	c, _ := Load(filepath.Join(file, "config.toml"))
	if err := c.Save(); err == nil {
		t.Error("saving under a file should fail")
	}
	if _, ok := Default().Device("nobody"); ok {
		t.Error("unknown device found")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[ui]\ntheme = 3"), 0o644)
	if _, err := Load(filepath.Join(dir, "config.toml")); err == nil {
		t.Error("a wrongly typed value should fail to load")
	}
}

func TestLoadKeys(t *testing.T) {
	dir := t.TempDir()
	if k, err := LoadKeys(filepath.Join(dir, "none.toml")); err != nil || len(k) != 0 {
		t.Errorf("no file is no rebinding: %v %v", k, err)
	}
	path := filepath.Join(dir, "keys.toml")
	os.WriteFile(path, []byte("[all]\n\"ctrl+n\" = \"down\"\n[map]\nw = \"up\"\nx = \"\"\n"), 0o644)
	k, err := LoadKeys(path)
	if err != nil || k["all"]["ctrl+n"] != "down" || k["map"]["w"] != "up" || k["map"]["x"] != "" {
		t.Errorf("keys = %v %v", k, err)
	}
	os.WriteFile(path, []byte("[admin]\nu = \"r\"\n[admin.settings]\nu = \"\"\n[panes]\nb = \"s\"\n"), 0o644)
	if k, err := LoadKeys(path); err != nil || k["admin"]["u"] != "r" || k["admin.settings"]["u"] != "" || k["panes"]["b"] != "s" {
		t.Errorf("views within a screen have sections of their own: %v %v", k, err)
	}
	os.WriteFile(path, []byte("[admin.setings]\nu = \"\"\n"), 0o644)
	if _, err := LoadKeys(path); err == nil || !strings.Contains(err.Error(), "unknown section [admin.setings]") {
		t.Errorf("a misspelt view is caught: %v", err)
	}
	os.WriteFile(path, []byte("[maps]\nw = \"up\"\n"), 0o644)
	if _, err := LoadKeys(path); err == nil || !strings.Contains(err.Error(), "unknown section [maps]") {
		t.Errorf("a misspelt section is caught: %v", err)
	}
	for _, body := range []string{"[all]\nn = \"y\"\n", "[admin.list]\nesc = \"y\"\n"} {
		os.WriteFile(path, []byte(body), 0o644)
		if _, err := LoadKeys(path); err == nil || !strings.Contains(err.Error(), "bound to y") {
			t.Errorf("%q: nothing may stand for yes: %v", body, err)
		}
	}
	for _, body := range []string{`["\u001b]0;owned\u0007"]` + "\nw = \"up\"\n", "[all]\n" + `"\u001b[2J" = 3`} {
		os.WriteFile(path, []byte(body), 0o644)
		if _, err := LoadKeys(path); err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") {
			t.Errorf("%q: a name reaches the terminal as it is: %q", body, err)
		}
	}
}

func TestConfigIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no owner or mode bits to check")
	}
	path := filepath.Join(t.TempDir(), "cfg", "config.toml")
	c, _ := Load(path)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("the config is %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
		t.Errorf("its folder is %v", st.Mode().Perm())
	}
}

// Neither a stale temp file's permissions nor a symlink in its place carries the config anywhere.
func TestConfigSaveIgnoresPlantedFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no permission bits or plain symlinks")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	c, _ := Load(path)
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(path+".tmp", nil, 0o644)
	os.Remove(path)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	os.Remove(path + ".tmp")
	if err := os.Symlink(outside, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(path); err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		t.Errorf("the config is %v %v", st.Mode(), err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Error("the config went through the symlink")
	}

	open := t.TempDir()
	os.Chmod(open, 0o777)
	c, _ = Load(filepath.Join(open, "config.toml"))
	if err := c.Save(); err == nil {
		t.Error("saved into a folder anyone can write to")
	}
}

func TestLoadDropsImpossibleLocations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[locations]\nAA = [51.4, -2.6]\nBB = [inf, 0.0]\nCC = [0.0, nan]\nDD = [91.0, 0.0]\nEE = [0.0, 1e300]\n"), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Locations) != 1 || c.Locations["AA"] != [2]float64{51.4, -2.6} {
		t.Errorf("locations = %v", c.Locations)
	}
}

// Signing goes by a channel's secret: a later channel under the same name doesn't take it on.
func TestSigningByChannelSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[messages]\nsign_channels = [\"Ops\", \"Gone\"]\n"), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ops, other := [16]byte{1, 2, 3}, [16]byte{9}
	c.MoveSigning(map[string][16]byte{"Ops": ops, "Public": other})
	if !c.Signs(ops) || c.Signs(other) || len(c.Messages.SignIDs) != 1 || c.Messages.SignChannels != nil {
		t.Fatalf("moved = %+v", c.Messages)
	}
	if id := ChannelID(ops); len(id) != 16 || strings.Contains(id, "010203") {
		t.Errorf("id %q", id)
	}
	c.Save()
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "Ops") || strings.Contains(string(data), "sign_channels =") {
		t.Errorf("saved:\n%s", data)
	}
	back, _ := Load(path)
	if !back.Signs(ops) {
		t.Error("read back")
	}
	back.SetSigning(ops, false)
	back.SetSigning(other, true)
	back.SetSigning(other, true)
	if back.Signs(ops) || !back.Signs(other) || len(back.Messages.SignIDs) != 1 {
		t.Errorf("set = %v", back.Messages.SignIDs)
	}
}

func TestKeepDays(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(filepath.Join(dir, "none.toml"))
	if err != nil || c.Messages.KeepDays != 0 {
		t.Fatalf("the default keeps everything: %d, %v", c.Messages.KeepDays, err)
	}
	for text, want := range map[string]int{"": 0, "keep_days = 0": 0, "keep_days = 365": 365, "keep_days = 36500": 36500} {
		path := filepath.Join(dir, "ok.toml")
		os.WriteFile(path, []byte("[messages]\n"+text+"\n"), 0o600)
		c, err := Load(path)
		if err != nil || c.Messages.KeepDays != want {
			t.Errorf("%q: %d, %v", text, c.Messages.KeepDays, err)
		}
	}
	for _, text := range []string{"keep_days = -1", "keep_days = 36501", "keep_days = 9223372036854775807"} {
		path := filepath.Join(dir, "bad.toml")
		os.WriteFile(path, []byte("[messages]\n"+text+"\n"), 0o600)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "[messages] keep_days") {
			t.Errorf("%q: %v", text, err)
		}
	}

	c.Messages.KeepDays = 90
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if back, err := Load(c.Path()); err != nil || back.Messages.KeepDays != 90 {
		t.Fatalf("saved: %d, %v", back.Messages.KeepDays, err)
	}
}

func TestNotificationLimitAndMutes(t *testing.T) {
	dir := t.TempDir()
	c, _ := Load(filepath.Join(dir, "none.toml"))
	if c.Notifications.PerMin != 6 || c.Notifications.Muted != nil {
		t.Fatalf("defaults: %d a minute, muted %v", c.Notifications.PerMin, c.Notifications.Muted)
	}
	path := filepath.Join(dir, "bad.toml")
	os.WriteFile(path, []byte("[notifications]\nmax_per_minute = -1\n"), 0o600)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "max_per_minute") {
		t.Errorf("a negative limit: %v", err)
	}

	until := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	c.Notifications.PerMin = 0
	c.Notifications.Muted = []Mute{{Channel: "#bristol", Until: until}, {Contact: "3AAA01"}}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(c.Path())
	if strings.Count(string(data), "until") != 1 {
		t.Errorf("a mute until unmuted has no until:\n%s", data)
	}
	back, err := Load(c.Path())
	if err != nil || back.Notifications.PerMin != 0 || len(back.Notifications.Muted) != 2 ||
		!back.Notifications.Muted[0].Until.Equal(until) || back.Notifications.Muted[1].Contact != "3AAA01" {
		t.Fatalf("saved: %+v, %v", back.Notifications, err)
	}
}

func TestFlagsSetting(t *testing.T) {
	dir := t.TempDir()
	for value, ok := range map[string]bool{"auto": true, "emoji": true, "letters": true, "": true, "flags": false, "Emoji": false} {
		path := filepath.Join(dir, value+"x.toml")
		os.WriteFile(path, []byte("[ui]\nflags = \""+value+"\"\n"), 0o600)
		if _, err := Load(path); (err == nil) != ok {
			t.Errorf("flags = %q: %v", value, err)
		}
	}
}
