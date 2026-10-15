package radio

import (
	"context"
	"time"
)

// Device is the companion radio's own configuration and counters. A repeater's serial console has none.
type Device interface {
	Settings(ctx context.Context) (Settings, error)
	SetName(ctx context.Context, name string) error
	SetLocation(ctx context.Context, lat, lon float64) error
	SendAdvert(ctx context.Context, flood bool) error
	SetRadio(ctx context.Context, p RadioParams) error
	SetTxPower(ctx context.Context, dbm int) error
	SetTuning(ctx context.Context, t Tuning) error
	SetOther(ctx context.Context, o Other) error
	SetPathHashMode(ctx context.Context, mode int) error
	SetDefaultScope(ctx context.Context, region string) error // "" floods everywhere
	SetPin(ctx context.Context, pin uint32) error
	ExportKey(ctx context.Context) ([64]byte, error)
	ImportKey(ctx context.Context, key [64]byte) error
	FactoryReset(ctx context.Context) error
	Reboot(ctx context.Context) error
	Clock(ctx context.Context) (time.Time, error)
	SyncClock(ctx context.Context) error
	CustomVars(ctx context.Context) ([]Var, error)
	SetCustomVar(ctx context.Context, name, value string) error
	Storage(ctx context.Context) (Storage, error)
	Stats(ctx context.Context) (Stats, error)
}

type RadioParams struct {
	FreqKHz uint32
	BwHz    uint32
	SF, CR  byte
	Repeat  bool // client repeat, only on frequencies the firmware allows
}

type Tuning struct {
	RxDelay       float64 // the base of the weak-signal receive delay
	AirtimeFactor float64
}

// Telemetry sharing modes for the base, location and environment readings.
const (
	ShareNone       = iota
	SharePerContact // only contacts given the permission
	ShareEveryone
)

// Advert location policies.
const (
	LocNone  = 0
	LocShare = 1
)

type Other struct {
	ManualAdd       bool // only add contacts by hand
	ManualFlags     byte // the rest of the firmware byte ManualAdd lives in, kept as it was
	TelemetryBase   int
	TelemetryLoc    int
	TelemetryEnv    int
	AdvertLocPolicy int
	MultiAcks       int
}

type FreqRange struct{ LowKHz, HighKHz uint32 }

// Settings is everything the Device screen shows, read in one go.
type Settings struct {
	Self         SelfInfo
	MaxTxPower   int
	Radio        RadioParams
	Tuning       Tuning
	Other        Other
	PathHashMode int
	Pin          uint32
	RepeatFreqs  []FreqRange // where client repeat is allowed
	Scope        string      // the default flood scope's region; "" for none
	HasScope     bool        // the firmware supports a default scope
}

type Var struct{ Name, Value string }

type Storage struct {
	Battery         Battery
	UsedKB, TotalKB uint32
}

type Stats struct {
	Battery    Battery
	Uptime     time.Duration
	ErrFlags   uint16
	Queue      int
	NoiseFloor int
	LastRSSI   int
	LastSNR    float64
	TxAir      time.Duration
	RxAir      time.Duration
	Recv, Sent uint32
	SentFlood  uint32
	SentDirect uint32
	RecvFlood  uint32
	RecvDirect uint32
	RecvErrors uint32
}
