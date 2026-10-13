// Package config loads and saves ~/.config/meshcore-tui/config.toml.
package config

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

type Device struct {
	Name     string    `toml:"name"`
	Kind     string    `toml:"kind"` // USB, TCP
	Addr     string    `toml:"addr"`
	LastUsed time.Time `toml:"last_used"`
}

type Config struct {
	UI struct {
		Theme        string `toml:"theme"`
		Motion       string `toml:"motion"` // empty means the default: full, or reduced over SSH
		PacketTicker bool   `toml:"packet_ticker"`
	} `toml:"ui"`
	Connection struct {
		AutoConnect   string `toml:"auto_connect"` // a saved device's name
		TimeSyncDrift int    `toml:"time_sync_drift_s"`
	} `toml:"connection"`
	Messages struct {
		DMRetries     int  `toml:"dm_retries"`
		FloodFallback bool `toml:"flood_fallback"`
	} `toml:"messages"`
	Devices []Device `toml:"devices"`

	path string
}

func Default() *Config {
	c := &Config{}
	c.UI.Theme = "neon"
	c.UI.PacketTicker = true
	c.Connection.TimeSyncDrift = 5
	c.Messages.DMRetries = 3
	c.Messages.FloodFallback = true
	return c
}

// Dir follows XDG on every platform, so the path in the docs is the path on disk.
func Dir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "meshcore-tui")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "meshcore-tui")
}

func DataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "meshcore-tui")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "meshcore-tui")
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
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
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
