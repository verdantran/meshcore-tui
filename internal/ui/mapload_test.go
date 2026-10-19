package ui

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

// TestMapCollidingHops: many placed repeaters sharing a first key byte, and long floods through that byte.
func TestMapCollidingHops(t *testing.T) {
	h := newHarness(t)
	self := h.m.ready.Self
	for i := range 300 {
		h.m.Update(eventMsg{radio.Heard{Contact: radio.Contact{Key: [32]byte{0xAA, byte(i), byte(i >> 8)}, Name: fmt.Sprint("R", i),
			Type: radio.TypeRepeater, Lat: self.Lat + float64(i%20)*0.01, Lon: self.Lon + float64(i/20)*0.01}}})
	}
	for k := range 30 {
		raw := advertPacket(t, fmt.Sprint("Mallory", k), bytes.Repeat([]byte{0xAA}, 63))
		start := time.Now()
		h.m.Update(eventMsg{radio.Packet{At: h.clk.t, SNR: 5, RSSI: -90, Raw: raw}})
		if took := time.Since(start); took > 50*time.Millisecond {
			t.Fatalf("advert %d took %v", k, took)
		}
	}
	if len(h.m.mapv.routes) != mapRoutes {
		t.Fatalf("%d routes", len(h.m.mapv.routes))
	}
	if n := len(h.m.mapv.flows); n == 0 || n > trafficKeep {
		t.Errorf("%d links remembered", n)
	}
	h.openMap()
	h.m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	if per := medianFrame(h); timed && per > frameBudget {
		t.Errorf("a map frame takes %v", per)
	}
}

// TestMapManyHeard: unique keys are cheap, so the heard list is capped and the map keeps its frame rate.
func TestMapManyHeard(t *testing.T) {
	h := newHarness(t)
	self := h.m.ready.Self
	start := time.Now()
	for i := range 5000 {
		h.clk.t = h.clk.t.Add(time.Millisecond)
		h.m.now = h.clk.t
		h.m.Update(eventMsg{radio.Heard{Contact: radio.Contact{Key: [32]byte{byte(i), byte(i >> 8), 0x55}, Name: fmt.Sprint("N", i),
			Type: radio.TypeRepeater, Lat: self.Lat + float64(i%70-35)*0.2, Lon: self.Lon + float64(i/70-35)*0.3}}})
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("hearing 5000 nodes took %v", took)
	}
	c := &h.m.contacts
	if len(c.heard) != heardMax || len(c.heardIdx) != heardMax {
		t.Fatalf("heard %d, indexed %d", len(c.heard), len(c.heardIdx))
	}
	if _, ok := c.heardIdx[[32]byte{0, 0, 0x55}]; !ok {
		t.Error("the first heard should stay")
	}
	if _, ok := c.heardIdx[[32]byte{byte(4999 % 256), byte(4999 >> 8), 0x55}]; !ok {
		t.Error("the latest heard should be there")
	}
	for i, n := range c.heard {
		if c.heardIdx[n.c.Key] != i {
			t.Fatalf("index out of step at %d", i)
		}
	}
	if len(c.arrived) > 2*heardMax {
		t.Errorf("%d entrances kept", len(c.arrived))
	}
	h.m.Update(eventMsg{radio.Heard{Contact: c.heard[0].c}})
	if len(c.heard) != heardMax {
		t.Error("hearing a node again doesn't add it twice")
	}
	h.openMap()
	h.m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	if per := medianFrame(h); timed && per > frameBudget {
		t.Errorf("a map frame takes %v", per)
	}
	h.m.mapv.view = h.m.mapv.view.Zoomed(3)
	if per := medianFrame(h); timed && per > frameBudget {
		t.Errorf("a map frame zoomed in takes %v", per)
	}
}

func medianFrame(h *harness) time.Duration {
	renderFrame(h, nil)
	took := make([]time.Duration, 15)
	for i := range took {
		start := time.Now()
		renderFrame(h, nil)
		took[i] = time.Since(start)
	}
	slices.Sort(took)
	return took[len(took)/2]
}
