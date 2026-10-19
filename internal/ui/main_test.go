package ui

import (
	"os"
	"testing"
	"time"
)

// TestMain keeps every test away from the real config and data directories, and in UTC so golden frames
// show the same clock times on every machine.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	dir, err := os.MkdirTemp("", "meshcore-tui-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_DATA_HOME", dir)
	os.Setenv("XDG_CONFIG_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
