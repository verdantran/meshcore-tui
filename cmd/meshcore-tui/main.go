// Command meshcore-tui is a terminal client for MeshCore companion radios.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"go.bug.st/serial/enumerator"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/config"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/repeaterserial"
	"github.com/verdantran/meshcore-tui/internal/sim"
	"github.com/verdantran/meshcore-tui/internal/store"
	"github.com/verdantran/meshcore-tui/internal/theme"
	"github.com/verdantran/meshcore-tui/internal/ui"
)

// likelyRadios are USB vendor IDs of boards that run MeshCore companion firmware.
var likelyRadios = map[string]string{
	"303A": "Espressif (ESP32-S3, e.g. Heltec V4)",
	"10C4": "Silicon Labs CP210x",
	"1A86": "WCH CH340/CH9102",
	"239A": "Adafruit nRF52",
	"2886": "Seeed nRF52",
}

// sortPorts lists USB serial ports with likely radios first.
func sortPorts(list []*enumerator.PortDetails) []ui.Target {
	var likely, other []ui.Target
	for _, p := range list {
		if !p.IsUSB || strings.Contains(p.Name, "/dev/tty.") {
			continue // on macOS the cu.* twin is the one to open
		}
		vid := strings.ToUpper(p.VID)
		detail := strings.TrimSpace(p.Product)
		if name, ok := likelyRadios[vid]; ok {
			if detail == "" {
				detail = name
			}
			likely = append(likely, ui.Target{Kind: "USB", Addr: p.Name, Detail: detail + "  " + vid + ":" + strings.ToUpper(p.PID)})
			continue
		}
		other = append(other, ui.Target{Kind: "USB", Addr: p.Name, Detail: detail})
	}
	return append(likely, other...)
}

func ports() []ui.Target {
	list, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil
	}
	return sortPorts(list)
}

func scanBLE(ctx context.Context, found func(ui.Target)) error {
	return radio.ScanBLE(ctx, func(d radio.BLEDevice) {
		found(ui.Target{Kind: "BLE", Addr: d.Addr, Detail: fmt.Sprintf("%s · %d dBm", d.Name, d.RSSI)})
	})
}

func dial(t ui.Target) radio.Radio {
	switch {
	case t.Kind == "USB" && t.Mode == ui.ModeRepeater:
		return repeaterserial.Dial(t.Addr)
	case t.Kind == "USB":
		return repeaterserial.NewUSB(t.Addr)
	case t.Kind == "TCP":
		return radio.NewTCP(t.Addr)
	case t.Kind == "BLE":
		return radio.NewBLE(t.Addr)
	case t.Mode == ui.ModeRepeater:
		return simRepeater()
	}
	return sim.New(time.Now().UnixNano())
}

// simRepeater is a simulated repeater on its USB console.
func simRepeater() radio.Radio {
	s := sim.New(time.Now().UnixNano())
	return repeaterserial.NewRepeater("demo", nil, func() (io.ReadWriteCloser, error) { return s.Console("Dundry"), nil })
}

var errUsage = errors.New("usage")

