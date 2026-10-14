package notify

import (
	"errors"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDetect(t *testing.T) {
	cases := []struct {
		vars map[string]string
		want Method
	}{
		{map[string]string{"TERM_PROGRAM": "ghostty"}, Terminal},
		{map[string]string{"TERM": "xterm-kitty"}, Terminal},
		{map[string]string{"TERM_PROGRAM": "ghostty", "SSH_CONNECTION": "1 2 3 4"}, Terminal},
		{map[string]string{"TERM": "xterm-256color", "SSH_CONNECTION": "1 2 3 4"}, BellOnly},
	}
	for _, c := range cases {
		if got := Detect(env(c.vars)); got != c.want {
			t.Errorf("Detect(%v) = %s, want %s", c.vars, got, c.want)
		}
	}
	if got := Resolve("off", nil); got != Off {
		t.Errorf("Resolve(off) = %s", got)
	}
}

func TestEscape(t *testing.T) {
	ghostty := env(map[string]string{"TERM_PROGRAM": "ghostty"})
	if got := Escape(Terminal, "Alice", "hi\x1b]evil", true, ghostty); got != "\a\x1b]9;Alice: hi ]evil\a" {
		t.Errorf("OSC 9 = %q", got)
	}
	kitty := env(map[string]string{"TERM": "xterm-kitty"})
	if got := Escape(Terminal, "Alice", "hi", false, kitty); got != "\x1b]99;i=mc:d=0;Alice\x1b\\\x1b]99;i=mc:d=1:p=body;hi\x1b\\" {
		t.Errorf("OSC 99 = %q", got)
	}
	if got := Escape(OS, "a", "b", true, ghostty); got != "\a" {
		t.Errorf("OS method should only ring the bell in the terminal, got %q", got)
	}
	if got := Escape(Off, "a", "b", true, ghostty); got != "" {
		t.Errorf("off should be silent, got %q", got)
	}
}

func TestDesktopAndPlatforms(t *testing.T) {
	saved, savedOS, savedLook := run, goos, lookPath
	t.Cleanup(func() { run, goos, lookPath = saved, savedOS, savedLook })
	var calls [][]string
	run = func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}

	goos = "darwin"
	if Detect(env(nil)) != OS {
		t.Error("macOS without a known terminal uses system notifications")
	}
	Desktop(`say "hi"`, "line\nbreak \\ done")
	if len(calls) != 1 || calls[0][0] != "osascript" || calls[0][2] != `display notification "line break \\ done" with title "say \"hi\""` {
		t.Errorf("osascript call = %q", calls)
	}

	goos = "linux"
	lookPath = func(string) (string, error) { return "/usr/bin/notify-send", nil }
	if Detect(env(nil)) != OS {
		t.Error("Linux with notify-send uses it")
	}
	Desktop("Alice", "hi\x07")
	if calls[1][0] != "notify-send" || calls[1][3] != "hi " {
		t.Errorf("notify-send call = %q", calls[1])
	}
	lookPath = func(string) (string, error) { return "", errNotFound }
	if Detect(env(nil)) != BellOnly {
		t.Error("Linux without notify-send falls back to the bell")
	}

	goos = "plan9"
	if Detect(env(nil)) != BellOnly || Desktop("a", "b") != nil {
		t.Error("other systems ring the bell only")
	}
	for _, m := range []string{"terminal", "os", "bell"} {
		if Resolve(m, nil) != Method(m) {
			t.Errorf("Resolve(%s)", m)
		}
	}
	if Resolve("auto", env(map[string]string{"TERM_PROGRAM": "iTerm.app"})) != Terminal {
		t.Error("auto detects iTerm2")
	}
}

var errNotFound = errors.New("not found")
