package signing

import (
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	f.Add([]byte("MC1"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if d, ok := Parse(b); ok {
			d.Verify([32]byte{1}, "#x", string(b))
		}
	})
}

func FuzzParseV2(f *testing.F) {
	f.Add([]byte("MC2"))
	f.Add([]byte("MCB"))
	f.Add([]byte("MCR"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if d, ok := ParseV2(b); ok {
			d.Verify([32]byte{1}, [32]byte{2}, "#x", string(b))
		}
		if k, ok := ParseBinding(b); ok {
			k.Verify([32]byte{1})
		}
		ParseRevocation(b)
	})
}

func FuzzKeyfile(f *testing.F) {
	f.Add(`{"v":2,"pub":"00","slots":[]}`)
	f.Fuzz(func(t *testing.T, s string) {
		if k, err := ReadKeyfile(strings.NewReader(s)); err == nil {
			k.UnlockRecovery("AAAA")
		}
	})
}