// setup turns the command line, config file and environment into the app's options.
// Flags win over the config file only when they're given.
func setup(args []string, env func(string) string, stderr io.Writer) (ui.Options, error) {
	fs := flag.NewFlagSet("meshcore-tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	serialPort := fs.String("serial", "", "connect to a USB serial port, e.g. /dev/cu.usbmodem1101, /dev/ttyACM0 or COM3")
	tcp := fs.String("tcp", "", "connect to a WiFi companion at host[:port] (default port 5000)")
	ble := fs.String("ble", "", "connect over Bluetooth to a radio's address, as the connect screen's b scan shows it")
	demo := fs.Bool("demo", false, "run against a simulated radio")
	asciiOnly := fs.Bool("ascii", false, "draw in plain ASCII, for terminals without braille or box drawing")
	repeater := fs.Bool("repeater", false, "with -serial or -demo, open a repeater's own console instead of a companion radio")
	motionFlag := fs.String("motion", "", "full | reduced | off (default full, or reduced over SSH)")
	themeFlag := fs.String("theme", "", strings.Join(theme.Names(), " | "))
	configPath := fs.String("config", filepath.Join(config.Dir(), "config.toml"), "config file")
	if err := fs.Parse(args); err != nil {
		return ui.Options{}, errUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return ui.Options{}, fmt.Errorf("can't read %s: %w", *configPath, err)
	}
	keysPath := filepath.Join(filepath.Dir(*configPath), "keys.toml")
	keys, err := config.LoadKeys(keysPath)
	if err != nil {
		return ui.Options{}, fmt.Errorf("can't read %s: %w", keysPath, err)
	}
	themeName := cfg.UI.Theme
	if *themeFlag != "" {
		themeName = *themeFlag
	}
	if !slices.Contains(theme.Names(), themeName) {
		return ui.Options{}, fmt.Errorf("unknown theme %q; choose from %s", themeName, strings.Join(theme.Names(), ", "))
	}
	th := theme.ByName(themeName)
	if env("NO_COLOR") != "" {
		th = theme.Mono
	}

	level := motion.Full
	if env("SSH_CONNECTION") != "" {
		level = motion.Reduced
	}
	motionName := cfg.UI.Motion
	if *motionFlag != "" {
		motionName = *motionFlag
	}
	chosen := motionName != ""
	if chosen {
		l, ok := motion.ParseLevel(motionName)
		if !ok {
			return ui.Options{}, fmt.Errorf("unknown motion level %q; choose full, reduced or off", motionName)
		}
		level = l
	}

	var notes []string
	if oldConsole(goos, env) {
		notes = append(notes, "CONSOLE  this looks like the old Windows console, which may not draw the map's braille, some symbols or the mouse. Windows Terminal, free from Microsoft, shows everything; --ascii draws in plain ASCII instead.")
	}
	opt := ui.Options{Keys: keys, ASCII: *asciiOnly || cfg.UI.ASCII, Notes: notes, Theme: th, Motion: level, Dial: dial, Ports: ports, Scan: scanBLE, Config: cfg, MotionChosen: chosen, Keychain: admin.SystemOr(passwordFile(*configPath))}
	if d, ok := cfg.Device(cfg.Connection.AutoConnect); ok {
		opt.Start = &ui.Target{Kind: d.Kind, Addr: d.Addr, Saved: d.Name, Mode: d.Mode}
	}
	mode := ""
	if *repeater {
		mode = ui.ModeRepeater
	}
	switch {
	case *serialPort != "":
		opt.Start = &ui.Target{Kind: "USB", Addr: *serialPort, Mode: mode}
	case *tcp != "":
		opt.Start = &ui.Target{Kind: "TCP", Addr: *tcp}
	case *ble != "":
		opt.Start = &ui.Target{Kind: "BLE", Addr: *ble}
	case *demo:
		opt.Start = &ui.Target{Kind: "SIM", Addr: "demo", Mode: mode}
	}
	return opt, nil
}

// openStore opens the message history, falling back to memory (with a warning) if the file can't be used.
func openStore(path string, stderr io.Writer) (*store.Store, error) {
	db, err := store.Open(path)
	if err == nil {
		return db, nil
	}
	fmt.Fprintf(stderr, "can't open message history at %s (%v); this session's messages won't be kept\n", path, err)
	return store.Open(":memory:")
}

func run(args []string, env func(string) string, stderr io.Writer) int {
	opt, err := setup(args, env, stderr)
	if errors.Is(err, errUsage) {
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	db, err := openStore(filepath.Join(config.DataDir(), "meshcore-tui.db"), stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	opt.Store = db
	out := &timedOutput{File: os.Stdout}
	p := tea.NewProgram(ui.New(opt), tea.WithOutput(out))
	out.slow = func() { p.Send(ui.SlowTerminalMsg{}) }
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

var goos = runtime.GOOS

// passwordFile is where node passwords go without a system keychain: beside the config in use.
func passwordFile(configPath string) *admin.FileKeychain {
	return &admin.FileKeychain{Path: filepath.Join(filepath.Dir(configPath), "passwords.json")}
}

// oldConsole reports the Windows console host, which sets none of the variables newer terminals do.
func oldConsole(goos string, env func(string) string) bool {
	if goos != "windows" {
		return false
	}
	for _, k := range []string{"WT_SESSION", "TERM_PROGRAM", "TERM", "ALACRITTY_WINDOW_ID", "ConEmuANSI", "SSH_CONNECTION"} {
		if env(k) != "" {
			return false
		}
	}
	return true
}

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stderr)) }
