// Command meshcore-tui is a terminal client for MeshCore companion radios.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"go.bug.st/serial/enumerator"

	"meshcore-tui/internal/admin"
	"meshcore-tui/internal/config"
	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/repeaterserial"
	"meshcore-tui/internal/sim"
	"meshcore-tui/internal/store"
	"meshcore-tui/internal/theme"
	"meshcore-tui/internal/ui"
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

func dial(t ui.Target) radio.Radio {
	switch {
	case t.Kind == "USB" && t.Mode == ui.ModeRepeater:
		return repeaterserial.Dial(t.Addr)
	case t.Kind == "USB":
		return repeaterserial.NewUSB(t.Addr)
	case t.Kind == "TCP":
		return radio.NewTCP(t.Addr)
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
	serialPort := fs.String("serial", "", "connect to a USB serial port, e.g. /dev/cu.usbmodem1101")
	tcp := fs.String("tcp", "", "connect to a WiFi companion at host[:port] (default port 5000)")
	demo := fs.Bool("demo", false, "run against a simulated radio")
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
	if motionName != "" {
		l, ok := motion.ParseLevel(motionName)
		if !ok {
			return ui.Options{}, fmt.Errorf("unknown motion level %q; choose full, reduced or off", motionName)
		}
		level = l
	}

	opt := ui.Options{Theme: th, Motion: level, Dial: dial, Ports: ports, Config: cfg, Keychain: admin.SystemOr(&admin.FileKeychain{Path: filepath.Join(config.Dir(), "passwords.json")})}
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
	if _, err := tea.NewProgram(ui.New(opt)).Run(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stderr)) }
