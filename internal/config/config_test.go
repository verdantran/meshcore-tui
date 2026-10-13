package config

import (
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
