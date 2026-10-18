package admin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeychains(t *testing.T) {
	keyring.MockInit()
	for _, k := range []Keychain{OSKeychain{}, &MemoryKeychain{}, &FileKeychain{Path: filepath.Join(t.TempDir(), "sub", "passwords.json")}} {
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

func TestFileKeychainIsPrivateAndLasts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passwords.json")
	if err := (&FileKeychain{Path: path}).Set([32]byte{0xA3}, "hunter2"); err != nil {
		t.Fatal(err)
	}
	// Windows has no permission bits: the file is private there because it's under the user's own profile.
	if fi, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v, want 0600", fi.Mode().Perm(), err)
	}
	if pw, ok := (&FileKeychain{Path: path}).Get([32]byte{0xA3}); !ok || pw != "hunter2" {
		t.Errorf("a new run reads %q %v", pw, ok)
	}
}

// A stale 0644 temp file or a symlink planted in its place used to take the passwords with it.
func TestFileKeychainIgnoresPlantedFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no permission bits or plain symlinks")
	}
	path := filepath.Join(t.TempDir(), "passwords.json")
	k := &FileKeychain{Path: path}
	os.WriteFile(path+".tmp", nil, 0o644)
	if err := k.Set([32]byte{0xA3}, "hunter2"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("after a stale temp file: %v", fi.Mode().Perm())
	}
	outside := filepath.Join(t.TempDir(), "outside")
	os.Remove(path + ".tmp")
	if err := os.Symlink(outside, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := k.Set([32]byte{0x5F}, "swordfish"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Error("the passwords went through the symlink")
	}
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("passwords.json is %v %v", fi.Mode(), err)
	}

	open := t.TempDir()
	os.Chmod(open, 0o777)
	if err := (&FileKeychain{Path: filepath.Join(open, "passwords.json")}).Set([32]byte{0xA3}, "hunter2"); err == nil {
		t.Error("saved into a folder anyone can write to")
	}
}

func TestFileKeychainTightensAnOldFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no permission bits")
	}
	path := filepath.Join(t.TempDir(), "passwords.json")
	os.WriteFile(path, []byte(`{"A300000000000000000000000000000000000000000000000000000000000000": "hunter2"}`), 0o644)
	k := &FileKeychain{Path: path}
	k.Get([32]byte{0xA3})
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("a 0644 passwords.json is still %v after it's read", fi.Mode().Perm())
	}
	open := t.TempDir()
	os.WriteFile(filepath.Join(open, "passwords.json"), []byte(`{}`), 0o600)
	os.Chmod(open, 0o777)
	if _, err := (&FileKeychain{Path: filepath.Join(open, "passwords.json")}).load(); err == nil {
		t.Error("read from a folder anyone can write to")
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
