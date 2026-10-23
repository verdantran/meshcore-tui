// Package backup is a radio's settings, channels, contacts and, if chosen, private key, as a JSON file
// that can be restored to the same radio or another. A backup may come from someone else, so Parse checks
// every field before anything reaches a radio.
package backup

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

const (
	Format  = "meshcore-tui radio backup"
	Version = 1

	MaxSize     = 1 << 20 // far beyond a full radio's contacts and channels
	maxChannels = 64
	maxContacts = 2000
	maxName     = 31 // bytes, as the firmware keeps them
	maxPath     = 64
)

// Part is one thing a backup can hold, chosen when it's made and again when it's restored.
type Part int

const (
	PartIdentity Part = iota
	PartRadio
	PartOptions
	PartPin
	PartChannels
	PartContacts
	PartKey
)

var Parts = []Part{PartIdentity, PartRadio, PartOptions, PartPin, PartChannels, PartContacts, PartKey}

func (p Part) String() string {
	return [...]string{"Name and location", "Radio settings", "Options", "BLE PIN", "Channels", "Contacts", "Private key"}[p]
}

type Backup struct {
	Format     string    `json:"format"`
	Version    int       `json:"version"`
	App        string    `json:"app,omitempty"`
	Made       time.Time `json:"made"`
	RadioName  string    `json:"radio_name,omitempty"` // the radio it came from, for telling backups apart
	Identity   *Identity `json:"identity,omitempty"`
	Radio      *Radio    `json:"radio,omitempty"`
	Options    *Options  `json:"options,omitempty"`
	Pin        *uint32   `json:"ble_pin,omitempty"`
	Channels   []Channel `json:"channels,omitempty"`
	Contacts   []Contact `json:"contacts,omitempty"`
	PrivateKey string    `json:"private_key,omitempty"` // 128 hex characters
}

type Identity struct {
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
}

type Radio struct {
	FreqKHz       uint32  `json:"freq_khz"`
	BwHz          uint32  `json:"bw_hz"`
	SF            byte    `json:"sf"`
	CR            byte    `json:"cr"`
	Repeat        bool    `json:"repeat"`
	TxDBm         int     `json:"tx_dbm"`
	RxDelay       float64 `json:"rx_delay"`
	AirtimeFactor float64 `json:"airtime_factor"`
}

type Options struct {
	ManualAdd       bool    `json:"manual_add"`
	ManualFlags     byte    `json:"manual_flags"`
	TelemetryBase   int     `json:"telemetry_base"`
	TelemetryLoc    int     `json:"telemetry_location"`
	TelemetryEnv    int     `json:"telemetry_environment"`
	AdvertLocPolicy int     `json:"advert_location"`
	MultiAcks       int     `json:"multi_acks"`
	PathHashMode    int     `json:"path_hash_mode"`
	AutoAdd         AutoAdd `json:"auto_add"`
	FloodScope      *string `json:"flood_scope,omitempty"` // only from firmware that has one
}

type AutoAdd struct {
	Chat            bool `json:"chat"`
	Repeater        bool `json:"repeater"`
	Room            bool `json:"room"`
	Sensor          bool `json:"sensor"`
	OverwriteOldest bool `json:"overwrite_oldest"`
	MaxHops         int  `json:"max_hops"`
}

type Channel struct {
	Slot   int    `json:"slot"`
	Name   string `json:"name"`
	Secret string `json:"secret"` // 32 hex characters
}

