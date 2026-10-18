package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

func rawPacket(t *testing.T, route, typ byte, path, payload []byte) []byte {
	t.Helper()
	p := &meshcore.Packet{Header: meshcore.MakeHeader(route, typ, 0), PathLength: meshcore.MakePathLen(1, uint8(len(path))), Path: path, Payload: payload}
	raw, err := p.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (h *harness) hear(raw []byte, snr float32) {
	_, cmd := h.m.Update(eventMsg{radio.Packet{At: h.clk.t, SNR: snr, RSSI: -90, Raw: raw}})
	h.start(cmd)
	h.settle()
}

func channelPacket(t *testing.T, h *harness, channel, sender, text string, path []byte) []byte {
	t.Helper()
	for _, ch := range h.m.chat.Channels() {
		if ch.Name == channel {
			return secretPacket(t, ch.Secret, sender, text, path)
		}
	}
	t.Fatalf("no channel %s", channel)
	return nil
}

func secretPacket(t *testing.T, secret [16]byte, sender, text string, path []byte) []byte {
	t.Helper()
	gt, err := (&meshcore.GroupTextPayload{Timestamp: 1760000000, Sender: sender, Text: text}).Encrypt(meshcore.DeriveChannelHash(secret), secret[:])
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := gt.ToBytes()
	return rawPacket(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpTxt, path, payload)
}

// A private channel named like an open one is still private: what it carries never reaches Packets, the
// ticker, the map, Stats or an export.
func TestPrivateChannelsNamedLikeOpenOnesStayHidden(t *testing.T) {
	h := newHarness(t)
	fakePublic := radio.Channel{Idx: 5, Name: "Public", Secret: [16]byte{5, 5, 1, 2, 3}}
	fakeBristol := radio.Channel{Idx: 6, Name: "#bristol", Secret: [16]byte{6, 6, 1, 2, 3}}
	h.m.chat.ReplaceChannels(append(h.m.chat.Channels(), fakePublic, fakeBristol))
	h.keys("4")
	h.hear(secretPacket(t, fakePublic.Secret, "Mum", "the key's under the mat", []byte{0xA3}), 5)
	h.hear(secretPacket(t, fakeBristol.Secret, "Carol", "meet at the back door", []byte{0xA3}), 5)

	if scr := h.screen(); !strings.Contains(scr, "PACKETS 0 · 2 OTHERS NOT SHOWN") || strings.Contains(scr, "mat") || strings.Contains(scr, "back door") {
		t.Fatalf("private messages shown:\n%s", scr)
	}
	if len(h.m.ticker.items) != 0 || len(h.m.mapv.routes) != 0 {
		t.Fatalf("private messages reached the ticker (%d) or the map (%d)", len(h.m.ticker.items), len(h.m.mapv.routes))
	}
	h.hear(channelPacket(t, h, "Public", "Bob", "hello all", nil), 5)
	h.keys(":export packets")
	h.key(tea.KeyEnter)
	files, _ := filepath.Glob(filepath.Join(h.m.opt.ExportDir, "packets-*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("export files = %v", files)
	}
	if data, _ := os.ReadFile(files[0]); strings.Count(string(data), "\n") != 1 || !strings.Contains(string(data), "hello all") {
		t.Fatalf("export:\n%s", data)
	}
	h.keys("7")
	if scr := h.screen(); !strings.Contains(scr, "(private channels)") || strings.Contains(scr, "#bristol") {
		t.Errorf("Stats names only open channels:\n%s", scr)
	}
}

// TestMadeUpPathsDontCrowdOut: copies with invented paths sent first leave room for the real ones that follow,
// those passed on by a neighbour first, then the strongest.
func TestMadeUpPathsDontCrowdOut(t *testing.T) {
	h := newHarness(t)
	h.m.contacts.heard = append(h.m.contacts.heard, heardNode{radio.Contact{Key: [32]byte{0xA3, 0x01}, Name: "Hill", PathLen: 0}, h.clk.t})
	p, _ := meshcore.PacketFromBytes(channelPacket(t, h, "Public", "Bob", "again", nil))
	hear := func(path []byte, snr float32) {
		p.Path, p.PathLength = path, meshcore.MakePathLen(1, uint8(len(path)))
		raw, _ := p.ToBytes()
		h.m.heardPacket(radio.Packet{At: h.clk.t, SNR: snr, Raw: raw})
	}
	for i := range 200 {
		hear([]byte{0x10, byte(i)}, 9)
	}
	hear([]byte{0x5F, 0xA3}, 1)
	hear([]byte{0x77, 0xA3}, 3)
	hear([]byte{0x5F, 0xA3}, 4) // the same way again
	hear([]byte{0x10, 0xEE}, 12)
	row := h.m.inspector.rows[0]
	if len(row.copies) != packetCopies || row.heard() != 204 {
		t.Fatalf("copies kept %d, heard %d", len(row.copies), row.heard())
	}
	paths := map[string]float32{}
	for i, c := range row.copies {
		paths[string(c.path)] = c.snr
		if i < firstCopies && c.path[1] != byte(i) {
			t.Errorf("copy %d is %x: the first heard stay", i, c.path)
		}
	}
	if paths["\x5F\xA3"] != 1 || paths["\x77\xA3"] != 3 {
		t.Error("the copies passed on by a neighbour are kept")
	}
	if _, ok := paths["\x10\xEE"]; !ok {
		t.Error("the strongest copy is kept")
	}
	h.m.showPacketPath(row)
	if p := h.m.mapv.path; p == nil || len(p.routes) != packetCopies {
		t.Fatalf("map routes: %+v", p)
	}
}

// A packet replayed by many routes keeps 32 copies, and counts the rest.
func TestReplaysAreCapped(t *testing.T) {
	h := newHarness(t)
	h.keys("4")
	p, _ := meshcore.PacketFromBytes(channelPacket(t, h, "Public", "Bob", "again", nil))
	for i := range 100_000 {
		p.Path, p.PathLength = []byte{byte(i), byte(i >> 8)}, meshcore.MakePathLen(1, 2)
		raw, _ := p.ToBytes()
		h.m.heardPacket(radio.Packet{At: h.clk.t, SNR: 1, Raw: raw})
	}
	row := h.m.inspector.rows[0]
	if len(h.m.inspector.rows) != 1 || len(row.copies) != packetCopies || row.heard() != 100_000 {
		t.Fatalf("rows %d, copies kept %d, heard %d", len(h.m.inspector.rows), len(row.copies), row.heard())
	}
	start := time.Now()
	scr := h.screen()
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("a frame took %v", d)
	}
	if !strings.Contains(scr, "heard 100000 times") || !strings.Contains(scr, "heard 100000 times, 32 kept") || !strings.Contains(scr, "99+") {
		t.Errorf("the count of replays is shown:\n%s", scr)
	}
}

func TestInspector(t *testing.T) {
	h := newHarness(t)
	h.keys("4")
	if got := h.screen(); !strings.Contains(got, "Waiting for adverts, and messages on Public") {
		t.Fatalf("empty inspector:\n%s", got)
	}

	msg := channelPacket(t, h, "#bristol", "Carol", "Purdown is back", []byte{0xA3})
	h.hear(msg, 6.5)
	h.hear(channelPacket(t, h, "Public", "Bob", "hello all", []byte{0x1E}), -2)
	// The same message heard again by another route: the first packet's bytes with a different path.
	p, _ := meshcore.PacketFromBytes(msg)
	p.Path, p.PathLength = []byte{0x77, 0x5F}, meshcore.MakePathLen(1, 2)
	again, _ := p.ToBytes()
	h.hear(again, -1.25)

	got := h.screen()
	for _, want := range []string{"PACKETS 2", "#bristol · Carol: Purdown is back", "Public · Bob: hello all", "GRP_TXT"} {
		if !strings.Contains(got, want) {
			t.Fatalf("inspector missing %q:\n%s", want, got)
		}
	}
	h.keys("j") // the #bristol message, below the newer Public one
	got = h.screen()
	for _, want := range []string{"heard 2 times", "A3 (+6.5)", "77·5F (-1.2)", "ciphertext, decrypted", "0000"} {
		if !strings.Contains(got, want) {
			t.Fatalf("detail missing %q:\n%s", want, got)
		}
	}
	h.keys("]]")
	if !strings.Contains(h.screen(), "▸ path") {
		t.Fatalf("] should step to the path field:\n%s", h.screen())
	}

	h.keys("++") // SNR ≥ 5: only the #bristol message's best copy qualifies
	if got := h.screen(); strings.Contains(got, "hello all") || !strings.Contains(got, "Purdown") {
		t.Fatalf("signal filter:\n%s", got)
	}
	h.keys("--")

	h.key(tea.KeySpace)
	h.hear(channelPacket(t, h, "Public", "Dan", "late arrival", nil), 3)
	if got := h.screen(); !strings.Contains(got, "PAUSED, 1 NEW") || strings.Contains(got, "late arrival") {
		t.Fatalf("paused:\n%s", got)
	}
	h.key(tea.KeySpace)
	if got := h.screen(); !strings.Contains(got, "late arrival") {
		t.Fatalf("resumed:\n%s", got)
	}

	h.keys(":export packets")
	h.key(tea.KeyEnter)
	files, _ := filepath.Glob(filepath.Join(h.m.opt.ExportDir, "packets-*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("export files = %v", files)
	}
	data, _ := os.ReadFile(files[0])
	if lines := strings.Count(string(data), "\n"); lines != 3 || !strings.Contains(string(data), `"decrypted":"Carol: Purdown is back"`) {
		t.Fatalf("export:\n%s", data)
	}

	for _, size := range [][2]int{{80, 24}, {200, 60}} {
		h.m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		h.screen()
	}
}

// Only messages on open channels are kept; the rest are counted, blanked in the ticker, and tallied for Stats.
func TestInspectorShowsOnlyAdvertsAndOpenChannels(t *testing.T) {
	h := newHarness(t)
	h.keys("4")
	self := h.m.ready.Self.PubKey[0]
	alice := h.contact("Alice").Key[0]
	toUs := rawPacket(t, meshcore.RouteTypeDirect, meshcore.PayloadTypeTxtMsg, []byte{0xA3, 0x1E}, append([]byte{self, alice, 9, 9}, make([]byte, 16)...))
	h.hear(toUs, 4)
	h.hear(toUs, 2) // a repeat isn't counted twice
	h.hear(rawPacket(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpTxt, nil, []byte{0xEE, 1, 2, 3, 4, 5}), 1)
	h.hear(rawPacket(t, meshcore.RouteTypeDirect, meshcore.PayloadTypeAck, nil, []byte{5, 6, 7, 8}), 1)
	h.hear([]byte{0x15}, 0)

	// A private channel we're in isn't shown either: only Public and hashtag channels are open to anyone.
	h.typeChannel("n", "Family", "000102030405060708090a0b0c0d0e0f")
	h.keys("4")
	h.hear(channelPacket(t, h, "Family", "Mum", "tea's ready", nil), 6)

	scr := h.screen()
	if !strings.Contains(scr, "PACKETS 0 · 5 OTHERS NOT SHOWN") || !strings.Contains(scr, "Waiting for adverts") {
		t.Fatalf("others hidden:\n%s", scr)
	}
	if len(h.m.ticker.items) != 0 {
		t.Errorf("hidden packets don't scroll past: %+v", h.m.ticker.items)
	}
	h.hear(channelPacket(t, h, "#bristol", "Carol", "morning", []byte{0xA3}), 5)
	h.hear(advertPacket(t, "Purdown", []byte{0xA3}), 5)
	if scr := h.screen(); !strings.Contains(scr, "PACKETS 2 · 5 OTHERS NOT SHOWN") || !strings.Contains(scr, "#bristol · Carol: morning") ||
		!strings.Contains(scr, "Purdown · repeater") {
		t.Fatalf("an advert and an open channel's message are shown:\n%s", scr)
	}
	if len(h.m.ticker.items) != 2 || strings.Contains(h.m.ticker.items[0].text, "··") {
		t.Errorf("shown packets' bytes scroll past: %+v", h.m.ticker.items)
	}
	if h.m.inspector.mesh.packets != 7 {
		t.Errorf("the mesh tally counts every packet's routing: %+v", h.m.inspector.mesh)
	}
	h.keys("Cy")
	if h.m.inspector.mesh.packets != 7 || len(h.m.inspector.rows) != 0 {
		t.Error("clearing the list keeps the Stats tally")
	}
	h.keys("7")
	if !strings.Contains(h.screen(), "7 packets") || !strings.Contains(h.screen(), "(unknown channel)") {
		t.Errorf("stats:\n%s", h.screen())
	}
}

func (h *harness) typeChannel(key, name, secret string) {
	h.t.Helper()
	h.keys("1")
	h.keys(key)
	h.typeLine(name)
	h.typeFast(secret)
	h.key(tea.KeyEnter)
	h.until(name, func() bool { _, ok := h.channel(name); return ok })
}
