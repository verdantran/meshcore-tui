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

type countingKeychain struct {
	MemoryKeychain
	gets int
}

func (k *countingKeychain) Get(node [32]byte) (string, bool) {
	k.gets++
	return k.MemoryKeychain.Get(node)
}

func TestCachedKeychain(t *testing.T) {
	inner := &countingKeychain{}
	inner.Set([32]byte{1}, "pw")
	c := NewCached(inner)
	if _, known := c.Saved([32]byte{1}); known {
		t.Error("nothing asked yet")
	}
	for range 3 {
		if pw, ok := c.Get([32]byte{1}); !ok || pw != "pw" {
			t.Fatal("get")
		}
		c.Get([32]byte{2})
	}
	if inner.gets != 2 {
		t.Errorf("the keychain was asked %d times, want once per node", inner.gets)
	}
	if saved, known := c.Saved([32]byte{1}); !saved || !known {
		t.Error("cached")
	}
	c.Set([32]byte{2}, "new")
	if pw, _ := c.Get([32]byte{2}); pw != "new" {
		t.Error("set updates the cache")
	}
	c.Forget([32]byte{1})
	if _, ok := c.Get([32]byte{1}); ok || inner.gets != 2 {
		t.Error("forget updates the cache")
	}
}
