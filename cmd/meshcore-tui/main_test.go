package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.bug.st/serial/enumerator"

	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/repeaterserial"
	"meshcore-tui/internal/sim"
	"meshcore-tui/internal/ui"
)

func noEnv(string) string { return "" }

func env(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSetupDefaultsAndFlags(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	var errs bytes.Buffer
	opt, err := setup([]string{"--config", cfgPath}, noEnv, &errs)
	if err != nil {
		t.Fatal(err)
	}
	if opt.Theme.Name != "neon" || opt.Motion != motion.Full || opt.Start != nil || opt.Config == nil || opt.Dial == nil || opt.Ports == nil {
		t.Fatalf("defaults = %+v", opt)
	}

	for _, c := range []struct {
		args []string
		kind string
		addr string
	}{
		{[]string{"--serial", "/dev/cu.usbmodem1101"}, "USB", "/dev/cu.usbmodem1101"},
		{[]string{"--tcp", "10.0.0.4"}, "TCP", "10.0.0.4"},
		{[]string{"--demo"}, "SIM", "demo"},
		{[]string{"--demo", "--repeater"}, "SIM", "demo"},
	} {
		opt, err := setup(append(c.args, "--config", cfgPath), noEnv, &errs)
		repeater := strings.Contains(strings.Join(c.args, " "), "--repeater")
		if err != nil || opt.Start == nil || opt.Start.Kind != c.kind || opt.Start.Addr != c.addr || (opt.Start.Mode == ui.ModeRepeater) != repeater {
			t.Errorf("%v → %+v, %v", c.args, opt.Start, err)
		}
	}
}

func TestSetupConfigAndOverrides(t *testing.T) {
	path := writeConfig(t, `
[ui]
theme = "ice"
motion = "off"

[connection]
auto_connect = "Shed"

[[devices]]
name = "Shed"
kind = "TCP"
addr = "10.0.0.4:5000"
`)
	var errs bytes.Buffer
	opt, err := setup([]string{"--config", path}, noEnv, &errs)
	if err != nil {
		t.Fatal(err)
	}
	if opt.Theme.Name != "ice" || opt.Motion != motion.Off {
		t.Errorf("config not applied: theme %s, motion %s", opt.Theme.Name, opt.Motion)
	}
	if opt.Start == nil || opt.Start.Addr != "10.0.0.4:5000" || opt.Start.Saved != "Shed" {
		t.Errorf("auto_connect = %+v", opt.Start)
	}
	opt, _ = setup([]string{"--config", path, "--theme", "amber", "--motion", "reduced", "--demo"}, noEnv, &errs)
	if opt.Theme.Name != "amber" || opt.Motion != motion.Reduced || opt.Start.Kind != "SIM" {
		t.Errorf("flags should win: %s %s %+v", opt.Theme.Name, opt.Motion, opt.Start)
	}
}

func TestSetupEnvironment(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	var errs bytes.Buffer
	opt, _ := setup([]string{"--config", cfgPath}, env(map[string]string{"SSH_CONNECTION": "1 2 3 4", "NO_COLOR": "1"}), &errs)
	if opt.Motion != motion.Reduced || opt.Theme.Name != "mono" {
		t.Errorf("over SSH with NO_COLOR: motion %s, theme %s", opt.Motion, opt.Theme.Name)
	}
}

func TestOldWindowsConsole(t *testing.T) {
	none := env(nil)
	if !oldConsole("windows", none) || oldConsole("darwin", none) || oldConsole("windows", env(map[string]string{"WT_SESSION": "x"})) {
		t.Error("only the bare Windows console is flagged")
	}
	saved := goos
	t.Cleanup(func() { goos = saved })
	goos = "windows"
	var errs bytes.Buffer
	opt, _ := setup([]string{"--config", filepath.Join(t.TempDir(), "config.toml")}, none, &errs)
	if len(opt.Notes) != 1 || !strings.Contains(opt.Notes[0], "Windows Terminal") {
		t.Errorf("notes = %q", opt.Notes)
	}
}

func TestSetupErrors(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	var errs bytes.Buffer
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--theme", "nonsense"}, "unknown theme"},
		{[]string{"--motion", "lots"}, "unknown motion level"},
		{[]string{"--config", writeConfig(t, "not = [valid")}, "can't read"},
	} {
		_, err := setup(append([]string{"--config", cfgPath}, c.args...), noEnv, &errs)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v → %v, want %q", c.args, err, c.want)
		}
	}
	if _, err := setup([]string{"--no-such-flag"}, noEnv, &errs); err != errUsage {
		t.Errorf("unknown flag → %v", err)
	}
	if code := run([]string{"--no-such-flag"}, noEnv, &errs); code != 2 {
		t.Errorf("run with a bad flag = %d", code)
	}
	if code := run([]string{"--config", cfgPath, "--theme", "nonsense"}, noEnv, &errs); code != 2 {
		t.Errorf("run with a bad theme = %d", code)
	}
}

