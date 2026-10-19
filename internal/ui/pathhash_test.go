package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/charmbracelet/x/ansi"

	"github.com/verdantran/meshcore-tui/internal/packets"
	"github.com/verdantran/meshcore-tui/internal/radio"
	"github.com/verdantran/meshcore-tui/internal/sim"
)

// twoByteHarness is the demo mesh with the radio set to 2-byte path hashes. Kelston and Lansdown share their
// first key byte, 5F, but not their second.
func twoByteHarness(t *testing.T) *harness {
	s := sim.New(5).Fast().Lossless().Quiet()
	if err := s.SetPathHashMode(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, s)
	if h.m.pathHash() != 2 {
		t.Fatalf("path hash = %d bytes, want the radio's 2", h.m.pathHash())
	}
	return h
}

func channelPacketSized(t *testing.T, h *harness, text string, size int, hops ...[]byte) []byte {
	t.Helper()
	var secret [16]byte
	for _, ch := range h.m.chat.Channels() {
		if ch.Name == "Public" {
			secret = ch.Secret
		}
	}
	gt, err := (&meshcore.GroupTextPayload{Timestamp: 1760000000, Sender: "Bob", Text: text}).Encrypt(meshcore.DeriveChannelHash(secret), secret[:])
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := gt.ToBytes()
	p := &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpTxt, 0),
		PathLength: meshcore.MakePathLen(uint8(size), uint8(len(hops))), Path: slices.Concat(hops...), Payload: payload}
	raw, err := p.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (h *harness) busiest() string {
	return ansi.Strip(strings.Join(h.m.meshLines(120), "\n"))
}

// Busiest repeaters counts a 2-byte hop as itself, so Lansdown isn't taken for Kelston, which shares its
// first byte. A 1-byte 5F is either; where they are makes one the likelier, and it's marked as a guess.
func TestBusiestRepeatersByWholeHash(t *testing.T) {
	h := twoByteHarness(t)
	kelston, lansdown := h.contact("Kelston").Key, h.contact("Lansdown").Key
	for i := range 3 {
		h.hear(channelPacketSized(t, h, fmt.Sprint("two bytes ", i), 2, lansdown[:2]), 4)
	}
	h.hear(channelPacketSized(t, h, "one more", 2, kelston[:2]), 4)
	got := h.busiest()
	top := fmt.Sprintf("%X Lansdown", lansdown[:2])
	if !strings.Contains(got, top+" ") || !strings.Contains(got, fmt.Sprintf("%X Kelston", kelston[:2])) {
		t.Fatalf("busiest repeaters:\n%s", got)
	}
	if strings.Index(got, top) > strings.Index(got, "Kelston") || strings.Contains(got, "Kelston or Lansdown") {
		t.Errorf("Lansdown carried more, and each hop names one repeater:\n%s", got)
	}

	for i := range 5 {
		h.hear(channelPacketSized(t, h, fmt.Sprint("one byte ", i), 1, []byte{0x5F}), 4)
	}
	if got := h.busiest(); !strings.Contains(got, "5F Kelston?") {
		t.Errorf("an ambiguous 1-byte hop is the likelier, marked as a guess:\n%s", got)
	}
}

func TestBusiestRepeatersKeepsToABound(t *testing.T) {
	tally := newMeshTally()
	for i := range maxHopHashes + 50 {
		tally.hop(packets.Hop{Hash: []byte{byte(i >> 8), byte(i)}})
	}
	tally.hop(packets.Hop{Hash: []byte{0, 0}})
	if len(tally.hashes) != maxHopHashes || tally.hashes[string([]byte{0, 0})] == nil {
		t.Errorf("%d hashes kept, want %d with the busiest", len(tally.hashes), maxHopHashes)
	}
}

