package admin

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Alerts are what the fleet view flags for a node.
type Alerts struct {
	LowBattery  bool
	Silent      time.Duration // since its last advert, when that's too long
	OldFirmware bool
}

func (a Alerts) Any() bool { return a.LowBattery || a.Silent > 0 || a.OldFirmware }

func (a Alerts) String() string {
	var out []string
	if a.LowBattery {
		out = append(out, "LOW BATTERY")
	}
	if a.Silent > 0 {
		out = append(out, fmt.Sprintf("SILENT %dh", int(a.Silent.Hours())))
	}
	if a.OldFirmware {
		out = append(out, "OLD FIRMWARE")
	}
	return strings.Join(out, " · ")
}

// Thresholds come from the [admin] config section.
type Thresholds struct {
	BatteryVolts float64
	Stale        time.Duration
}

// Check flags a node; newest is the newest firmware seen on any node.
func Check(sess *Session, lastAdvert time.Time, newest string, th Thresholds, now time.Time) Alerts {
	var a Alerts
	if p, ok := sess.Latest(); ok && th.BatteryVolts > 0 && float64(p.Stats.BattMilliVolts)/1000 < th.BatteryVolts {
		a.LowBattery = true
	}
	if !lastAdvert.IsZero() && th.Stale > 0 && now.Sub(lastAdvert) > th.Stale {
		a.Silent = now.Sub(lastAdvert)
	}
	if sess.Owner != nil && newest != "" && CompareVersions(sess.Owner.FirmwareVersion, newest) < 0 {
		a.OldFirmware = true
	}
	return a
}

// Newest is the highest firmware version among the sessions.
func (s *Service) Newest() string {
	newest := ""
	for _, sess := range s.sessions {
		if sess.Owner != nil && CompareVersions(sess.Owner.FirmwareVersion, newest) > 0 {
			newest = sess.Owner.FirmwareVersion
		}
	}
	return newest
}

// CompareVersions orders firmware versions such as "v1.17.1"; anything unparseable sorts first.
func CompareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := range 3 {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "-")
	for i, p := range strings.SplitN(v, ".", 3) {
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{-1, -1, -1}
		}
		out[i] = n
	}
	return out
}
