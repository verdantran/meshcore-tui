package admin

import (
	"math"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"meshcore-tui/internal/radio"
)

// PointFrom decodes a status reply. Room servers send a longer reply than repeaters.
func PointFrom(at time.Time, room bool, data []byte) (StatusPoint, bool) {
	p := StatusPoint{At: at}
	if room {
		r, err := meshcore.ParseRoomStats(data)
		if err != nil {
			return p, false
		}
		p.Stats.CommonStats, p.Room = r.CommonStats, &r
		return p, true
	}
	st, err := meshcore.ParseRepeaterStats(data)
	if err != nil {
		return p, false
	}
	p.Stats = st
	return p, true
}

func pointFor(t radio.ContactType, at time.Time, data []byte) StatusPoint {
	p, _ := PointFrom(at, t == radio.TypeRoom, data)
	return p
}

// Series is one health measure over a window, split into equal buckets. Rates come from the
// difference between polls, because the node's counters are totals since it booted.
type Series struct {
	Name   string
	Unit   string
	Values []float64 // NaN where no poll fell in the bucket
	Latest float64   // NaN until there's a value
	Avg    float64   // over the whole window; NaN if there's nothing
}

// Health is a node's measures over a window.
type Health struct {
	From, To time.Time
	Series   []Series
	Reboots  int
	Polls    int
}

type ratio struct{ num, den float64 }

func (r ratio) value(scale float64) float64 {
	if r.den <= 0 {
		return math.NaN()
	}
	return r.num / r.den * scale
}

type measure struct {
	name, unit string
	scale      float64
	pair       func(a, b StatusPoint, secs float64) (ratio, bool) // from two polls
	gauge      func(p StatusPoint) (float64, bool)                // from one
}

var measures = []measure{
	{name: "Received", unit: "/h", scale: 3600, pair: func(a, b StatusPoint, secs float64) (ratio, bool) {
		return ratio{float64(statsDelta(a, b).PacketsRecv), secs}, true
	}},
	{name: "Sent", unit: "/h", scale: 3600, pair: func(a, b StatusPoint, secs float64) (ratio, bool) {
		return ratio{float64(statsDelta(a, b).PacketsSent), secs}, true
	}},
	{name: "Duplicates", unit: "%", scale: 100, pair: func(a, b StatusPoint, secs float64) (ratio, bool) {
		d := statsDelta(a, b)
		return ratio{float64(d.FloodDups) + float64(d.DirectDups), float64(d.PacketsRecv)}, true
	}},
	{name: "Receive errors", unit: "%", scale: 100, pair: func(a, b StatusPoint, secs float64) (ratio, bool) {
		if a.Room != nil || b.Room != nil {
			return ratio{}, false
		}
		errs := float64(b.Stats.RecvErrors) - float64(a.Stats.RecvErrors)
		return ratio{errs, errs + float64(statsDelta(a, b).PacketsRecv)}, true
	}},
	{name: "Transmitting", unit: "%", scale: 100, pair: func(a, b StatusPoint, secs float64) (ratio, bool) {
		return ratio{float64(statsDelta(a, b).AirTimeSecs), secs}, true
	}},
	{name: "Receiving", unit: "%", scale: 100, pair: func(a, b StatusPoint, secs float64) (ratio, bool) {
		if a.Room != nil || b.Room != nil {
			return ratio{}, false
		}
		return ratio{float64(b.Stats.RxAirTimeSecs) - float64(a.Stats.RxAirTimeSecs), secs}, true
	}},
	{name: "Noise floor", unit: "dBm", gauge: func(p StatusPoint) (float64, bool) {
		return float64(p.Stats.NoiseFloor), p.Stats.NoiseFloor != 0
	}},
	{name: "Battery", unit: "V", gauge: func(p StatusPoint) (float64, bool) {
		return float64(p.Stats.BattMilliVolts) / 1000, p.Stats.BattMilliVolts != 0
	}},
}

// statsDelta is b's counters less a's. The caller has checked none went backwards.
func statsDelta(a, b StatusPoint) meshcore.CommonStats {
	x, y := a.Stats.CommonStats, b.Stats.CommonStats
	return meshcore.CommonStats{PacketsRecv: y.PacketsRecv - x.PacketsRecv, PacketsSent: y.PacketsSent - x.PacketsSent,
		AirTimeSecs: y.AirTimeSecs - x.AirTimeSecs, FloodDups: y.FloodDups - x.FloodDups, DirectDups: y.DirectDups - x.DirectDups}
}

// reset reports whether the node rebooted or had its stats cleared between two polls.
func reset(a, b StatusPoint) bool {
	x, y := a.Stats.CommonStats, b.Stats.CommonStats
	return y.UpTimeSecs < x.UpTimeSecs || y.PacketsRecv < x.PacketsRecv || y.PacketsSent < x.PacketsSent ||
		y.AirTimeSecs < x.AirTimeSecs || y.FloodDups < x.FloodDups || y.DirectDups < x.DirectDups ||
		b.Stats.RecvErrors < a.Stats.RecvErrors || b.Stats.RxAirTimeSecs < a.Stats.RxAirTimeSecs
}

// HealthOf measures a node over [from, to] in n buckets. points must be oldest first; one poll before
// from lets the first bucket have a rate. Two polls too far apart to say when things happened leave a gap.
func HealthOf(points []StatusPoint, from, to time.Time, n int) Health {
	h := Health{From: from, To: to}
	width := to.Sub(from) / time.Duration(max(n, 1))
	maxGap := max(2*width, 30*time.Minute)
	bucket := func(t time.Time) int { return min(int(t.Sub(from)/width), n-1) }
	for _, ms := range measures {
		s := Series{Name: ms.name, Unit: ms.unit, Values: make([]float64, n), Latest: math.NaN()}
		sums := make([]ratio, n)
		var total ratio
		seen := false
		for i, p := range points {
			if p.At.Before(from) || p.At.After(to) {
				continue
			}
			b := bucket(p.At)
			if ms.gauge != nil {
				if v, ok := ms.gauge(p); ok {
					sums[b].num += v
					sums[b].den++
					total.num += v
					total.den++
					s.Latest, seen = v, true
				}
				continue
			}
			if i == 0 {
				continue
			}
			prev := points[i-1]
			gap := p.At.Sub(prev.At)
			if gap <= 0 || gap > maxGap || reset(prev, p) {
				continue
			}
			r, ok := ms.pair(prev, p, gap.Seconds())
			if !ok {
				continue
			}
			sums[b].num += r.num
			sums[b].den += r.den
			total.num += r.num
			total.den += r.den
			s.Latest, seen = r.value(ms.scale), true
		}
		if !seen {
			continue
		}
		scale := ms.scale
		if ms.gauge != nil {
			scale = 1
		}
		for b := range sums {
			s.Values[b] = sums[b].value(scale)
		}
		s.Avg = total.value(scale)
		h.Series = append(h.Series, s)
	}
	for i, p := range points {
		if p.At.Before(from) || p.At.After(to) {
			continue
		}
		h.Polls++
		if i > 0 && p.Stats.UpTimeSecs < points[i-1].Stats.UpTimeSecs {
			h.Reboots++
		}
	}
	return h
}
