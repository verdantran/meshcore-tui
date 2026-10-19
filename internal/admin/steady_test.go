package admin

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func TestHealthHoldsForAsLongAsSteadySays(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	var points []StatusPoint
	for i := range 300 {
		points = append(points, poll(i*7+r.Intn(5), uint32(i*420), uint32(i*30), uint32(i*10), uint32(i), uint32(i), 0))
	}
	span := 24 * time.Hour
	changed := 0
	for trial := range 200 {
		to := t0.Add(time.Duration(r.Int63n(int64(36 * time.Hour))))
		n := 8 + trial%40
		from := to.Add(-span)
		want := HealthOf(points, from, to, n)
		d := Steady(points, from, to, n)
		for _, step := range []time.Duration{0, d / 3, d} {
			if !sameHealth(HealthOf(points, from.Add(step), to.Add(step), n), want) {
				t.Fatalf("trial %d: changed %v into a steady %v", trial, step, d)
			}
		}
		if !sameHealth(HealthOf(points, from.Add(d+1), to.Add(d+1), n), want) {
			changed++
		}
	}
	if changed == 0 {
		t.Error("Steady never names the moment the buckets change")
	}
}

// sameHealth compares two measurements bucket by bucket, gaps included, whatever their windows.
func sameHealth(a, b Health) bool {
	if a.Polls != b.Polls || a.Reboots != b.Reboots || len(a.Series) != len(b.Series) {
		return false
	}
	eq := func(x, y float64) bool { return x == y || math.IsNaN(x) && math.IsNaN(y) }
	for i, s := range a.Series {
		o := b.Series[i]
		if s.Name != o.Name || !eq(s.Latest, o.Latest) || !eq(s.Avg, o.Avg) || len(s.Values) != len(o.Values) {
			return false
		}
		for j := range s.Values {
			if !eq(s.Values[j], o.Values[j]) {
				return false
			}
		}
	}
	return true
}
