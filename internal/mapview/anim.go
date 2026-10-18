package mapview

import (
	"math"
	"time"

	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/theme"
)

type AnimKind int

const (
	Comet  AnimKind = iota // a packet travelling its route, hop by hop
	Pulse                  // an advert: a ring at the node that sent it
	Ripple                 // a flood spreading from where it started
)

type Anim struct {
	Kind    AnimKind
	Start   time.Time
	Path    []LatLon
	Links   []Link // a comet's tail takes each hop's signal colour
	Outward bool   // ours, going out, rather than heard
	At      LatLon
}

const (
	HopDur       = 320 * time.Millisecond
	pulseDur     = 1100 * time.Millisecond
	rippleDur    = 900 * time.Millisecond
	beaconPeriod = 2400 * time.Millisecond
)

func (a Anim) Dur() time.Duration {
	switch a.Kind {
	case Comet:
		return HopDur * time.Duration(max(len(a.Path)-1, 1))
	case Pulse:
		return pulseDur
	}
	return rippleDur
}

func (a Anim) Running(now time.Time) bool { return motion.Running(a.Start, a.Dur(), now) }

// Allowed says which animations a motion level shows: everything at full, no ripples at reduced, none when off.
func (a Anim) Allowed(l motion.Level) bool {
	return l == motion.Full || l == motion.Reduced && a.Kind != Ripple
}

// DrawAnims paints running animations, and at full motion the beacon pulsing around us, over the map.
func DrawAnims(c *Canvas, v View, anims []Anim, self LatLon, level motion.Level, now time.Time, pal Palette) {
	v.W, v.H = c.W*2, c.H*4
	if level == motion.Full && !self.Unplaced() {
		beacon(c, v, self, now, pal)
	}
	for _, a := range anims {
		if !a.Allowed(level) {
			continue
		}
		t := motion.Progress(a.Start, a.Dur(), now)
		if t <= 0 || t >= 1 {
			continue
		}
		switch a.Kind {
		case Comet:
			comet(c, v, a, t, pal)
		case Pulse:
			x, y := v.Project(a.At)
			ring(c, x, y, 3+14*motion.EaseOut(t), pal.Cyan.Lerp(pal.Land, t), prioAnim)
			if t < 0.5 {
				ring(c, x, y, 3+7*motion.EaseOut(t*2), pal.Text.Lerp(pal.Land, t*2), prioAnim)
			}
		case Ripple:
			x, y := v.Project(a.At)
			col := pal.Violet
			if a.Outward {
				col = pal.Magenta
			}
			ring(c, x, y, 4+34*motion.EaseOut(t), col.Lerp(pal.Land, 0.35+0.65*t), prioRipple)
		}
	}
}

func ring(c *Canvas, cx, cy, r float64, col theme.RGB, prio int) {
	steps := int(2*math.Pi*r) + 8
	for i := range steps {
		a := float64(i) / float64(steps) * 2 * math.Pi
		c.Dot(int(math.Round(cx+r*math.Cos(a))), int(math.Round(cy+r*math.Sin(a))), col, prio)
	}
}

// pointAt is the position at progress p, in hops, along a projected path, and which hop that is.
func pointAt(pts [][2]float64, p float64) (float64, float64, int) {
	if p <= 0 {
		return pts[0][0], pts[0][1], 0
	}
	i := int(p)
	if i >= len(pts)-1 {
		n := len(pts) - 1
		return pts[n][0], pts[n][1], n - 1
	}
	f := p - float64(i)
	a, b := pts[i], pts[i+1]
	return a[0] + (b[0]-a[0])*f, a[1] + (b[1]-a[1])*f, i
}

// comet draws a white-hot head with a tail in the hop's signal colour, or magenta for ours, fading to the land.
func comet(c *Canvas, v View, a Anim, t float64, pal Palette) {
	if len(a.Path) < 2 {
		return
	}
	pts := make([][2]float64, len(a.Path))
	for i, p := range a.Path {
		x, y := v.Project(p)
		pts[i] = [2]float64{x, y}
	}
	head := t * float64(len(pts)-1)
	const tail, steps = 0.6, 28 // tail length in hops
	for k := steps; k >= 0; k-- {
		p := head - tail*float64(k)/steps
		if p < 0 {
			continue
		}
		x, y, hop := pointAt(pts, p)
		col := pal.Magenta
		if !a.Outward {
			col = pal.Unknown.Lerp(pal.Text, 0.4)
			if hop < len(a.Links) && a.Links[hop].Known {
				col = pal.SNR(a.Links[hop].SNR)
			}
		}
		f := float64(k) / steps
		if f < 0.25 {
			col = pal.Text.Lerp(col, f/0.25)
		} else {
			col = col.Lerp(pal.Land, (f-0.25)/0.75*0.8)
		}
		if k == 0 {
			for dx := -1; dx <= 1; dx++ {
				for dy := -1; dy <= 1; dy++ {
					c.Dot(int(x)+dx, int(y)+dy, pal.Text, prioAnim+1)
				}
			}
			continue
		}
		c.Dot(int(x), int(y), col, prioAnim)
		if f < 0.4 {
			c.Dot(int(x)+1, int(y), col, prioAnim)
		}
	}
}

// beacon is two rings growing out from us half a period apart, fading as they go.
func beacon(c *Canvas, v View, self LatLon, now time.Time, pal Palette) {
	x, y := v.Project(self)
	period := beaconPeriod.Milliseconds()
	for k := range int64(2) {
		t := float64((now.UnixMilli()+k*period/2)%period) / float64(period)
		ring(c, x, y, 2+8*motion.EaseOut(t), pal.Green.Lerp(pal.Land, 0.5+0.45*t), prioBeacon)
	}
}