// A trace, the route editor and the contacts table all use the radio's 2-byte hashes.
func TestTwoByteTraceAndRoutes(t *testing.T) {
	h := twoByteHarness(t)
	dundry := h.contact("Dundry")
	h.keys("2")
	if got := h.screen(); !strings.Contains(got, fmt.Sprintf("%X", dundry.Key[:2])) {
		t.Errorf("the key column shows 2-byte hashes:\n%s", got)
	}
	h.pick("Dundry")
	h.keys("t")
	h.until("trace result", func() bool {
		run := h.m.contacts.traces[h.m.contacts.lastTrace[dundry.Key]]
		return run != nil && run.result != nil
	})
	run := h.m.contacts.traces[h.m.contacts.lastTrace[dundry.Key]]
	if run.size != 2 || string(run.path) != string(dundry.Key[:2]) || run.result.HashSize != 2 {
		t.Errorf("trace = %X at %d bytes, result %+v", run.path, run.size, run.result)
	}
	if got := h.screen(); !strings.Contains(got, fmt.Sprintf("→ %X", dundry.Key[:2])) {
		t.Errorf("trace hops:\n%s", got)
	}
	if _, ok := h.m.linkSNR(h.m.ready.Self.PubKey[:], dundry.Key[:]); !ok {
		t.Error("the trace teaches the link from us to Dundry")
	}

	h.key(tea.KeyEscape)
	h.pick("Bob")
	h.keys("p") // Bob's stored route is A3·1E in 1-byte hashes; each names one repeater, so it's recut to 2
	if c := h.m.contacts; c.editSize != 2 || len(c.editPath) != 4 || string(c.editPath[:2]) != string(dundry.Key[:2]) {
		t.Fatalf("editor starts from %X at %d bytes", c.editPath, c.editSize)
	}
	if got := h.screen(); strings.Contains(got, "shares its hash") {
		t.Errorf("no two repeaters share a 2-byte hash:\n%s", got)
	}
	h.key(tea.KeyBackspace)
	h.key(tea.KeyBackspace)
	h.key(tea.KeyEnter) // Aust
	h.keys("j")
	h.key(tea.KeyEnter) // Blackdown
	h.keys("s")
	aust, blackdown := h.contact("Aust").Key, h.contact("Blackdown").Key
	h.until("route saved", func() bool {
		c := h.contact("Bob")
		return c.PathLen == 2 && c.HashSize == 2 && string(c.Path) == string(append(aust[:2:2], blackdown[:2]...))
	})

	// A 1-byte hop more than one repeater could be can't be lengthened, so the route keeps its size.
	if path, size := h.m.rehop([]byte{0xA3, 0x5F}, 1, 2); size != 1 || string(path) != "\xA3\x5F" {
		t.Errorf("rehop = %X at %d bytes", path, size)
	}
}

func TestTracePathSizes(t *testing.T) {
	rep := radio.Contact{Key: [32]byte{0xA3, 0x01, 0x02}, Type: radio.TypeRepeater, PathLen: -1}
	if p, size, _ := tracePath(rep, 3); size != 2 || string(p) != "\xA3\x01" {
		t.Errorf("3-byte radio traces with %d-byte hops: %X", size, p)
	}
	routed := rep
	routed.PathLen, routed.Path, routed.HashSize = 2, []byte{0x1E, 0x5F}, 1
	if p, size, _ := tracePath(routed, 2); size != 1 || string(p) != "\x1E\x5F\xA3\x5F\x1E" {
		t.Errorf("a 1-byte route traces with %d-byte hops: %X", size, p)
	}
}

// Link signal is kept by whole hashes: a reading for Kelston isn't Lansdown's.
func TestLinkSignalByWholeHash(t *testing.T) {
	h := twoByteHarness(t)
	self := h.m.ready.Self.PubKey[:]
	kelston, lansdown := h.contact("Kelston").Key, h.contact("Lansdown").Key
	h.m.Update(eventMsg{radio.TraceResult{Hashes: kelston[:2], HashSize: 2, SNRs: []float32{4}}})
	if snr, ok := h.m.linkSNR(self, kelston[:]); !ok || snr != 4 {
		t.Errorf("Kelston's link = %v %v", snr, ok)
	}
	if _, ok := h.m.linkSNR(self, lansdown[:]); ok {
		t.Error("Lansdown shares Kelston's first byte, not its link")
	}
	if _, ok := h.m.linkSNR(self, []byte{0x5F}); !ok {
		t.Error("a 1-byte hash could be Kelston")
	}
	for i := range linkKeep + 10 {
		h.m.noteLink(self, []byte{byte(i >> 8), byte(i), 1}, 1)
	}
	if len(h.m.mapv.links) != linkKeep {
		t.Errorf("%d link readings kept, want %d", len(h.m.mapv.links), linkKeep)
	}
}