type Contact struct {
	Key      string  `json:"key"` // 64 hex characters
	Name     string  `json:"name"`
	Type     string  `json:"type"` // chat, repeater, room or sensor
	Flags    byte    `json:"flags"`
	Route    *string `json:"route,omitempty"` // the stored direct route's hops in hex; absent floods
	HashSize int     `json:"hash_size,omitempty"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
}

// Has reports whether the backup holds a part.
func (b *Backup) Has(p Part) bool {
	switch p {
	case PartIdentity:
		return b.Identity != nil
	case PartRadio:
		return b.Radio != nil
	case PartOptions:
		return b.Options != nil
	case PartPin:
		return b.Pin != nil
	case PartChannels:
		return len(b.Channels) > 0
	case PartContacts:
		return len(b.Contacts) > 0
	case PartKey:
		return b.PrivateKey != ""
	}
	return false
}

// Write encodes the backup, indented so it can be read and checked by eye.
func (b *Backup) Write(w io.Writer) error {
	b.Format, b.Version = Format, Version
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(b)
}

// Parse reads and checks a backup. Names are cleaned as any name from outside is; anything out of range is
// refused, naming the field, rather than passed on to the radio.
func Parse(r io.Reader) (*Backup, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSize {
		return nil, errors.New("that file is too big to be a radio backup")
	}
	var b Backup
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("that file isn't a radio backup: %w", err)
	}
	if b.Format != Format {
		return nil, errors.New("that file isn't a meshcore-tui radio backup")
	}
	if b.Version < 1 || b.Version > Version {
		return nil, fmt.Errorf("that backup is version %d; this app reads up to %d, so it may need updating", b.Version, Version)
	}
	b.App, b.RadioName = untrusted.Line(clip(b.App, 64)), untrusted.Name(clip(b.RadioName, 64))
	if err := b.check(); err != nil {
		return nil, err
	}
	return &b, nil
}

func (b *Backup) check() error {
	if id := b.Identity; id != nil {
		id.Name = untrusted.Name(id.Name)
		if id.Name == "" || len(id.Name) > maxName {
			return fmt.Errorf("the radio's name must be 1 to %d bytes", maxName)
		}
		if !latLon(id.Lat, id.Lon) {
			return errors.New("the location isn't a place on Earth")
		}
	}
	if r := b.Radio; r != nil {
		switch {
		case r.FreqKHz < 137_000 || r.FreqKHz > 1_020_000:
			return errors.New("the frequency is outside what LoRa radios use")
		case r.BwHz < 7_800 || r.BwHz > 500_000:
			return errors.New("the bandwidth is out of range")
		case r.SF < 5 || r.SF > 12:
			return errors.New("the spreading factor must be 5 to 12")
		case r.CR < 5 || r.CR > 8:
			return errors.New("the coding rate must be 5 to 8")
		case r.TxDBm < 1 || r.TxDBm > 30:
			return errors.New("the TX power is out of range")
		case !finite(r.RxDelay) || r.RxDelay < 0 || r.RxDelay > 20:
			return errors.New("the receive delay is out of range")
		case !finite(r.AirtimeFactor) || r.AirtimeFactor < 0 || r.AirtimeFactor > 9:
			return errors.New("the airtime factor is out of range")
		}
	}
	if o := b.Options; o != nil {
		for _, v := range []int{o.TelemetryBase, o.TelemetryLoc, o.TelemetryEnv} {
			if v < radio.ShareNone || v > radio.ShareEveryone {
				return errors.New("a telemetry setting is out of range")
			}
		}
		switch {
		case o.AdvertLocPolicy < 0 || o.AdvertLocPolicy > 1:
			return errors.New("the advert location setting is out of range")
		case o.MultiAcks < 0 || o.MultiAcks > 2:
			return errors.New("the extra acks setting is out of range")
		case o.PathHashMode < 0 || o.PathHashMode > 2:
			return errors.New("the path hash size must be 1, 2 or 3 bytes")
		case o.AutoAdd.MaxHops < 0 || o.AutoAdd.MaxHops > 63:
			return errors.New("auto-add's hop limit is out of range")
		}
		if o.FloodScope != nil {
			s := untrusted.Line(*o.FloodScope)
			if len(s) > maxName {
				return errors.New("the flood scope's region name is too long")
			}
			o.FloodScope = &s
		}
	}
	if b.Pin != nil && *b.Pin != 0 && (*b.Pin < 100000 || *b.Pin > 999999) {
		return errors.New("a BLE PIN is 6 digits")
	}
	if len(b.Channels) > maxChannels {
		return errors.New("that backup has more channels than a radio holds")
	}
	for i := range b.Channels {
		c := &b.Channels[i]
		c.Name = untrusted.Name(c.Name)
		if _, err := c.secret(); err != nil || c.Name == "" || len(c.Name) > maxName || c.Slot < 0 || c.Slot >= maxChannels {
			return fmt.Errorf("channel %d in the backup isn't valid", i+1)
		}
	}
	if len(b.Contacts) > maxContacts {
		return errors.New("that backup has more contacts than a radio holds")
	}
	for i := range b.Contacts {
		c := &b.Contacts[i]
		c.Name = untrusted.Name(c.Name)
		if _, err := c.Radio(); err != nil {
			return fmt.Errorf("contact %d in the backup isn't valid: %v", i+1, err)
		}
	}
	if b.PrivateKey != "" {
		if _, err := b.Key(); err != nil {
			return err
		}
	}
	return nil
}

// Key is the private key, if the backup holds one.
func (b *Backup) Key() ([64]byte, error) {
	k, err := hex.DecodeString(b.PrivateKey)
	if err != nil || len(k) != 64 {
		return [64]byte{}, errors.New("the private key in the backup isn't 128 hex characters")
	}
	return [64]byte(k), nil
}

func (c Channel) secret() ([16]byte, error) {
	s, err := hex.DecodeString(c.Secret)
	if err != nil || len(s) != 16 {
		return [16]byte{}, errors.New("a channel secret is 32 hex characters")
	}
	return [16]byte(s), nil
}

// Radio is the channel as the radio takes it, in the slot it had.
func (c Channel) Radio() radio.Channel {
	s, _ := c.secret()
	return radio.Channel{Idx: c.Slot, Name: c.Name, Secret: s}
}

var typeNames = map[string]radio.ContactType{"chat": radio.TypeChat, "repeater": radio.TypeRepeater, "room": radio.TypeRoom, "sensor": radio.TypeSensor}

// Radio is the contact as the radio takes it.
func (c Contact) Radio() (radio.Contact, error) {
	key, err := hex.DecodeString(c.Key)
	if err != nil || len(key) != 32 {
		return radio.Contact{}, errors.New("its key isn't 64 hex characters")
	}
	t, ok := typeNames[c.Type]
	if !ok {
		return radio.Contact{}, fmt.Errorf("%q isn't a kind of node", c.Type)
	}
	if c.Name == "" || len(c.Name) > maxName {
		return radio.Contact{}, fmt.Errorf("its name must be 1 to %d bytes", maxName)
	}
	if !latLon(c.Lat, c.Lon) {
		return radio.Contact{}, errors.New("its location isn't a place on Earth")
	}
	rc := radio.Contact{Key: [32]byte(key), Name: c.Name, Type: t, Flags: c.Flags, PathLen: -1, Lat: c.Lat, Lon: c.Lon}
	if c.Route != nil {
		path, err := hex.DecodeString(*c.Route)
		size := max(c.HashSize, 1)
		if err != nil || len(path) > maxPath || size > 3 || len(path)%size != 0 {
			return radio.Contact{}, errors.New("its route isn't valid")
		}
		rc.Path, rc.HashSize, rc.PathLen = path, size, len(path)/size
	}
	return rc, nil
}

// FromChannel and FromContact turn what the radio holds into backup entries.
func FromChannel(c radio.Channel) Channel {
	return Channel{Slot: c.Idx, Name: c.Name, Secret: hex.EncodeToString(c.Secret[:])}
}

func FromContact(c radio.Contact) Contact {
	out := Contact{Key: hex.EncodeToString(c.Key[:]), Name: c.Name, Type: strings.ToLower(c.Type.String()), Flags: c.Flags, Lat: c.Lat, Lon: c.Lon}
	if c.PathLen >= 0 {
		size := max(c.HashSize, 1)
		route := hex.EncodeToString(c.Path[:min(len(c.Path), c.PathLen*size)])
		out.Route, out.HashSize = &route, size
	}
	return out
}

func latLon(lat, lon float64) bool {
	return finite(lat) && finite(lon) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// clip cuts s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	for len(s) > n {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}
