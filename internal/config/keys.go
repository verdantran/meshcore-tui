package config

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

// KeyScopes are keys.toml's sections: everywhere, then the connect screen and each main screen, the key after
// ctrl+w, and the views within a screen, which win over the screen's own section.
var KeyScopes = []string{"all", "connect", "chats", "contacts", "map", "packets", "admin", "device", "stats", "panes",
	"chats.reading", "contacts.heard", "map.path", "admin.list", "admin.status", "admin.health", "admin.settings",
	"admin.neighbours", "admin.access", "admin.regions", "admin.telemetry", "admin.log"}

// Keys rebinds keys by scope: each pressed key does what another does by default, or nothing when
// bound to "".
type Keys map[string]map[string]string

// LoadKeys reads keys.toml; a missing file is no rebinding.
func LoadKeys(path string) (Keys, error) {
	var raw map[string]any
	_, err := toml.DecodeFile(path, &raw)
	if errors.Is(err, fs.ErrNotExist) {
		return Keys{}, nil
	}
	if err != nil {
		return nil, err
	}
	k := Keys{}
	var walk func(scope string, t map[string]any) error
	walk = func(scope string, t map[string]any) error {
		if !slices.Contains(KeyScopes, scope) {
			return fmt.Errorf("unknown section [%s]; use %s", untrusted.Line(scope), strings.Join(KeyScopes, ", "))
		}
		for from, v := range t {
			switch v := v.(type) {
			case string:
				if v == "y" {
					return fmt.Errorf("[%s] %q: nothing can be bound to y, the key that answers yes", scope, from)
				}
				if k[scope] == nil {
					k[scope] = map[string]string{}
				}
				k[scope][from] = v
			case map[string]any:
				if err := walk(scope+"."+from, v); err != nil {
					return err
				}
			default:
				return fmt.Errorf("[%s] %q: a key is bound to another key's name, in quotes", scope, from)
			}
		}
		return nil
	}
	for scope, v := range raw {
		t, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%q: bindings go under a section, such as [all]", scope)
		}
		if err := walk(scope, t); err != nil {
			return nil, err
		}
	}
	return k, nil
}
