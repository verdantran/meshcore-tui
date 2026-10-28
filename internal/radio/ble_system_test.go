//go:build darwin || linux || windows

package radio

import "testing"

func TestBLEDropOnWindowsIsntDisconnectedTwice(t *testing.T) {
	defer func(os string) { bleOS = os }(bleOS)
	bleOS = "windows"
	var forgot, dropped int
	p := &systemPeer{dropped: func() { dropped++ }, forget: func() { forgot++ }}
	p.lost()
	if err := p.close(); err != nil { // a zero device would fail, or crash, if it were disconnected
		t.Fatal(err)
	}
	if forgot != 1 || dropped != 1 {
		t.Errorf("forgot %d, dropped %d", forgot, dropped)
	}
}
