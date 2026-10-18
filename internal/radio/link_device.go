package radio

import (
	"context"
	"math"
	"strings"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/companion"

	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

func (l *Link) Device() Device { return l }

// Settings re-reads the self info and the extras newer firmware adds; those that fail are left at zero.
func (l *Link) Settings(ctx context.Context) (Settings, error) {
	c := l.client
	self, err := c.AppStart(ctx, appVersion, appName)
	if err != nil {
		return Settings{}, err
	}
	s := Settings{
		Self: SelfInfo{Name: untrusted.Text(self.Name), PubKey: self.PublicKey, FreqKHz: self.RadioFrequency, BwHz: self.RadioBandwidth,
			SF: self.RadioSpreadFactor, CR: self.RadioCodingRate, TxPower: self.TxPower,
			Lat: float64(self.AdvertLatitude) / 1e6, Lon: float64(self.AdvertLongitude) / 1e6},
		MaxTxPower: int(self.MaxTxPower),
		Radio:      RadioParams{FreqKHz: self.RadioFrequency, BwHz: self.RadioBandwidth, SF: self.RadioSpreadFactor, CR: self.RadioCodingRate},
		Other: Other{ManualAdd: self.ManualAddContacts&1 != 0, ManualFlags: self.ManualAddContacts &^ 1, TelemetryBase: int(self.TelemetryModeBase), TelemetryLoc: int(self.TelemetryModeLoc),
			TelemetryEnv: int(self.TelemetryModeEnv), AdvertLocPolicy: int(self.AdvertLocPolicy), MultiAcks: int(self.MultiAcks)},
	}
	if dev, err := c.DeviceQuery(ctx); err == nil {
		s.Radio.Repeat, s.PathHashMode, s.Pin = dev.RepeatEnabled, int(dev.PathHashMode), dev.BLEPin
	}
	if t, err := c.GetTuningParams(ctx); err == nil {
		s.Tuning = Tuning{RxDelay: round(float64(t.RxDelayBase)), AirtimeFactor: round(float64(t.AirtimeFactor))}
	}
	if fr, err := c.GetAllowedRepeatFreq(ctx); err == nil {
		for _, r := range fr.Ranges {
			s.RepeatFreqs = append(s.RepeatFreqs, FreqRange{r.LowerFreq, r.UpperFreq})
		}
	}
	if sc, err := c.GetDefaultFloodScope(ctx); err == nil {
		s.Scope, s.HasScope = strings.TrimPrefix(sc.Name, "#"), true
	}
	return s, nil
}

// round drops float32 noise, so 1.2 doesn't show as 1.2000000476837158.
func round(f float64) float64 { return math.Round(f*1000) / 1000 }

func (l *Link) SetName(ctx context.Context, name string) error {
	return l.client.SetAdvertName(ctx, name)
}

func (l *Link) SetLocation(ctx context.Context, lat, lon float64) error {
	return l.client.SetAdvertLatLon(ctx, int32(math.Round(lat*1e6)), int32(math.Round(lon*1e6)))
}

func (l *Link) SendAdvert(ctx context.Context, flood bool) error {
	var t byte
	if flood {
		t = 1
	}
	return l.client.SendSelfAdvert(ctx, t)
}

func (l *Link) SetRadio(ctx context.Context, p RadioParams) error {
	return l.client.SetRadioParams(ctx, p.FreqKHz, p.BwHz, p.SF, p.CR, p.Repeat)
}

func (l *Link) SetTxPower(ctx context.Context, dbm int) error {
	return l.client.SetTxPower(ctx, byte(dbm))
}

func (l *Link) SetTuning(ctx context.Context, t Tuning) error {
	return l.client.SetTuningParams(ctx, float32(t.RxDelay), float32(t.AirtimeFactor))
}

func (l *Link) SetOther(ctx context.Context, o Other) error {
	manual := o.ManualFlags &^ 1
	if o.ManualAdd {
		manual |= 1
	}
	return l.client.SetOtherParamsFull(ctx, companion.SetOtherParamsCommand{ManualAddContacts: manual, HasExtended: true,
		TelemetryModeBase: byte(o.TelemetryBase), TelemetryModeLoc: byte(o.TelemetryLoc), TelemetryModeEnv: byte(o.TelemetryEnv),
		AdvertLocPolicy: byte(o.AdvertLocPolicy), MultiAcks: byte(o.MultiAcks)})
}

func (l *Link) SetPathHashMode(ctx context.Context, mode int) error {
	return l.client.SetPathHashMode(ctx, byte(mode))
}

func (l *Link) SetDefaultScope(ctx context.Context, region string) error {
	if region == "" {
		return l.client.ClearDefaultFloodScope(ctx)
	}
	r := meshcore.NewRegion(region)
	return l.client.SetDefaultFloodScope(ctx, r.Name, r.Key[:])
}

func (l *Link) SetPin(ctx context.Context, pin uint32) error { return l.client.SetDevicePin(ctx, pin) }

func (l *Link) ExportKey(ctx context.Context) ([64]byte, error) {
	k, err := l.client.ExportPrivateKey(ctx)
	return k.PrivateKey, err
}

func (l *Link) ImportKey(ctx context.Context, key [64]byte) error {
	return l.client.ImportPrivateKey(ctx, key)
}

// FactoryReset and Reboot get no reply: the radio goes away.
func (l *Link) FactoryReset(context.Context) error { return l.client.FactoryReset() }
func (l *Link) Reboot(context.Context) error       { return l.client.Reboot() }

func (l *Link) Clock(ctx context.Context) (time.Time, error) {
	t, err := l.client.GetDeviceTime(ctx)
	return time.Unix(int64(t.Timestamp), 0), err
}

func (l *Link) SyncClock(ctx context.Context) error { return l.client.SyncDeviceTime(ctx) }

func (l *Link) CustomVars(ctx context.Context) ([]Var, error) {
	r, err := l.client.GetCustomVars(ctx)
	if err != nil {
		return nil, err
	}
	return ParseVars(r.Vars), nil
}

// ParseVars reads the firmware's "name:value,name:value" list.
func ParseVars(s string) []Var {
	var out []Var
	for _, pair := range strings.Split(s, ",") {
		if name, value, ok := strings.Cut(pair, ":"); ok && name != "" {
			out = append(out, Var{name, value})
		}
	}
	return out
}

func (l *Link) SetCustomVar(ctx context.Context, name, value string) error {
	return l.client.SetCustomVar(ctx, name, value)
}

func (l *Link) Storage(ctx context.Context) (Storage, error) {
	b, err := l.client.GetBattAndStorage(ctx)
	return Storage{Battery: Battery{b.BatteryMilliVolts}, UsedKB: b.UsedStorageKB, TotalKB: b.TotalStorageKB}, err
}

// Stats reads the three GET_STATS sets.
func (l *Link) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	core, err := l.client.GetStats(ctx, companion.StatsTypeCore)
	if err != nil {
		return s, err
	}
	rad, err := l.client.GetStats(ctx, companion.StatsTypeRadio)
	if err != nil {
		return s, err
	}
	pkt, err := l.client.GetStats(ctx, companion.StatsTypePackets)
	if err != nil {
		return s, err
	}
	if c := core.Core; c != nil {
		s.Battery, s.Uptime, s.ErrFlags, s.Queue = Battery{c.BatteryMV}, time.Duration(c.UptimeSecs)*time.Second, c.ErrFlags, int(c.QueueLen)
	}
	if r := rad.Radio; r != nil {
		s.NoiseFloor, s.LastRSSI, s.LastSNR = int(r.NoiseFloor), int(r.LastRSSI), round(float64(r.LastSNR))
		s.TxAir, s.RxAir = time.Duration(r.TxAirSecs)*time.Second, time.Duration(r.RxAirSecs)*time.Second
	}
	if p := pkt.Packets; p != nil {
		s.Recv, s.Sent, s.SentFlood, s.SentDirect = p.PacketsRecv, p.PacketsSent, p.SentFlood, p.SentDirect
		s.RecvFlood, s.RecvDirect, s.RecvErrors = p.RecvFlood, p.RecvDirect, p.RecvErrors
	}
	return s, nil
}
