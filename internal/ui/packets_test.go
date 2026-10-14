package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	meshcore "github.com/OwlShack/meshcore-go"

	"meshcore-tui/internal/radio"
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
			gt, err := (&meshcore.GroupTextPayload{Timestamp: 1760000000, Sender: sender, Text: text}).Encrypt(meshcore.DeriveChannelHash(ch.Secret), ch.Secret[:])
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := gt.ToBytes()
			return rawPacket(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpTxt, path, payload)
		}
	}
	t.Fatalf("no channel %s", channel)
	return nil
}

func TestInspector(t *testing.T) {
	h := newHarness(t)
	h.keys("4")
	if got := h.screen(); !strings.Contains(got, "Waiting for packets") {
		t.Fatalf("empty inspector:\n%s", got)
	}

	msg := channelPacket(t, h, "#bristol", "Carol", "Purdown is back", []byte{0xA3})
	h.hear(msg, 6.5)
	h.hear(rawPacket(t, meshcore.RouteTypeDirect, meshcore.PayloadTypeAck, []byte{0x1E}, []byte{0x33, 0x1A, 0x2E, 0x9F}), -2)
	// The same message heard again by another route: the first packet's bytes with a different path.
	p, _ := meshcore.PacketFromBytes(msg)
	p.Path, p.PathLength = []byte{0x77, 0x5F}, meshcore.MakePathLen(1, 2)
	again, _ := p.ToBytes()
	h.hear(again, -1.25)

	got := h.screen()
	for _, want := range []string{"PACKETS 2", "#bristol · Carol: Purdown is back", "ack 9F2E1A33", "GRP_TXT", "ACK"} {
		if !strings.Contains(got, want) {
			t.Fatalf("inspector missing %q:\n%s", want, got)
		}
	}
	h.keys("j") // the channel message, below the newer ack
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

	h.keys("F") // type: ADVERT
	if got := h.screen(); !strings.Contains(got, "Nothing matches") {
		t.Fatalf("type filter:\n%s", got)
	}
	for i := 1; i < len(typeChoices); i++ {
		h.keys("F")
	}
	h.keys("nn") // SNR ≥ 5: only the channel message's best copy qualifies
	if got := h.screen(); strings.Contains(got, "ack 9F2E1A33") || !strings.Contains(got, "Purdown") {
		t.Fatalf("signal filter:\n%s", got)
	}
	h.keys("n")

	h.key(tea.KeySpace)
	h.hear(rawPacket(t, meshcore.RouteTypeDirect, meshcore.PayloadTypeAck, nil, []byte{1, 2, 3, 4}), 3)
	if got := h.screen(); !strings.Contains(got, "PAUSED, 1 NEW") || strings.Contains(got, "ack 04030201") {
		t.Fatalf("paused:\n%s", got)
	}
	h.key(tea.KeySpace)
	if got := h.screen(); !strings.Contains(got, "ack 04030201") {
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
