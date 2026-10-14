package packets

import (
	"crypto/ed25519"
	"crypto/sha256"
	"strings"
	"testing"

	meshcore "github.com/OwlShack/meshcore-go"

	"meshcore-tui/internal/radio"
)

func build(t *testing.T, route, typ byte, path, payload []byte) []byte {
	t.Helper()
	p := &meshcore.Packet{Header: meshcore.MakeHeader(route, typ, 0), PathLength: meshcore.MakePathLen(1, uint8(len(path))), Path: path, Payload: payload}
	raw, err := p.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// checkSpans makes sure fields tile the packet in order without gaps or overlaps.
func checkSpans(t *testing.T, d Decoded) {
	t.Helper()
	at := 0
	for _, f := range d.Fields {
		if f.Start != at || f.End <= f.Start || f.End > len(d.Raw) {
			t.Fatalf("field %q spans [%d,%d), expected to start at %d (len %d)", f.Name, f.Start, f.End, at, len(d.Raw))
		}
		at = f.End
	}
	if at != len(d.Raw) {
		t.Fatalf("fields end at %d of %d bytes", at, len(d.Raw))
	}
}

var dundry = radio.Contact{Key: [32]byte{0xA3, 1}, Name: "Dundry"}
var kelston = radio.Contact{Key: [32]byte{0x5F, 1}, Name: "Kelston"}
var lansdown = radio.Contact{Key: [32]byte{0x5F, 2}, Name: "Lansdown"}

func TestAdvertSignedAndDecoded(t *testing.T) {
	seed := sha256.Sum256([]byte("node"))
	id := meshcore.NewLocalIdentityFromSeed([ed25519.SeedSize]byte(seed))
	adv, err := meshcore.NewAdvert(id, meshcore.AdvertAppData{Type: "REPEATER", Name: "Dundry", Lat: 51399000, Lon: -2639000, HasLocation: true}, 1760000000)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := adv.ToBytes()
	raw := build(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, []byte{0xA3, 0x5F}, payload)
	d := Decode(raw, Book{Contacts: []radio.Contact{dundry, kelston, lansdown}})
	if d.Err != nil || !d.SigOK || d.Name != "Dundry" || d.Lat != 51.399 {
		t.Fatalf("advert = %+v", d)
	}
	checkSpans(t, d)
	if !strings.Contains(d.Summary, "Dundry · repeater · 51.3990, -2.6390") {
		t.Errorf("summary = %q", d.Summary)
	}
	if path := d.Fields[2].Value; path != "Dundry → 5F (Kelston or Lansdown?)" {
		t.Errorf("path = %q", path)
	}
	// Tamper with the timestamp: the signature no longer holds.
	bad := append([]byte(nil), raw...)
	bad[len(raw)-len(payload)+33] ^= 0xFF
	if d := Decode(bad, Book{}); d.SigOK || !strings.Contains(d.Summary, "BAD SIGNATURE") {
		t.Errorf("tampered advert = %q", d.Summary)
	}
}

func TestChannelDecrypt(t *testing.T) {
	psk := meshcore.DeriveHashtagPSK("#bristol")
	gt, err := (&meshcore.GroupTextPayload{Timestamp: 1760000000, Sender: "Carol", Text: "Purdown is back"}).Encrypt(meshcore.DeriveChannelHash(psk), psk[:])
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := gt.ToBytes()
	raw := build(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpTxt, []byte{0xA3}, payload)

	with := Decode(raw, Book{Channels: []radio.Channel{{Idx: 1, Name: "#bristol", Secret: psk}}})
	if !with.Decrypted || with.Summary != "#bristol · Carol: Purdown is back" {
		t.Fatalf("with the key: %+v", with.Summary)
	}
	checkSpans(t, with)
	without := Decode(raw, Book{})
	if without.Decrypted || !strings.Contains(without.Summary, "no key") {
		t.Fatalf("without the key: %q", without.Summary)
	}
}

func TestOtherTypesAndMalformed(t *testing.T) {
	book := Book{Self: [32]byte{0xC0}, Contacts: []radio.Contact{dundry}}
	msg := build(t, meshcore.RouteTypeDirect, meshcore.PayloadTypeTxtMsg, []byte{0xA3}, append([]byte{0xC0, 0xA3, 1, 2}, make([]byte, 16)...))
	if d := Decode(msg, book); d.Summary != "message to me from Dundry · 16 B encrypted" {
		t.Errorf("text message = %q", d.Summary)
	} else {
		checkSpans(t, d)
	}
	ack := build(t, meshcore.RouteTypeDirect, meshcore.PayloadTypeAck, nil, []byte{0x33, 0x1A, 0x2E, 0x9F})
	if d := Decode(ack, book); d.Summary != "ack 9F2E1A33" {
		t.Errorf("ack = %q", d.Summary)
	} else {
		checkSpans(t, d)
	}
	tr, _ := (&meshcore.Trace{Tag: 7, PathHashes: []byte{0xA3, 0xC0}}).ToBytes()
	if d := Decode(build(t, meshcore.RouteTypeDirect, meshcore.PayloadTypeTrace, nil, tr), book); d.Summary != "trace via Dundry → me" {
		t.Errorf("trace = %q", d.Summary)
	} else {
		checkSpans(t, d)
	}
	if d := Decode([]byte{0x15}, book); d.Err == nil || !strings.HasPrefix(d.Summary, "malformed") {
		t.Errorf("one byte = %+v", d)
	}
	// Same payload by two routes: the packet hash ignores the path, so duplicates group.
	a := Decode(build(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeAck, []byte{0xA3}, []byte{1, 2, 3, 4}), book)
	b := Decode(build(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeAck, []byte{0x5F, 0xA3}, []byte{1, 2, 3, 4}), book)
	if a.Hash != b.Hash {
		t.Error("the same packet heard by two routes should share a hash")
	}
}

func TestEveryPayloadType(t *testing.T) {
	book := Book{Self: [32]byte{0xC0}, Contacts: []radio.Contact{dundry}}
	addressed := append([]byte{0xA3, 0xC0, 9, 9}, make([]byte, 16)...)
	anon := append(append([]byte{0xA3}, make([]byte, 32)...), append([]byte{1, 2}, make([]byte, 16)...)...)
	discover := (meshcore.NewDiscoverRequest(meshcore.DiscoverRequest{Tag: 1}))
	discoverBytes, _ := discover.ToBytes()
	reply := meshcore.NewDiscoverResponse(meshcore.DiscoverResponse{Tag: 1}, true)
	replyBytes, _ := reply.ToBytes()
	for _, c := range []struct {
		route, typ byte
		payload    []byte
		want       string
	}{
		{meshcore.RouteTypeFlood, meshcore.PayloadTypePath, addressed, "returned path to Dundry from me · 16 B encrypted"},
		{meshcore.RouteTypeDirect, meshcore.PayloadTypeReq, addressed, "request to Dundry from me · 16 B encrypted"},
		{meshcore.RouteTypeDirect, meshcore.PayloadTypeResponse, addressed, "response to Dundry from me · 16 B encrypted"},
		{meshcore.RouteTypeFlood, meshcore.PayloadTypeAnonReq, anon, "anonymous request to Dundry"},
		{meshcore.RouteTypeDirect, meshcore.PayloadTypeControl, discoverBytes, "neighbour discovery request"},
		{meshcore.RouteTypeDirect, meshcore.PayloadTypeControl, replyBytes, "neighbour discovery reply"},
		{meshcore.RouteTypeDirect, meshcore.PayloadTypeControl, []byte{0x30, 1}, "control subtype 3"},
		{meshcore.RouteTypeDirect, meshcore.PayloadTypeMultiPart, []byte{1, 2, 3}, "multi_part · 3 B"},
		{meshcore.RouteTypeDirect, meshcore.PayloadTypeRawCustom, []byte{1, 2}, "raw_custom · 2 B"},
	} {
		d := Decode(build(t, c.route, c.typ, nil, c.payload), book)
		if d.Summary != c.want {
			t.Errorf("%s: %q, want %q", d.Type, d.Summary, c.want)
			continue
		}
		checkSpans(t, d)
	}
}

func TestMalformedPayloads(t *testing.T) {
	for typ, want := range map[byte]string{
		meshcore.PayloadTypeAdvert:  "advert, malformed",
		meshcore.PayloadTypeGrpTxt:  "channel message, malformed",
		meshcore.PayloadTypeTxtMsg:  "message (truncated)",
		meshcore.PayloadTypeTrace:   "trace, malformed",
		meshcore.PayloadTypeAnonReq: "anonymous request, malformed",
	} {
		d := Decode(build(t, meshcore.RouteTypeFlood, typ, nil, []byte{1}), Book{})
		if !strings.HasPrefix(d.Summary, want) {
			t.Errorf("type %d: %q, want %q", typ, d.Summary, want)
		}
	}
	if d := Decode(build(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeControl, nil, nil), Book{}); d.Summary != "control, malformed" {
		t.Errorf("empty control = %q", d.Summary)
	}
}

func TestChannelEdgeCases(t *testing.T) {
	psk := meshcore.DeriveHashtagPSK("#bristol")
	hash := meshcore.DeriveChannelHash(psk)
	// Group data on a known channel isn't text, so it isn't decrypted.
	data := build(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpData, nil, append([]byte{hash, 1, 2}, make([]byte, 16)...))
	if d := Decode(data, Book{Channels: []radio.Channel{{Name: "#bristol", Secret: psk}}}); d.Summary != "#bristol · 16 B of data" {
		t.Errorf("group data = %q", d.Summary)
	}
	// A matching hash with the wrong secret (a hash collision) can't decrypt.
	junk := build(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpTxt, nil, append([]byte{hash, 1, 2}, make([]byte, 16)...))
	if d := Decode(junk, Book{Channels: []radio.Channel{{Name: "#bristol", Secret: psk}}}); d.Decrypted || d.Summary != "#bristol · 16 B, can't decrypt" {
		t.Errorf("collision = %q", d.Summary)
	}
	// Transport-routed packets carry region codes.
	p := &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeTransportFlood, meshcore.PayloadTypeAck, 0), TransportCode1: 0x1234,
		PathLength: meshcore.MakePathLen(2, 1), Path: []byte{0xA3, 0x01}, Payload: []byte{1, 2, 3, 4}}
	raw, _ := p.ToBytes()
	d := Decode(raw, Book{})
	if !d.HasTransport || d.Fields[1].Name != "transport codes" || d.HashSize != 2 || d.Fields[3].Value != "A3 01" {
		t.Errorf("transport packet = %+v", d.Fields)
	}
	checkSpans(t, d)
	// An advert with no name, type or location still decodes.
	seed := sha256.Sum256([]byte("anon"))
	adv, _ := meshcore.NewAdvert(meshcore.NewLocalIdentityFromSeed([ed25519.SeedSize]byte(seed)), meshcore.AdvertAppData{}, 1)
	payload, _ := adv.ToBytes()
	if d := Decode(build(t, meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, nil, payload), Book{}); d.Summary != "unnamed · unknown type" {
		t.Errorf("bare advert = %q", d.Summary)
	}
}
