package admin

import (
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeychains(t *testing.T) {
	keyring.MockInit()
	for _, k := range []Keychain{OSKeychain{}, &MemoryKeychain{}} {
		node := [32]byte{0xA3}
		if _, ok := k.Get(node); ok {
			t.Errorf("%T: nothing saved yet", k)
		}
		if err := k.Set(node, "hunter2"); err != nil {
			t.Fatal(err)
		}
		if pw, ok := k.Get(node); !ok || pw != "hunter2" {
			t.Errorf("%T: get = %q %v", k, pw, ok)
		}
		k.Forget(node)
		if _, ok := k.Get(node); ok {
			t.Errorf("%T: forgotten", k)
		}
	}
}
