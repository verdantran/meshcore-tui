// Package golden compares rendered frames with files kept under a package's testdata/golden, for tests.
// go test -update rewrites them.
package golden

import (
	"flag"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

var update = flag.Bool("update", false, "rewrite the golden frames")

// Frame is a screen at one moment, as rendered with its colours.
type Frame struct {
	Label string
	Lines []string
}

// Check compares frames with testdata/golden/<name>.txt. Each frame is kept as plain text, so a change to
// the layout reads as a diff, and a hash of its styled form, so a change only to colour is caught too.
func Check(t testing.TB, name string, frames []Frame) {
	t.Helper()
	got := format(frames)
	path := filepath.Join("testdata", "golden", name+".txt")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: no golden frames yet; go test -run %s -update writes them", name, t.Name())
	}
	if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("%s: frames differ from %s (go test -update rewrites it after checking the change):\n%s", name, path, firstDiff(string(want), got))
	}
}

func format(frames []Frame) string {
	var b strings.Builder
	for _, f := range frames {
		fmt.Fprintf(&b, "== %s ==\n", f.Label)
		h := fnv.New64a()
		for _, l := range f.Lines {
			b.WriteString(strings.TrimRight(ansi.Strip(l), " ") + "\n")
			h.Write([]byte(l + "\n"))
		}
		fmt.Fprintf(&b, "styles %016x\n", h.Sum64())
	}
	return b.String()
}

func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	frame := ""
	for i := range max(len(w), len(g)) {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if strings.HasPrefix(gl, "== ") {
			frame = gl
		}
		if wl != gl {
			return fmt.Sprintf("in frame %s, line %d\nwant: %q\n got: %q", frame, i+1, wl, gl)
		}
	}
	return ""
}
