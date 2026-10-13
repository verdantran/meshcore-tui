// Command meshcore-tui is a terminal client for MeshCore companion radios.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"go.bug.st/serial/enumerator"

	"meshcore-tui/internal/config"
	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
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

func ports() []ui.Target {
	list, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil
	}
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

func dial(t ui.Target) radio.Radio {
	switch t.Kind {
	case "USB":
		return radio.NewSerial(t.Addr)
	case "TCP":
		return radio.NewTCP(t.Addr)
	}
	return sim.New(time.Now().UnixNano())
}

func main() {
	serialPort := flag.String("serial", "", "connect to a USB serial port, e.g. /dev/cu.usbmodem1101")
	tcp := flag.String("tcp", "", "connect to a WiFi companion at host[:port] (default port 5000)")
	demo := flag.Bool("demo", false, "run against a simulated radio")
	motionFlag := flag.String("motion", "", "full | reduced | off (default full, or reduced over SSH)")
	themeFlag := flag.String("theme", "", strings.Join(theme.Names(), " | "))
	configPath := flag.String("config", filepath.Join(config.Dir(), "config.toml"), "config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "can't read %s: %v\n", *configPath, err)
		os.Exit(2)
	}
	// Flags win over the config file only when they're given.
	themeName := cfg.UI.Theme
	if *themeFlag != "" {
		themeName = *themeFlag
	}
	level := motion.DefaultLevel()
	motionName := cfg.UI.Motion
	if *motionFlag != "" {
		motionName = *motionFlag
	}
	if motionName != "" {
		l, ok := motion.ParseLevel(motionName)
		if !ok {
			fmt.Fprintln(os.Stderr, "unknown motion level:", motionName)
			os.Exit(2)
		}
		level = l
	}
	dbPath := filepath.Join(config.DataDir(), "meshcore-tui.db")
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "can't open message history at %s (%v); this session's messages won't be kept\n", dbPath, err)
		if db, err = store.Open(":memory:"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	defer db.Close()
	opt := ui.Options{Theme: theme.Default(themeName), Motion: level, Dial: dial, Ports: ports, Config: cfg, Store: db}
	if d, ok := cfg.Device(cfg.Connection.AutoConnect); ok {
		opt.Start = &ui.Target{Kind: d.Kind, Addr: d.Addr, Saved: d.Name}
	}
	switch {
	case *serialPort != "":
		opt.Start = &ui.Target{Kind: "USB", Addr: *serialPort}
	case *tcp != "":
		opt.Start = &ui.Target{Kind: "TCP", Addr: *tcp}
	case *demo:
		opt.Start = &ui.Target{Kind: "SIM", Addr: "demo"}
	}

	if _, err := tea.NewProgram(ui.New(opt)).Run(); err != nil {
		db.Close()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
