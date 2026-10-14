// Package notify raises desktop notifications: through the terminal where it supports them,
// which also works over SSH, or through the operating system otherwise.
package notify

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type Method string

const (
	Off      Method = "off"
	Terminal Method = "terminal" // OSC 9, or OSC 99 on kitty
	OS       Method = "os"       // osascript or notify-send
	BellOnly Method = "bell"
)

// Detect picks the best method for this session. Over SSH only terminal escapes reach the user's desktop.
func Detect(env func(string) string) Method {
	if env == nil {
		env = os.Getenv
	}
	switch prog := env("TERM_PROGRAM"); {
	case prog == "ghostty" || prog == "iTerm.app" || prog == "WezTerm":
		return Terminal
	case strings.Contains(env("TERM"), "kitty") || strings.Contains(env("TERM"), "ghostty"):
		return Terminal
	}
	if env("SSH_CONNECTION") != "" {
		return BellOnly
	}
	switch goos {
	case "darwin":
		return OS
	case "linux":
		if _, err := lookPath("notify-send"); err == nil {
			return OS
		}
	}
	return BellOnly
}

// Resolve turns the config value into a method; "auto" or empty means Detect.
func Resolve(setting string, env func(string) string) Method {
	switch m := Method(setting); m {
	case Off, Terminal, OS, BellOnly:
		return m
	}
	return Detect(env)
}

func clean(s string) string {
	// Control characters would end the escape sequence early.
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// Escape is the terminal sequence for a notification, with the bell first if wanted.
func Escape(m Method, title, body string, bell bool, env func(string) string) string {
	if env == nil {
		env = os.Getenv
	}
	var b strings.Builder
	if bell && m != Off {
		b.WriteString("\a")
	}
	if m != Terminal {
		return b.String()
	}
	title, body = clean(title), clean(body)
	if strings.Contains(env("TERM"), "kitty") {
		b.WriteString("\x1b]99;i=mc:d=0;" + title + "\x1b\\")
		b.WriteString("\x1b]99;i=mc:d=1:p=body;" + body + "\x1b\\")
		return b.String()
	}
	b.WriteString("\x1b]9;" + title + ": " + body + "\a")
	return b.String()
}

// run starts an external command; tests replace it so they don't raise real notifications.
var run = func(name string, args ...string) error { return exec.Command(name, args...).Run() }

var (
	goos     = runtime.GOOS
	lookPath = exec.LookPath
)

// Desktop sends an OS notification; it's only used when the method is OS.
func Desktop(title, body string) error {
	switch goos {
	case "darwin":
		return run("osascript", "-e", `display notification `+appleQuote(body)+` with title `+appleQuote(title))
	case "linux":
		return run("notify-send", "--app-name=meshcore-tui", clean(title), clean(body))
	}
	return nil
}

func appleQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(clean(s)) + `"`
}
