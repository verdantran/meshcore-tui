package packets

import (
	"strings"
	"testing"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

// FuzzDecode feeds the decoder what any radio in range could send: it mustn't panic, and nothing it returns
// may carry a control character to the terminal.
func FuzzDecode(f *testing.F) {
	f.Add([]byte{0x15, 0x01, 0x90, 0x3a, 0x84, 0x5f, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	f.Add([]byte{0x11, 0x03, 0xe1, 0x08, 0xa3})
	book := Book{Channels: []radio.Channel{{Name: "Public", Secret: [16]byte{0x8b, 0x33, 0x87, 0xe9, 0xc5, 0xcd, 0xea, 0x6a, 0xc9, 0xe5, 0xed, 0xba, 0xa1, 0x15, 0xcd, 0x72}}}}
	f.Fuzz(func(t *testing.T, b []byte) {
		d := Decode(b, book)
		for _, s := range append([]string{d.Summary, d.Text, d.Name, d.Sender}, values(d)...) {
			if strings.ContainsAny(s, "\x1b\u009b\u009d") {
				t.Fatalf("%q kept an escape", s)
			}
		}
	})
}

func values(d Decoded) []string {
	var out []string
	for _, f := range d.Fields {
		out = append(out, f.Value)
	}
	return out
}
