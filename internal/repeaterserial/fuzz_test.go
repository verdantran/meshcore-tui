package repeaterserial

import (
	"strings"
	"testing"
)

func FuzzConsole(f *testing.F) {
	f.Add("A3CEE2B9:120:-7.25\n5F12AB34:60:4")
	f.Add("A3CEE2B988D9 3")
	f.Fuzz(func(t *testing.T, s string) {
		ParseNeighbours(s)
		ParseACL(strings.Split(s, "\n"))
		Reply(strings.Split(s, "\n"))
	})
}
