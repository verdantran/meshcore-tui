package ui

import (
	"os"
	"testing"
)

// TestMain keeps every test away from the real config and data directories.
func TestMain(m *testing.M) {
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
