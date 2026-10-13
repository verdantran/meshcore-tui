package ui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/sim"
	"meshcore-tui/internal/store"
	"meshcore-tui/internal/theme"
)

// TestDemoNotifies runs the real simulator through the model, as --demo does.
func TestDemoNotifies(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	s := sim.New(1).Fast()
	m := New(Options{Theme: theme.Neon, Motion: motion.Full, Store: st,
		Dial:  func(Target) radio.Radio { return s },
		Start: &Target{Kind: "SIM", Addr: "demo"},
		Env:   func(k string) string { return map[string]string{"TERM_PROGRAM": "ghostty"}[k] }})
	m.Init()
	defer s.Close()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("no notification queued; pending=%v phase=%v", m.notes.pending, m.phase)
		case e := <-s.Events():
			m.Update(eventMsg{e})
			if len(m.notes.pending) > 0 {
				got := flushed(m)
				if got == "" || got[0] != '\a' {
					t.Fatalf("flush wrote %q", got)
				}
				t.Logf("demo notification: %q", got)
				return
			}
		}
	}
}
