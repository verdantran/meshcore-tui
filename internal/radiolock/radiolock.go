// Package radiolock keeps a second copy of the app off a radio one is already using. The radio answers
// whoever asks without saying who asked, so two copies read each other's replies.
package radiolock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrInUse is another copy of the app connected to the same radio.
var ErrInUse = errors.New("another meshcore-tui window is already connected to this radio: use that one, or close it first")

var errHeld = errors.New("held")

// Claim takes the radio at kind and addr for this process until release is called. The lock is the
// system's, so it goes with the process if that ends without releasing it. Where the system can't lock,
// connecting goes ahead.
func Claim(dir, kind, addr string) (release func(), err error) {
	if kind == "USB" {
		if real, err := filepath.EvalSymlinks(addr); err == nil {
			addr = real // /dev/serial/by-id names point at the same port
		}
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + strings.ToLower(addr)))
	if os.MkdirAll(dir, 0o700) != nil {
		return func() {}, nil
	}
	f, err := os.OpenFile(filepath.Join(dir, hex.EncodeToString(sum[:8])+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, nil
	}
	if err := lock(f); err != nil {
		f.Close()
		if errors.Is(err, errHeld) {
			return nil, ErrInUse
		}
		return func() {}, nil
	}
	return func() { f.Close() }, nil
}
