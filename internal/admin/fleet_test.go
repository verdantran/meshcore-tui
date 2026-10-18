package admin

import (
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

func TestFleetAlerts(t *testing.T) {
	s := newService()
	a, b := s.Session(dundry), s.Session(room)
	a.Owner = &meshcore.OwnerInfo{FirmwareVersion: "v1.15.0"}
	b.Owner = &meshcore.OwnerInfo{FirmwareVersion: "v1.17.1"}
	s.Session(radio.Contact{Key: [32]byte{9}})
	if s.Newest() != "v1.15.0" {
		t.Fatalf("newest = %s", s.Newest())
	}
	s.Session(radio.Contact{Key: [32]byte{10}}).Owner = &meshcore.OwnerInfo{FirmwareVersion: "v1.17.1"}
	if s.Newest() != "v1.17.1" {
		t.Fatalf("two nodes run v1.17.1: newest = %s", s.Newest())
	}
	a.Status = []StatusPoint{{Stats: meshcore.RepeaterStats{CommonStats: meshcore.CommonStats{BattMilliVolts: 3410}}}}
	th := Thresholds{BatteryVolts: 3.5, Stale: 24 * time.Hour}
	got := Check(a, t0.Add(-30*time.Hour), s.Newest(), th, t0)
	if !got.LowBattery || got.Silent < 30*time.Hour || !got.OldFirmware || got.String() != "LOW BATTERY · SILENT 30h · OLD FIRMWARE" {
		t.Errorf("alerts = %+v %q", got, got)
	}
	th.Chemistry = func([32]byte) radio.Chemistry { return radio.LiFePO4 }
	a.Status[0].Stats.BattMilliVolts = 3300
	if Check(a, t0, s.Newest(), th, t0).LowBattery {
		t.Error("a LiFePO4 cell at 3.3 V is three-quarters full")
	}
	a.Status[0].Stats.BattMilliVolts = 3050
	if !Check(a, t0, s.Newest(), th, t0).LowBattery {
		t.Error("a LiFePO4 cell at 3.05 V is nearly flat")
	}
	th.Chemistry = func([32]byte) radio.Chemistry { return radio.Wired }
	a.Status[0].Stats.BattMilliVolts = 0
	if Check(a, t0, s.Newest(), th, t0).LowBattery {
		t.Error("a wired node has no battery to run low")
	}
	th.Chemistry = nil
	if got := Check(b, t0, s.Newest(), th, t0); got.Any() || got.String() != "" {
		t.Errorf("healthy = %+v", got)
	}
	for _, c := range []struct {
		a, b string
		want int
	}{{"v1.17.1", "v1.17.0", 1}, {"1.9.0", "v1.10.0", -1}, {"v1.17.1-dev", "v1.17.1", 0}, {"junk", "v0.0.1", -1}, {"v2", "v1.9.9", 1}} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare %s %s = %d", c.a, c.b, got)
		}
	}
}

func TestOneNodeCantInflateTheNewestFirmware(t *testing.T) {
	s := newService()
	if s.Newest() != "" {
		t.Error("no nodes, nothing to compare with")
	}
	a, b := s.Session(dundry), s.Session(room)
	a.Owner = &meshcore.OwnerInfo{FirmwareVersion: "v1.17.1"}
	if s.Newest() != "" {
		t.Error("one node alone sets no bar")
	}
	b.Owner = &meshcore.OwnerInfo{FirmwareVersion: "v99.0.0"}
	if got := Check(a, t0, s.Newest(), Thresholds{}, t0); got.OldFirmware {
		t.Errorf("one node claiming v99 flagged the rest: newest %s", s.Newest())
	}
}
