package mapview

import (
	"fmt"
	"testing"
	"time"

	"meshcore-tui/internal/golden"
	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/theme"
)

// Each map animation at its start, midpoint and end, over a fixed view of Bristol.
func TestGoldenAnims(t *testing.T) {
	w := LoadWorld()
	pal := PaletteFor(theme.Neon)
	self := LatLon{51.4545, -2.5879}
	hill, mast := LatLon{51.41, -2.68}, LatLon{51.50, -2.45}
	v := View{Center: self, Zoom: 8.5}
	base := Draw(Input{World: w, Nodes: []Node{{Name: "me", Kind: KindSelf, Pos: self}, {Name: "Dundry", Kind: KindRepeater, Pos: hill}, {Name: "Mast", Kind: KindRepeater, Pos: mast}}}, v, 60, 18, pal)
	t0 := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		anim *Anim // nil for the beacon alone
		dur  time.Duration
	}{
		{"map-comet", &Anim{Kind: Comet, Start: t0, Path: []LatLon{mast, hill, self}}, 2 * HopDur},
		{"map-pulse", &Anim{Kind: Pulse, Start: t0, At: hill}, pulseDur},
		{"map-ripple", &Anim{Kind: Ripple, Start: t0, At: mast}, rippleDur},
		{"map-ripple-outward", &Anim{Kind: Ripple, Start: t0, At: self, Outward: true}, rippleDur},
		{"map-beacon", nil, beaconPeriod / 2}, // two rings half a period apart: the picture repeats each half
	} {
		var frames []golden.Frame
		for _, d := range []time.Duration{0, c.dur / 2, c.dur} {
			cv := base.Clone()
			var anims []Anim
			at := self
			if c.anim != nil {
				anims, at = []Anim{*c.anim}, LatLon{0, 0}
			}
			DrawAnims(cv, v, anims, at, motion.Full, t0.Add(d), pal)
			frames = append(frames, golden.Frame{Label: fmt.Sprint(d), Lines: cv.Render(true)})
		}
		golden.Check(t, c.name, frames)
	}
}
