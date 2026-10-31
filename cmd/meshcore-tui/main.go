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
	"runtime/debug"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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
	"github.com/verdantran/meshcore-tui/internal/untrusted"
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
		detail := strings.TrimSpace(untrusted.Line(p.Product))
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
		found(ui.Target{Kind: "BLE", Addr: d.Addr, Detail: fmt.Sprintf("%s · %d dBm", untrusted.Line(d.Name), d.RSSI)})
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

var (
	errUsage   = errors.New("usage")
	errVersion = errors.New("version")
	errWidths  = errors.New("widths")
)

// stdout is where --version prints; tests replace it.
var stdout io.Writer = os.Stdout

// version is the module version a release was installed at, or for a build from a checkout the commit it
// was built from, which Go records in every binary.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown version"
	}
	v := info.Main.Version
	var rev, dirty string
	for _, s := range info.Settings {
		switch {
		case s.Key == "vcs.revision" && len(s.Value) >= 12:
			rev = s.Value[:12]
		case s.Key == "vcs.modified" && s.Value == "true":
			dirty = ", with local changes"
		}
	}
	// Go stamps a checkout build with a made-up version holding the commit; the commit alone reads better.
	if rev != "" && (v == "" || v == "(devel)" || strings.Contains(v, rev) || strings.HasSuffix(v, "+dirty")) {
		return rev + dirty
	}
	return v
}

// setup turns the command line, config file and environment into the app's options.
// Flags win over the config file only when they're given.
func setup(args []string, env func(string) string, sess session, stderr io.Writer) (ui.Options, error) {
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
	showVersion := fs.Bool("version", false, "print the version and exit")
	showWidths := fs.Bool("widths", false, "measure how the terminal draws emoji and flags, say what the app does about it, and exit")
	if err := fs.Parse(args); err != nil {
		return ui.Options{}, errUsage
	}
	if *showVersion {
		return ui.Options{}, errVersion
	}
	if *showWidths {
		cfg, err := config.Load(*configPath)
		if err != nil {
			cfg = config.Default()
		}
		return ui.Options{Config: cfg}, errWidths
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
	if sess.remote {
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
	opt := ui.Options{Version: version(), Keys: keys, ASCII: *asciiOnly || cfg.UI.ASCII, Notes: notes, Theme: th, Motion: level, Dial: dial, Ports: ports, Scan: scanBLE, Config: cfg, MotionChosen: chosen, Remote: sess.remote, Keychain: admin.SystemOr(passwordFile(*configPath))}
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
	sess := detectSession(env, runTmux, parentOf)
	opt, err := setup(args, env, sess, stderr)
	if errors.Is(err, errUsage) {
		return 2
	}
	if errors.Is(err, errVersion) {
		fmt.Fprintln(stdout, "meshcore-tui "+version())
		return 0
	}
	if errors.Is(err, errWidths) {
		widthReport(stdout, sess, terminalWidths(), opt.Config.UI.Flags)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, untrusted.Line(err.Error())) // a parse error can quote a handed file's text
		return 2
	}
	db, err := openStore(filepath.Join(config.DataDir(), "meshcore-tui.db"), stderr)
	if err != nil {
		fmt.Fprintln(stderr, untrusted.Line(err.Error()))
		return 1
	}
	defer db.Close()
	opt.Store = db
	widths := terminalWidths()
	opt.SplitEmoji, opt.LoneLetter, opt.Flag = widths.mode == widthsSplit, widths.loneLetter, widths.flag
	opt.FlagLetters = flagLetters(opt.Config.UI.Flags, sess, widths)
	opt.TextEmoji = widths.textEmoji
	out := &timedOutput{File: os.Stdout}
	p := tea.NewProgram(ui.New(opt), tea.WithOutput(out), tea.WithFPS(motion.FrameRate), tea.WithEnvironment(teaEnv(os.Environ(), sess))) // the app never animates faster
	out.slow = func() { p.Send(ui.SlowTerminalMsg{}) }
	if widths.mode == widthsGraphemes {
		go p.Send(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeSet}) // as the answer many terminals, tmux among them, don't give
	}
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
