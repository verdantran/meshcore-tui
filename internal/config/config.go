// Package config loads and saves ~/.config/meshcore-tui/config.toml.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/verdantran/meshcore-tui/internal/privfile"
)

type Device struct {
	Name     string    `toml:"name"`
	Kind     string    `toml:"kind"` // USB, TCP
	Addr     string    `toml:"addr"`
	Mode     string    `toml:"mode,omitempty"` // "repeater" for a repeater's own console; empty is a companion
	Key      string    `toml:"key,omitempty"`  // the radio's public key, once it has proved it holds it
	LastUsed time.Time `toml:"last_used"`
}

type Config struct {
	UI struct {
		Theme        string `toml:"theme"`
		Motion       string `toml:"motion"` // empty means the default: full, or reduced over SSH
		PacketTicker bool   `toml:"packet_ticker"`
		Beacon       bool   `toml:"beacon"` // the rings pulsing around us on the map at full motion
		Mouse        bool   `toml:"mouse"`  // clicks pick screens and panes; off leaves the mouse to the terminal, for selecting text
		Layout       string `toml:"layout"` // split panes, e.g. "right 50 (*chats, packets)"; empty is one screen
		ASCII        bool   `toml:"ascii"`  // plain ASCII for terminals without braille or box drawing
	} `toml:"ui"`
	Connection struct {
		AutoConnect   string `toml:"auto_connect"` // a saved device's name
		TimeSyncDrift int    `toml:"time_sync_drift_s"`
	} `toml:"connection"`
	Messages struct {
		DMRetries     int      `toml:"dm_retries"`
		FloodFallback bool     `toml:"flood_fallback"`
		SignIDs       []string `toml:"sign_channel_ids"`        // channels whose messages you send are signed (SIGNING.md), by ChannelID
		SignChannels  []string `toml:"sign_channels,omitempty"` // the same by name, from before; moved to SignIDs once the radio's channels are known
	} `toml:"messages"`
	Notifications struct {
		Events  []string `toml:"events"` // dm, mention
		Bell    bool     `toml:"bell"`
		Desktop string   `toml:"desktop"` // auto, terminal, os, bell or off
		Show    string   `toml:"show"`    // what reaches the desktop and lock screen: message, sender or nothing
	} `toml:"notifications"`
	Admin struct {
		StatusPoll   int      `toml:"status_poll_s"` // the auto-refresh interval on a node's status
		FleetSpacing int      `toml:"fleet_poll_spacing_s"`
		BatteryAlert float64  `toml:"battery_alert_v"`
		StaleAfter   int      `toml:"stale_after_h"` // flag a node not heard from in this long
		Watch        []string `toml:"watch"`         // public keys (hex) of nodes whose status is polled and kept
		WatchEvery   int      `toml:"watch_poll_m"`  // minutes between polls of a watched node over the air
		LocalEvery   int      `toml:"watch_local_s"` // seconds between polls of a node on USB
	} `toml:"admin"`
	Devices   []Device              `toml:"devices"`
	Batteries map[string]string     `toml:"batteries"` // battery chemistry by public key (hex), for its charge curve
	Locations map[string][2]float64 `toml:"locations"` // latitude, longitude set by hand, by public key (hex)

	path string
}

func Default() *Config {
	c := &Config{}
	c.UI.Theme = "neon"
	c.UI.PacketTicker = true
	c.UI.Beacon = true
	c.UI.Mouse = true
	c.Connection.TimeSyncDrift = 5
	c.Messages.DMRetries = 3
	c.Messages.FloodFallback = true
	c.Notifications.Events = []string{"dm", "mention"}
	c.Notifications.Bell = true
	c.Notifications.Desktop = "auto"
	c.Notifications.Show = "message"
	c.Admin.StatusPoll = 60
	c.Admin.FleetSpacing = 20
	c.Admin.BatteryAlert = 3.5
	c.Admin.StaleAfter = 24
	c.Admin.WatchEvery = 10
	c.Admin.LocalEvery = 60
	return c
}

var goos = runtime.GOOS

// Dir follows XDG on macOS and Linux, so the path in the docs is the path on disk. Windows keeps
// settings in AppData\Roaming and data in AppData\Local, as its own apps do; XDG still overrides both.
func Dir() string {
	return dir("XDG_CONFIG_HOME", "APPDATA", ".config")
}

func DataDir() string {
	return dir("XDG_DATA_HOME", "LOCALAPPDATA", filepath.Join(".local", "share"))
}

