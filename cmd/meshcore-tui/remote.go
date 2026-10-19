package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// session is what the terminal is reached through.
type session struct {
	remote     bool // over SSH or mosh, where every frame crosses the network
	tmux       bool
	clientSync bool // tmux says the attached terminal takes synchronised output
}

// runTmux asks the tmux this process runs in, giving up quickly so startup never waits on it.
var runTmux = func(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	return string(out), err
}

// parentOf gives a process's parent and command name; only Linux has /proc to read them from.
var parentOf = func(pid int) (int, string, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, "", false
	}
	s := string(b)
	open, shut := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')') // the name can hold spaces and brackets
	if open < 0 || shut < open {
		return 0, "", false
	}
	f := strings.Fields(s[shut+1:])
	if len(f) < 2 {
		return 0, "", false
	}
	ppid, err := strconv.Atoi(f[1])
	return ppid, s[open+1 : shut], err == nil
}

var remoteServers = []string{"sshd", "sshd-session", "mosh-server", "dropbear", "etserver"}

// overNetwork walks up from pid looking for a remote-login server; known is false when the processes can't be read.
func overNetwork(pid int, parent func(int) (int, string, bool)) (remote, known bool) {
	for range 64 {
		if pid <= 1 {
			return false, true
		}
		ppid, name, ok := parent(pid)
		if !ok {
			return false, false
		}
		if slices.Contains(remoteServers, name) {
			return true, true
		}
		pid = ppid
	}
	return false, false
}

// detectSession works out whether the terminal is remote. Inside tmux the environment is the tmux
// server's, possibly from another login, so tmux is asked about the client attached now.
func detectSession(env func(string) string, tmux func(...string) (string, error), parent func(int) (int, string, bool)) session {
	s := session{tmux: env("TMUX") != ""}
	if s.tmux {
		// show-environment fails when the variable was never set, so it goes last, after the line we always want.
		out, err := tmux("display-message", "-p", "#{client_pid} #{client_termfeatures}", ";", "show-environment", "SSH_CONNECTION")
		var exit *exec.ExitError
		if lines := strings.Split(strings.TrimRight(out, "\n"), "\n"); out != "" && (err == nil || errors.As(err, &exit)) {
			f := append(strings.Fields(lines[0]), "", "")
			s.clientSync = slices.Contains(strings.Split(f[1], ","), "sync")
			if pid, err := strconv.Atoi(f[0]); err == nil && pid > 0 {
				if remote, known := overNetwork(pid, parent); known {
					s.remote = remote
					return s
				}
			}
			if len(lines) > 1 { // tmux refreshes it on every attach, and marks it "-NAME" when the client had none
				s.remote = strings.HasPrefix(lines[1], "SSH_CONNECTION=") && lines[1] != "SSH_CONNECTION="
				return s
			}
		}
	}
	for _, k := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if env(k) != "" {
			s.remote = true
			return s
		}
	}
	if !s.tmux { // mosh sets none of the SSH variables
		s.remote, _ = overNetwork(os.Getppid(), parent)
	}
	return s
}

// teaEnv is the environment for Bubble Tea, which asks the terminal about synchronised output only
// without SSH_TTY. Inside tmux, tmux answers, and the attached terminal is what decides.
func teaEnv(environ []string, s session) []string {
	if !s.tmux || (!s.clientSync && !s.remote) {
		return environ
	}
	out := slices.DeleteFunc(slices.Clone(environ), func(kv string) bool { return strings.HasPrefix(kv, "SSH_TTY=") })
	if !s.clientSync {
		// tmux redraws every changed line whole after each synced frame, about three times the bytes, and can't pass them on at once anyway
		out = append(out, "SSH_TTY=")
	}
	return out
}
