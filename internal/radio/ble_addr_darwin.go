package radio

import "tinygo.org/x/bluetooth"

// parseBLEAddress reads a radio's address; macOS names each device by a UUID of its own, not its MAC.
func parseBLEAddress(s string) (bluetooth.Address, error) {
	u, err := bluetooth.ParseUUID(s)
	return bluetooth.Address{UUID: u}, err
}
