package ui

import (
	"fmt"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/sim"
)

// busyHarness is the simulator with its traffic on, at 200×60, after a while of the mesh talking.
func busyHarness(tb testing.TB) *harness {
	h := newHarnessWith(tb, sim.New(5).Fast().Lossless())
	h.m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	for range 20 {
		h.advance(time.Second)
	}
	return h
}

// frameBudget is SPEC §6.2's: a frame renders in under 4 ms at 200×60.
const frameBudget = 4 * time.Millisecond

// frameCases are the frames worth timing: each screen as it animates, and the map while it moves,
// which redraws its static layer every frame instead of only the animations over it.
func frameCases(tb testing.TB) (*harness, []struct {
	name  string
	setup func()
	step  func()
}) {
	h := busyHarness(tb)
	type frameCase = struct {
		name  string
		setup func()
		step  func()
	}
	var cases []frameCase
	for i, name := range tabNames {
		key := fmt.Sprint(i + 1)
		cases = append(cases, frameCase{name: name, setup: func() {
			h.keys(key)
			if key == "3" {
				h.until("the map", func() bool { return h.m.mapv.world != nil })
			}
			h.advance(time.Second)
		}})
	}
	cases = append(cases, frameCase{name: "MAP moving", setup: func() { h.keys("3"); h.advance(time.Second) },
		step: func() { h.m.mapv.view.Center.Lon += 1e-4 }})
	return h, cases
}

func renderFrame(h *harness, step func()) {
	h.clk.t = h.clk.t.Add(motion.FrameDur)
	h.m.now = h.clk.t
	if step != nil {
		step()
	}
	_ = h.m.View()
}

func BenchmarkFrame(b *testing.B) {
	h, cases := frameCases(b)
	for _, c := range cases {
		c.setup()
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				renderFrame(h, c.step)
			}
		})
	}
}

// TestFrameBudget keeps every steady frame inside the budget. The moving map is only reported: it may
// take longer, and the map jumps instead of gliding where it can't keep up.
func TestFrameBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	h, cases := frameCases(t)
	for _, c := range cases {
		c.setup()
		took := make([]time.Duration, 31)
		for i := range took {
			start := time.Now()
			renderFrame(h, c.step)
			took[i] = time.Since(start)
		}
		slices.Sort(took)
		per := took[len(took)/2]
		switch {
		case c.step != nil:
			t.Logf("%s: %v a frame", c.name, per)
		case per > frameBudget:
			t.Errorf("%s takes %v a frame at 200×60, over the %v budget", c.name, per, frameBudget)
		}
	}
}