func dir(xdg, windows, underHome string) string {
	if d := os.Getenv(xdg); d != "" {
		return filepath.Join(d, "meshcore-tui")
	}
	if d := os.Getenv(windows); goos == "windows" && d != "" {
		return filepath.Join(d, "meshcore-tui")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, underHome, "meshcore-tui")
}

// Load reads the config at path, or returns defaults if there isn't one yet.
func Load(path string) (*Config, error) {
	c := Default()
	c.path = path
	data, err := privfile.Read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if _, err := toml.Decode(string(data), c); err != nil {
		return c, err
	}
	for k, p := range c.Locations {
		if !(math.Abs(p[0]) <= 90 && math.Abs(p[1]) <= 180) { // TOML has inf and nan
			delete(c.Locations, k)
		}
	}
	return c, nil
}

func (c *Config) Path() string { return c.path }

// Save writes atomically, so a crash mid-write never leaves a broken config.
func (c *Config) Save() error {
	if c.path == "" {
		return nil
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	return privfile.Write(c.path, buf.Bytes())
}

// Remember records a device after a successful connection, keeping the most recent first.
func (c *Config) Remember(d Device) {
	for i, o := range c.Devices {
		if o.Kind == d.Kind && o.Addr == d.Addr {
			if d.Key == "" {
				d.Key = o.Key
			}
			c.Devices = append(c.Devices[:i], c.Devices[i+1:]...)
			break
		}
	}
	c.Devices = append([]Device{d}, c.Devices...)
}

// Watching reports whether a node's status is polled and kept.
func (c *Config) Watching(key string) bool { return slices.Contains(c.Admin.Watch, key) }

// SetWatch adds or removes a node from the watch list.
func (c *Config) SetWatch(key string, on bool) {
	c.Admin.Watch = slices.DeleteFunc(c.Admin.Watch, func(k string) bool { return k == key })
	if on {
		c.Admin.Watch = append(c.Admin.Watch, key)
	}
}

// ChannelID names a channel by its secret, without giving the secret away: a name or slot can be reused by
// another channel.
func ChannelID(secret [16]byte) string {
	h := sha256.Sum256(append([]byte("meshcore-tui channel\x00"), secret[:]...))
	return hex.EncodeToString(h[:8])
}

// Signs reports whether your messages on the channel with this secret are signed.
func (c *Config) Signs(secret [16]byte) bool {
	return slices.Contains(c.Messages.SignIDs, ChannelID(secret))
}

func (c *Config) SetSigning(secret [16]byte, on bool) {
	id := ChannelID(secret)
	c.Messages.SignIDs = slices.DeleteFunc(c.Messages.SignIDs, func(s string) bool { return s == id })
	if on {
		c.Messages.SignIDs = append(c.Messages.SignIDs, id)
	}
}

// MoveSigning turns the channel names signing was kept by into IDs, for the channels named in secrets, and
// drops the rest.
func (c *Config) MoveSigning(secrets map[string][16]byte) {
	for _, name := range c.Messages.SignChannels {
		if s, ok := secrets[name]; ok && !c.Signs(s) {
			c.SetSigning(s, true)
		}
	}
	c.Messages.SignChannels = nil
}

// SetBattery records a node's battery chemistry; li-ion, the default, isn't stored.
func (c *Config) SetBattery(key, chemistry string) {
	if chemistry == "li-ion" {
		delete(c.Batteries, key)
		return
	}
	if c.Batteries == nil {
		c.Batteries = map[string]string{}
	}
	c.Batteries[key] = chemistry
}

// SetLocation places a node by hand; ok false takes it away again.
func (c *Config) SetLocation(key string, lat, lon float64, ok bool) {
	if !ok {
		delete(c.Locations, key)
		return
	}
	if c.Locations == nil {
		c.Locations = map[string][2]float64{}
	}
	c.Locations[key] = [2]float64{lat, lon}
}

func (c *Config) Forget(kind, addr string) {
	for i, o := range c.Devices {
		if o.Kind == kind && o.Addr == addr {
			c.Devices = append(c.Devices[:i], c.Devices[i+1:]...)
			return
		}
	}
}

func (c *Config) Device(name string) (Device, bool) {
	var found []Device
	for _, d := range c.Devices {
		if d.Name == name {
			found = append(found, d)
		}
	}
	if len(found) != 1 {
		return Device{}, false // a radio names itself, so two saved under one name can't be told apart
	}
	return found[0], true
}
