package ui

import (
	"testing"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

// TestAdminNodesCached: the list is worked out once per change to the contacts or the filter, and follows both.
func TestAdminNodesCached(t *testing.T) {
	h := newHarness(t)
	a, b := h.m.adminNodes(), h.m.adminNodes()
	if len(a) < 2 || &a[0] != &b[0] {
		t.Fatalf("rebuilt with nothing changed: %d nodes", len(a))
	}
	last := a[len(a)-1]
	last.Name, last.Flags = "aaa first", last.Flags|radio.FlagFavourite
	h.m.chat.ContactUpdated(last)
	if got := h.m.adminNodes(); got[0].Key != last.Key || got[0].Name != "aaa first" {
		t.Errorf("a change to a contact isn't seen: %v", got[0].Name)
	}
	h.m.adm.favOnly = true
	if got := h.m.adminNodes(); len(got) != 1 || got[0].Key != last.Key {
		t.Errorf("favourites only: %d nodes", len(got))
	}
	if h.m.chat.ContactCount() != len(h.m.chat.Contacts()) {
		t.Error("contact count")
	}
}

// TestMapNodesOncePerFrame: drawing reuses one list, and what a resolver adds to it never reaches the next caller.
func TestMapNodesOncePerFrame(t *testing.T) {
	h := newHarness(t)
	h.m.mapv.drawing = true
	a := h.m.mapNodes()
	a = append(a, a[0])
	b := h.m.mapNodes()
	if len(b) != len(a)-1 || &a[0] == &b[0] {
		t.Error("an append wrote into the shared list")
	}
	if c := h.m.mapNodes(); &c[0] != &b[0] {
		t.Error("rebuilt within a frame")
	}
	h.m.View()
	if h.m.mapv.drawing || h.m.mapv.nodesSeen != nil {
		t.Error("the frame's list outlives the frame")
	}
}
