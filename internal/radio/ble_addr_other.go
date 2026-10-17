//go:build linux || windows

package radio

import "tinygo.org/x/bluetooth"

func parseBLEAddress(s string) (bluetooth.Address, error) {
	mac, err := bluetooth.ParseMAC(s)
	return bluetooth.Address{MACAddress: bluetooth.MACAddress{MAC: mac}}, err
}
