//go:build !darwin && !linux && !windows

package radio

import (
	"context"
	"errors"
)

type noBLE struct{}

var errNoBLE = errors.New("Bluetooth isn't supported on this system")

func systemBLE() bleAdapter { return noBLE{} }

func (noBLE) connect(context.Context, string, func([]byte), func()) (blePeer, error) {
	return nil, errNoBLE
}
func (noBLE) scan(context.Context, func(BLEDevice)) error { return errNoBLE }
