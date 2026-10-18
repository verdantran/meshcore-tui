package sim

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

// devSettings is the simulated companion's own configuration, changed by the Device screen.
type devSettings struct {
	s    radio.Settings
	vars []radio.Var
	key  [64]byte
}

func newDevSettings() devSettings {
	self := device.self
	return devSettings{
		s: radio.Settings{Self: self, MaxTxPower: 22,
			Radio:        radio.RadioParams{FreqKHz: self.FreqKHz, BwHz: self.BwHz, SF: self.SF, CR: self.CR},
			Tuning:       radio.Tuning{RxDelay: 0, AirtimeFactor: 1},
			Other:        radio.Other{TelemetryBase: radio.SharePerContact, AdvertLocPolicy: radio.LocShare},
			Pin:          123456,
			RepeatFreqs:  []radio.FreqRange{{LowKHz: 433000, HighKHz: 433500}, {LowKHz: 869400, HighKHz: 869650}},
			Scope:        "bristol",
			HasScope:     true,
			PathHashMode: 0},
		vars: []radio.Var{{Name: "gps", Value: "0"}, {Name: "gps.interval", Value: "600"}},
		key:  [64]byte{0x5E, 0xC2, 0x7A},
	}
}

func (s *Sim) Device() radio.Device { return s }

func (s *Sim) edit(cmd byte, f func(*devSettings) error) error {
	s.frame(cmd, 0)
	time.Sleep(s.delay(60))
	s.mu.Lock()
	defer s.mu.Unlock()
	return f(&s.dev)
}

func (s *Sim) Settings(ctx context.Context) (radio.Settings, error) {
	s.frame(1, 5)
	time.Sleep(s.delay(80))
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.dev.s
	out.RepeatFreqs = append([]radio.FreqRange(nil), out.RepeatFreqs...)
	return out, nil
}

func (s *Sim) SetName(ctx context.Context, name string) error {
	return s.edit(8, func(d *devSettings) error { d.s.Self.Name = name; return nil })
}

func (s *Sim) SetLocation(ctx context.Context, lat, lon float64) error {
	return s.edit(14, func(d *devSettings) error { d.s.Self.Lat, d.s.Self.Lon = lat, lon; return nil })
}

func (s *Sim) SendAdvert(ctx context.Context, flood bool) error {
	s.frame(7, 0)
	s.emit(radio.Transmit{At: time.Now(), What: "SELF_ADVERT"})
	return nil
}

func (s *Sim) SetRadio(ctx context.Context, p radio.RadioParams) error {
	return s.edit(11, func(d *devSettings) error {
		if p.Repeat && !allowed(d.s.RepeatFreqs, p.FreqKHz) {
			return errors.New("repeat isn't allowed on that frequency")
		}
		d.s.Radio = p
		d.s.Self.FreqKHz, d.s.Self.BwHz, d.s.Self.SF, d.s.Self.CR = p.FreqKHz, p.BwHz, p.SF, p.CR
		return nil
	})
}

func allowed(rs []radio.FreqRange, khz uint32) bool {
	for _, r := range rs {
		if khz >= r.LowKHz && khz <= r.HighKHz {
			return true
		}
	}
	return false
}

func (s *Sim) SetTxPower(ctx context.Context, dbm int) error {
	return s.edit(12, func(d *devSettings) error {
		if dbm < 1 || dbm > d.s.MaxTxPower {
			return fmt.Errorf("power must be 1 to %d dBm", d.s.MaxTxPower)
		}
		d.s.Self.TxPower = byte(dbm)
		return nil
	})
}

func (s *Sim) SetTuning(ctx context.Context, t radio.Tuning) error {
	return s.edit(21, func(d *devSettings) error { d.s.Tuning = t; return nil })
}

func (s *Sim) SetOther(ctx context.Context, o radio.Other) error {
	return s.edit(38, func(d *devSettings) error { d.s.Other = o; return nil })
}

