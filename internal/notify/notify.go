// Package notify raises desktop notifications: through the terminal where it supports them,
// which also works over SSH, or through the operating system otherwise.
package notify

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

type Method string

const (
	Off      Method = "off"
	Terminal Method = "terminal" // OSC 9, or OSC 99 on kitty
	OS       Method = "os"       // osascript, notify-send, or a PowerShell toast
	BellOnly Method = "bell"
)

// Detect picks the best method for this session. When it's remote, over SSH or mosh, only terminal escapes
// reach the user's desktop.
func Detect(env func(string) string, remote bool) Method {
	if env == nil {
		env = os.Getenv
	}
	switch prog := env("TERM_PROGRAM"); {
	case prog == "ghostty" || prog == "iTerm.app" || prog == "WezTerm":
		return Terminal
	case strings.Contains(env("TERM"), "kitty") || strings.Contains(env("TERM"), "ghostty"):
		return Terminal
	}
	if remote {
		return BellOnly
	}
	switch goos {
	case "darwin":
		return OS
	case "linux":
		if _, err := lookPath("notify-send"); err == nil {
			return OS
		}
	case "windows":
		if _, err := lookPath("powershell.exe"); err == nil {
			return OS
		}
	}
	return BellOnly
}

// Resolve turns the config value into a method; "auto" or empty means Detect.
func Resolve(setting string, env func(string) string, remote bool) Method {
	switch m := Method(setting); m {
	case Off, Terminal, OS, BellOnly:
		return m
	}
	return Detect(env, remote)
}

// clean keeps notification text to one line with no control characters, which would end an escape sequence
// early.
func clean(s string) string { return strings.ReplaceAll(untrusted.Text(s), "\n", " ") }

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

// run starts an external command, with extra environment variables; tests replace it so they don't raise
// real notifications.
var run = func(env []string, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), runLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd.Run()
}

var (
	goos     = runtime.GOOS
	lookPath = exec.LookPath
	runLimit = 5 * time.Second // so a hung notification daemon can't pile up waiting notifiers
)

// Desktop sends an OS notification; it's only used when the method is OS.
func Desktop(title, body string) error {
	switch goos {
	case "darwin":
		return run(nil, "osascript", "-e", `display notification `+appleQuote(body)+` with title `+appleQuote(title))
	case "linux":
		return run(nil, "notify-send", "--app-name=meshcore-tui", "--", clean(title), markupEscaper.Replace(clean(body)))
	case "windows":
		// The text goes in the environment, never the script: PowerShell ends a quoted string at curly
		// quotes as well as straight ones, so no quoting of it inside the script can be trusted.
		return run([]string{"MESHCORE_TUI_TITLE=" + clean(title), "MESHCORE_TUI_BODY=" + clean(body)},
			"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", toastScript)
	}
	return nil
}

// toastScript raises a Windows toast with the title and body from the environment. It's sent as
// PowerShell's own, since Windows only shows toasts from an app it knows.
const toastScript = `$n = [Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime]
$x = $n::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$t = $x.GetElementsByTagName('text')
$t.Item(0).AppendChild($x.CreateTextNode($env:MESHCORE_TUI_TITLE)) > $null
$t.Item(1).AppendChild($x.CreateTextNode($env:MESHCORE_TUI_BODY)) > $null
$n::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show([Windows.UI.Notifications.ToastNotification]::new($x))`

// markupEscaper escapes a notify-send body, which daemons may read as markup.
var markupEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "'", "&apos;", `"`, "&quot;")

func appleQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(clean(s)) + `"`
}
