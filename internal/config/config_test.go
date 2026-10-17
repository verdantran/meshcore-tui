package config

import (
	"os"
	"path/filepath"
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
	t.Setenv("APPDATA", `C:\Users\me\AppData\Roaming`)
	t.Setenv("LOCALAPPDATA", `C:\Users\me\AppData\Local`)
	if Dir() != filepath.Join("/x/config", "meshcore-tui") {
		t.Errorf("XDG wins on Windows too: %s", Dir())
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	if Dir() != filepath.Join(`C:\Users\me\AppData\Roaming`, "meshcore-tui") || DataDir() != filepath.Join(`C:\Users\me\AppData\Local`, "meshcore-tui") {
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
