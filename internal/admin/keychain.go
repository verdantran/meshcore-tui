package admin

import (
	"sync"

	"github.com/zalando/go-keyring"

	"meshcore-tui/internal/radio"
)

// Keychain keeps node passwords out of the config file.
type Keychain interface {
	Get(node [32]byte) (string, bool)
	Set(node [32]byte, password string) error
	Forget(node [32]byte) error
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

func (c *Cached) Forget(node [32]byte) error {
	err := c.inner.Forget(node)
	c.mu.Lock()
	c.known[node] = cachedEntry{}
	c.mu.Unlock()
	return err
}