func TestSortPorts(t *testing.T) {
	got := sortPorts([]*enumerator.PortDetails{
		{Name: "/dev/cu.Bluetooth-Incoming-Port"},
		{Name: "/dev/cu.usbserial-1", IsUSB: true, VID: "0403", PID: "6001", Product: "FT232R"},
		{Name: "/dev/tty.usbmodem1101", IsUSB: true, VID: "303a", PID: "1001"},
		{Name: "/dev/cu.usbmodem1101", IsUSB: true, VID: "303a", PID: "1001"},
		{Name: "/dev/cu.usbserial-2", IsUSB: true, VID: "10c4", PID: "ea60", Product: " CP2102 "},
	})
	if len(got) != 3 {
		t.Fatalf("ports = %+v", got)
	}
	if got[0].Addr != "/dev/cu.usbmodem1101" || got[0].Detail != "Espressif (ESP32-S3, e.g. Heltec V4)  303A:1001" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Detail != "CP2102  10C4:EA60" || got[2].Addr != "/dev/cu.usbserial-1" {
		t.Errorf("order = %+v", got)
	}
}

func TestDial(t *testing.T) {
	if _, ok := dial(uiTarget("USB", "/dev/cu.x")).(*repeaterserial.USB); !ok {
		t.Error("USB should try a companion, then a repeater console")
	}
	if r := dial(uiTarget("TCP", "10.0.0.4")); r.Transport() != "TCP" {
		t.Error("TCP should dial a TCP link")
	}
	if _, ok := dial(uiTarget("SIM", "demo")).(*sim.Sim); !ok {
		t.Error("SIM should dial the simulator")
	}
	if r, ok := dial(ui.Target{Kind: "USB", Addr: "/dev/cu.x", Mode: ui.ModeRepeater}).(*repeaterserial.Repeater); !ok || r.Describe() != "/dev/cu.x · repeater console" {
		t.Error("USB in repeater mode opens the console directly")
	}
	r := dial(ui.Target{Kind: "SIM", Addr: "demo", Mode: ui.ModeRepeater})
	defer r.Close()
	r.Start(context.Background())
	for e := range r.Events() {
		if ready, ok := e.(radio.Ready); ok {
			if !ready.Console || ready.Self.Name != "Dundry" {
				t.Errorf("simulated repeater = %+v", ready)
			}
			break
		}
	}
}

func TestOpenStore(t *testing.T) {
	var errs bytes.Buffer
	db, err := openStore(filepath.Join(t.TempDir(), "h.db"), &errs)
	if err != nil || errs.Len() != 0 {
		t.Fatalf("normal open: %v %q", err, errs.String())
	}
	db.Close()
	// A path under a file can't be a database: fall back to memory and say so.
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o644)
	db, err = openStore(filepath.Join(file, "h.db"), &errs)
	if err != nil || !strings.Contains(errs.String(), "won't be kept") {
		t.Fatalf("fallback: %v %q", err, errs.String())
	}
	db.Close()
}

func uiTarget(kind, addr string) ui.Target { return ui.Target{Kind: kind, Addr: addr} }

func TestSetupKeys(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(filepath.Join(dir, "keys.toml"), []byte("[map]\nw = \"up\"\n"), 0o644)
	var errs bytes.Buffer
	opt, err := setup([]string{"--config", cfgPath}, env(nil), &errs)
	if err != nil || opt.Keys["map"]["w"] != "up" {
		t.Errorf("keys.toml beside the config: %v %v", opt.Keys, err)
	}
	os.WriteFile(filepath.Join(dir, "keys.toml"), []byte("[nowhere]\n"), 0o644)
	if _, err := setup([]string{"--config", cfgPath}, env(nil), &errs); err == nil || !strings.Contains(err.Error(), "keys.toml") {
		t.Errorf("a bad keys.toml stops startup and says which file: %v", err)
	}
}
