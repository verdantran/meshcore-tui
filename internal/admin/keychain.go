package admin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/zalando/go-keyring"

	"meshcore-tui/internal/radio"
)

// Keychain keeps node passwords out of the config file.
type Keychain interface {
	Get(node [32]byte) (string, bool)
	Set(node [32]byte, password string) error
	Forget(node [32]byte) error
	Where() string // for the log line when a password is saved
}

const keychainService = "meshcore-tui"

// OSKeychain is the system's: Keychain on macOS, the Secret Service on Linux, Credential Manager on Windows.
type OSKeychain struct{}

func (OSKeychain) Get(node [32]byte) (string, bool) {
	pw, err := keyring.Get(keychainService, radio.KeyHex(node[:]))
	return pw, err == nil
}

func (OSKeychain) Set(node [32]byte, password string) error {
	return keyring.Set(keychainService, radio.KeyHex(node[:]), password)
}

func (OSKeychain) Forget(node [32]byte) error {
	return keyring.Delete(keychainService, radio.KeyHex(node[:]))
}

func (OSKeychain) Where() string { return "the system keychain" }

// SystemOr is the system keychain where there is one, and otherwise fallback: a headless Linux
// server has no Secret Service to talk to.
func SystemOr(fallback Keychain) Keychain {
	if runtime.GOOS != "linux" {
		return OSKeychain{}
	}
	if _, err := keyring.Get(keychainService, "probe"); err == nil || errors.Is(err, keyring.ErrNotFound) {
		return OSKeychain{}
	}
	return fallback
}

// FileKeychain keeps passwords in a file only its owner can read, as .netrc does.
type FileKeychain struct {
	Path string
	mu   sync.Mutex
}

func (k *FileKeychain) load() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(k.Path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(b, &m)
}

func (k *FileKeychain) save(m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(k.Path), 0o700); err != nil {
		return err
	}
	tmp := k.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, k.Path)
}

func (k *FileKeychain) Get(node [32]byte) (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	pw, ok := m[radio.KeyHex(node[:])]
	return pw, err == nil && ok
}

func (k *FileKeychain) Set(node [32]byte, password string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return err
	}
	m[radio.KeyHex(node[:])] = password
	return k.save(m)
}

func (k *FileKeychain) Forget(node [32]byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return err
	}
	delete(m, radio.KeyHex(node[:]))
	return k.save(m)
}

func (k *FileKeychain) Where() string { return k.Path }

// MemoryKeychain forgets everything when the program ends.
type MemoryKeychain struct {
	mu sync.Mutex
	m  map[[32]byte]string
}

func (k *MemoryKeychain) Get(node [32]byte) (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	pw, ok := k.m[node]
	return pw, ok
}

func (k *MemoryKeychain) Set(node [32]byte, password string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[[32]byte]string{}
	}
	k.m[node] = password
	return nil
}

func (k *MemoryKeychain) Forget(node [32]byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, node)
	return nil
}

func (k *MemoryKeychain) Where() string { return "memory, until you quit" }

// Cached remembers what a keychain said, since asking the system keychain can mean starting a
// process: macOS's is reached through /usr/bin/security. Redraws read only the cache.
type Cached struct {
	inner Keychain
	mu    sync.Mutex
	known map[[32]byte]cachedEntry
}

type cachedEntry struct {
	password string
	saved    bool
}

func NewCached(k Keychain) *Cached { return &Cached{inner: k, known: map[[32]byte]cachedEntry{}} }

func (c *Cached) Get(node [32]byte) (string, bool) {
	c.mu.Lock()
	e, ok := c.known[node]
	c.mu.Unlock()
	if ok {
		return e.password, e.saved
	}
	pw, saved := c.inner.Get(node)
	c.mu.Lock()
	c.known[node] = cachedEntry{pw, saved}
	c.mu.Unlock()
	return pw, saved
}

// Saved reports what's cached without asking the keychain; known is false until it has been asked.
func (c *Cached) Saved(node [32]byte) (saved, known bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.known[node]
	return e.saved, ok
}

func (c *Cached) Set(node [32]byte, password string) error {
	if err := c.inner.Set(node, password); err != nil {
		return err
	}
	c.mu.Lock()
	c.known[node] = cachedEntry{password, true}
	c.mu.Unlock()
	return nil
}

func (c *Cached) Where() string { return c.inner.Where() }

func (c *Cached) Forget(node [32]byte) error {
	err := c.inner.Forget(node)
	c.mu.Lock()
	c.known[node] = cachedEntry{}
	c.mu.Unlock()
	return err
}
