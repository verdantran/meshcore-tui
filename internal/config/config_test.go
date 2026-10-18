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
