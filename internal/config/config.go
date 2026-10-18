// Package config loads and saves ~/.config/meshcore-tui/config.toml.
package config

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/BurntSushi/toml"
)

type Device struct {
	Name     string    `toml:"name"`
	Kind     string    `toml:"kind"` // USB, TCP
	Addr     string    `toml:"addr"`
	Mode     string    `toml:"mode,omitempty"` // "repeater" for a repeater's own console; empty is a companion
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
		SignChannels  []string `toml:"sign_channels"` // channels whose messages you send are signed (SIGNING.md)
	} `toml:"messages"`
	Notifications struct {
		Events  []string `toml:"events"` // dm, mention
		Bell    bool     `toml:"bell"`
		Desktop string   `toml:"desktop"` // auto, terminal, os, bell or off
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
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if _, err := toml.Decode(string(data), c); err != nil {
		return c, err
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
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	os.Remove(tmp) // WriteFile keeps an existing file's permissions
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Remember records a device after a successful connection, keeping the most recent first.
func (c *Config) Remember(d Device) {
	for i, o := range c.Devices {
		if o.Kind == d.Kind && o.Addr == d.Addr {
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
	for _, d := range c.Devices {
		if d.Name == name {
			return d, true
		}
	}
	return Device{}, false
}