func (s *Sim) SetPathHashMode(ctx context.Context, mode int) error {
	return s.edit(61, func(d *devSettings) error { d.s.PathHashMode = mode; return nil })
}

func (s *Sim) SetDefaultScope(ctx context.Context, region string) error {
	return s.edit(63, func(d *devSettings) error { d.s.Scope = region; return nil })
}

func (s *Sim) SetPin(ctx context.Context, pin uint32) error {
	return s.edit(37, func(d *devSettings) error {
		if pin != 0 && (pin < 100000 || pin > 999999) {
			return errors.New("the PIN is six digits")
		}
		d.s.Pin = pin
		return nil
	})
}

func (s *Sim) ExportKey(ctx context.Context) ([64]byte, error) {
	s.frame(23, 0)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dev.key, nil
}

func (s *Sim) ImportKey(ctx context.Context, key [64]byte) error {
	return s.edit(24, func(d *devSettings) error { d.key = key; return nil })
}

func (s *Sim) FactoryReset(ctx context.Context) error {
	s.frame(51, 0)
	s.mu.Lock()
	s.dev = newDevSettings()
	s.mu.Unlock()
	go s.bounce()
	return nil
}

func (s *Sim) Reboot(ctx context.Context) error {
	s.frame(19, 0)
	go s.bounce()
	return nil
}

// bounce drops the link and brings it back, as a rebooting radio does.
func (s *Sim) bounce() {
	time.Sleep(s.delay(200))
	s.emit(radio.Lost{Err: errors.New("the radio is restarting")})
	time.Sleep(s.delay(3000))
	s.emit(radio.Restored{})
}

func (s *Sim) Clock(ctx context.Context) (time.Time, error) {
	s.frame(5, 0)
	return time.Now().Add(-device.drift).Truncate(time.Second), nil
}

func (s *Sim) SyncClock(ctx context.Context) error {
	s.frame(6, 0)
	return nil
}

func (s *Sim) CustomVars(ctx context.Context) ([]radio.Var, error) {
	s.frame(40, 0)
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]radio.Var(nil), s.dev.vars...), nil
}

func (s *Sim) SetCustomVar(ctx context.Context, name, value string) error {
	return s.edit(41, func(d *devSettings) error {
		for i, v := range d.vars {
			if v.Name == name {
				d.vars[i].Value = value
				return nil
			}
		}
		return fmt.Errorf("no custom var %q", strings.TrimSpace(name))
	})
}

func (s *Sim) Storage(ctx context.Context) (radio.Storage, error) {
	s.frame(20, 0)
	s.mu.Lock()
	defer s.mu.Unlock()
	return radio.Storage{Battery: radio.Battery{MilliVolts: s.mv}, UsedKB: 412, TotalKB: 1536}, nil
}

// Stats grows with the time the simulator has been running, so sparklines have something to show.
func (s *Sim) Stats(ctx context.Context) (radio.Stats, error) {
	s.frame(56, 0)
	s.mu.Lock()
	defer s.mu.Unlock()
	up := time.Since(s.born) + 3*time.Hour
	secs := uint32(up.Seconds())
	recv := secs/4 + uint32(s.rng.Intn(3))
	return radio.Stats{Battery: radio.Battery{MilliVolts: s.mv}, Uptime: up.Truncate(time.Second), Queue: s.rng.Intn(2),
		NoiseFloor: -114 + s.rng.Intn(6), LastRSSI: -95 + s.rng.Intn(20), LastSNR: float64(s.rng.Intn(48)-12) / 4,
		TxAir: time.Duration(secs/90) * time.Second, RxAir: time.Duration(secs/11) * time.Second,
		Recv: recv, Sent: recv / 20, SentFlood: recv / 30, SentDirect: recv / 60, RecvFlood: recv * 3 / 4, RecvDirect: recv / 4,
		RecvErrors: secs / 900}, nil
}
