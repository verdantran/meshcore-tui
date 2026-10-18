package signing

import "testing"

func FuzzParse(f *testing.F) {
	f.Add([]byte("MC1"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if d, ok := Parse(b); ok {
			d.Verify([32]byte{1}, "#x", string(b))
		}
	})
}
