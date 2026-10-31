package main

import (
	"errors"
	"os/exec"
	"slices"
	"testing"
)

func noTmux(...string) (string, error) { return "", errors.New("no tmux") }

func noProc(int) (int, string, bool) { return 0, "", false }

// procs is a fake process table: pid → parent and name.
func procs(table map[int]struct {
	ppid int
	name string
}) func(int) (int, string, bool) {
	return func(pid int) (int, string, bool) {
		p, ok := table[pid]
		return p.ppid, p.name, ok
	}
}

var (
	overSSH = procs(map[int]struct {
		ppid int
		name string
	}{900: {800, "bash"}, 800: {700, "sshd-session"}, 700: {1, "sshd"}})
	atDesk = procs(map[int]struct {
		ppid int
		name string
	}{900: {800, "zsh"}, 800: {1, "gnome-terminal-"}})
)

// exitErr is the error tmux gives when show-environment doesn't know the variable.
func exitErr(t *testing.T) error {
	err := exec.Command("sh", "-c", "exit 1").Run()
	if err == nil {
		t.Fatal("sh exit 1 succeeded")
	}
	return err
}

func TestDetectSessionInsideTmux(t *testing.T) {
	inTmux := func(more map[string]string) func(string) string {
		vars := map[string]string{"TMUX": "/tmp/tmux-1000/default,123,0"}
		for k, v := range more {
			vars[k] = v
		}
		return env(vars)
	}
	reply := func(out string, err error) func(...string) (string, error) {
		return func(args ...string) (string, error) {
			if !slices.Contains(args, "display-message") || !slices.Contains(args, "show-environment") {
				t.Errorf("asked tmux %q", args)
			}
			return out, err
		}
	}
	cases := []struct {
		name   string
		env    func(string) string
		tmux   func(...string) (string, error)
		parent func(int) (int, string, bool)
		want   session
	}{
		{"started at the desk, attached over SSH: the client's parents say so", inTmux(nil),
			reply("900 RGB,title\n-SSH_CONNECTION\n", nil), overSSH, session{remote: true, tmux: true}},
		{"started over SSH, attached at the desk: the stale variable is ignored", inTmux(map[string]string{"SSH_CONNECTION": "1 2 3 4", "SSH_TTY": "/dev/pts/3"}),
			reply("900 RGB,sync\nSSH_CONNECTION=1 2 3 4\n", nil), atDesk, session{tmux: true, clientSync: true}},
		{"no /proc: tmux's copy, refreshed on attach, says SSH", inTmux(nil),
			reply("900 RGB\nSSH_CONNECTION=10.0.0.2 50000 10.0.0.1 22\n", nil), noProc, session{remote: true, tmux: true}},
		{"no /proc: tmux marks it gone after a local attach", inTmux(map[string]string{"SSH_CONNECTION": "1 2 3 4"}),
			reply("900 RGB\n-SSH_CONNECTION\n", nil), noProc, session{tmux: true}},
		{"no /proc, never set in tmux: the pane's own variables decide", inTmux(map[string]string{"SSH_CLIENT": "1 2 3"}),
			reply("900 \n", exitErr(t)), noProc, session{remote: true, tmux: true}},
		{"no client attached", inTmux(nil), reply(" \n-SSH_CONNECTION\n", nil), noProc, session{tmux: true}},
		{"the attached terminal names itself", inTmux(nil), reply("900 RGB,sync\tghostty 1.2.3\txterm-ghostty\n-SSH_CONNECTION\n", nil), atDesk,
			session{tmux: true, clientSync: true, clientTerm: "ghostty 1.2.3 xterm-ghostty"}},
		{"no features, but a TERM", inTmux(nil), reply("900 \t\txterm-256color\nSSH_CONNECTION=1 2 3 4\n", nil), noProc,
			session{tmux: true, remote: true, clientTerm: "xterm-256color"}},
		{"tmux won't answer: fall back to the pane's variables", inTmux(map[string]string{"SSH_TTY": "/dev/pts/3"}),
			noTmux, overSSH, session{remote: true, tmux: true}},
	}
	for _, c := range cases {
		if got := detectSession(c.env, c.tmux, c.parent); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestDetectSessionWithoutTmux(t *testing.T) {
	for _, k := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if got := detectSession(env(map[string]string{k: "x"}), noTmux, noProc); !got.remote || got.tmux {
			t.Errorf("%s set: %+v", k, got)
		}
	}
	if got := detectSession(noEnv, func(...string) (string, error) { t.Error("asked tmux outside it"); return "", nil }, noProc); got.remote {
		t.Errorf("local: %+v", got)
	}
	mosh := func(pid int) (int, string, bool) {
		if pid == 1 {
			return 0, "", false
		}
		return 1, "mosh-server", true
	}
	if got := detectSession(noEnv, noTmux, mosh); !got.remote {
		t.Error("under mosh-server, which sets no SSH variables, the session is remote")
	}
}

func TestOverNetworkStopsOnLoops(t *testing.T) {
	loop := func(pid int) (int, string, bool) { return pid, "bash", true }
	if remote, known := overNetwork(5, loop); remote || known {
		t.Error("a process that is its own parent proves nothing")
	}
}

func TestTeaEnvDecidesTheSyncQuery(t *testing.T) {
	base := []string{"TERM=tmux-256color", "SSH_TTY=/dev/pts/3"}
	has := func(e []string, kv string) bool { return slices.Contains(e, kv) }
	if got := teaEnv(base, session{remote: true}); !slices.Equal(got, base) {
		t.Errorf("plain SSH is left to Bubble Tea: %q", got)
	}
	if got := teaEnv(base, session{tmux: true, clientSync: true, remote: true}); has(got, "SSH_TTY=/dev/pts/3") || has(got, "SSH_TTY=") {
		t.Errorf("tmux passes frames on whole, so it should be asked: %q", got)
	}
	if got := teaEnv([]string{"TERM=tmux-256color"}, session{tmux: true, remote: true}); !has(got, "SSH_TTY=") {
		t.Errorf("over SSH to a terminal tmux can't sync, the query is skipped: %q", got)
	}
	if got := teaEnv(base, session{tmux: true}); !slices.Equal(got, base) {
		t.Errorf("local tmux is unchanged: %q", got)
	}
	if !has(base, "SSH_TTY=/dev/pts/3") {
		t.Error("the caller's slice was changed")
	}
}
