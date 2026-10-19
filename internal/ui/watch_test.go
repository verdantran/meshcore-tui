package ui

import (
	"fmt"
	"math"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/admin"
	"github.com/verdantran/meshcore-tui/internal/motion"
	"github.com/verdantran/meshcore-tui/internal/radio"
)

func TestWatchPacesItselfWhileNodesAreDue(t *testing.T) {
	h := newHarness(t)
	h.keys("5")
	nodes := h.m.adminNodes()
	if len(nodes) < 3 {
		t.Fatalf("the sim has %d nodes to watch", len(nodes))
	}
	for _, c := range nodes[:3] {
		h.m.toggleWatch(c)
	}
	h.clk.t = h.clk.t.Add(11 * time.Minute)
	h.m.now = h.clk.t
	if h.m.watchWait() != watchPace {
		t.Fatal("three nodes are due, but the watch waits as if none were")
	}
	for range 3 {
		h.m.pollWatched() // no saved passwords, so each only notes it was tried
	}
	if _, n := h.m.overdue(); n != 0 || h.m.watchWait() != watchCheck {
		t.Fatalf("%d still due after a poll each; the watch waits %v", n, h.m.watchWait())
	}
}

func TestHealthIsMeasuredOnlyWhenItWouldChange(t *testing.T) {
	m, c := newTestModel(t, motion.Full, 100, 30)
	key := [32]byte{7}
	var hist []admin.StatusPoint
	var recv uint32
	for i := range 200 {
		recv += uint32(40 + 30*math.Sin(float64(i)/8))
		hist = append(hist, admin.StatusPoint{At: c.t.Add(time.Duration(i-200) * 10 * time.Minute), Stats: meshcore.RepeaterStats{
			CommonStats: meshcore.CommonStats{BattMilliVolts: uint16(4150 - i), PacketsRecv: recv, UpTimeSecs: uint32(i * 600)}}})
	}
	m.adm.kept[key] = hist
	k := healthKey{key, 24 * time.Hour, 16}
	hits, start := 0, c.t
	for step := range 2000 {
		m.now = start.Add(time.Duration(step) * 3 * time.Second)
		before, cached := m.adm.measured[k]
		got := m.measure(key, 24*time.Hour, 16)
		if cached && m.adm.measured[k].at == before.at {
			hits++
		}
		want := admin.HealthOf(hist, m.now.Add(-24*time.Hour), m.now, 16)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("at %v the kept measure differs:\n%v\n%v", m.now.Sub(start), got, want)
		}
	}
	if hits < 1500 {
		t.Errorf("measured again %d times in 2000 frames", 2000-hits)
	}

	m.clearHistory(radio.Contact{Key: key, Name: "Dundry"})
	if got := m.measure(key, 24*time.Hour, 16); len(got.Series) != 0 {
		t.Error("cleared history is still measured")
	}
}
