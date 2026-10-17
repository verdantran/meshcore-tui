package config

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// KeyScopes are keys.toml's sections: everywhere, then the connect screen and each main screen.
var KeyScopes = []string{"all", "connect", "chats", "contacts", "map", "packets", "admin", "device", "stats"}

// Keys rebinds keys by scope: each pressed key does what another does by default, or nothing when
// bound to "".
type Keys map[string]map[string]string

// LoadKeys reads keys.toml; a missing file is no rebinding.
func LoadKeys(path string) (Keys, error) {
	var k Keys
	_, err := toml.DecodeFile(path, &k)
	if errors.Is(err, fs.ErrNotExist) {
		return Keys{}, nil
	}
	if err != nil {
		return nil, err
	}
	for scope := range k {
		if !slices.Contains(KeyScopes, scope) {
			return nil, fmt.Errorf("unknown section [%s]; use %s", scope, strings.Join(KeyScopes, ", "))
		}
	}
	return k, nil
}
