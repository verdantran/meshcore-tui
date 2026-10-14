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
