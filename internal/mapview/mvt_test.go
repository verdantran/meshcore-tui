package mapview

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"math"
	"testing"
)

// A small vector-tile writer, to build test tiles.

type pw struct{ b []byte }

func (w *pw) varint(v uint64)     { w.b = binary.AppendUvarint(w.b, v) }
func (w *pw) key(field, wire int) { w.varint(uint64(field<<3 | wire)) }
func (w *pw) bytes(field int, b []byte) {
	w.key(field, 2)
	w.varint(uint64(len(b)))
	w.b = append(w.b, b...)
}
func (w *pw) packed(field int, vs []uint32) {
	var in pw
	for _, v := range vs {
		in.varint(uint64(v))
	}
	w.bytes(field, in.b)
}

type testFeature struct {
	typ   featureType
	props map[string]any
	parts [][]tilePoint // polygon rings must be closed by the caller's winding; ClosePath is added
}

func zig(v int32) uint32 { return uint32((v << 1) ^ (v >> 31)) }

func encodeGeom(f testFeature) []uint32 {
	var out []uint32
	var x, y int32
	for _, part := range f.parts {
		for i, p := range part {
			dx, dy := int32(p[0])-x, int32(p[1])-y
			x, y = int32(p[0]), int32(p[1])
			switch {
			case i == 0:
				out = append(out, 1|1<<3, zig(dx), zig(dy))
			case i == 1:
				out = append(out, 2|uint32(len(part)-1)<<3, zig(dx), zig(dy))
			default:
				out = append(out, zig(dx), zig(dy))
			}
		}
		if f.typ == mvtPolygon {
			out = append(out, 7|1<<3)
		}
	}
	return out
}

func encodeLayer(name string, feats []testFeature) []byte {
	var l pw
	l.bytes(1, []byte(name))
	var keys []string
	var values []any
	index := func(list *[]string, s string) uint32 {
		for i, k := range *list {
			if k == s {
				return uint32(i)
			}
		}
		*list = append(*list, s)
		return uint32(len(*list) - 1)
	}
	for _, f := range feats {
		var fw pw
		var tags []uint32
		for k, v := range f.props {
			tags = append(tags, index(&keys, k))
			values = append(values, v)
			tags = append(tags, uint32(len(values)-1))
		}
		fw.packed(2, tags)
		fw.key(3, 0)
		fw.varint(uint64(f.typ))
		fw.packed(4, encodeGeom(f))
		l.bytes(2, fw.b)
	}
	for _, k := range keys {
		l.bytes(3, []byte(k))
	}
	for _, v := range values {
		var vw pw
		switch v := v.(type) {
		case string:
			vw.bytes(1, []byte(v))
		case float64:
			vw.key(3, 1)
			vw.b = binary.LittleEndian.AppendUint64(vw.b, math.Float64bits(v))
		case bool:
			vw.key(7, 0)
			vw.varint(map[bool]uint64{true: 1}[v])
		case int:
			vw.key(6, 0)
			vw.varint(uint64(zig(int32(v))))
		}
		l.bytes(4, vw.b)
	}
	l.key(5, 0)
	l.varint(4096)
	l.key(15, 0)
	l.varint(2)
	return l.b
}

func encodeTile(layers map[string][]testFeature) []byte {
	var t pw
	for _, name := range []string{"water", "waterway", "transportation", "place", "building"} {
		if f, ok := layers[name]; ok {
			t.bytes(3, encodeLayer(name, f))
		}
	}
	return t.b
}

func square(x0, y0, x1, y1 float64) []tilePoint {
	return []tilePoint{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}} // clockwise with y down: positive area, so exterior
}

// sampleTile has sea down the west half reaching past the tile's edges, a pond, a big lake with an island,
// a river, a motorway and a minor road, a city and a hamlet, and a building layer the map ignores.
func sampleTile() []byte {
	island := []tilePoint{{1000, 2600}, {1000, 2800}, {1200, 2800}, {1200, 2600}}
	return encodeTile(map[string][]testFeature{
		"water": {
			{mvtPolygon, map[string]any{"class": "ocean"}, [][]tilePoint{square(-64, -64, 1500, 4160)}},
			{mvtPolygon, map[string]any{"class": "pond"}, [][]tilePoint{square(3000, 100, 3050, 150)}},
			{mvtPolygon, map[string]any{"class": "lake"}, [][]tilePoint{square(2000, 2000, 2010, 2010)}},
			{mvtPolygon, map[string]any{"class": "lake"}, [][]tilePoint{square(800, 2400, 1400, 3000), island}},
		},
		"waterway":       {{mvtLine, map[string]any{"class": "river"}, [][]tilePoint{{{1500, 2000}, {4096, 2100}}}}},
		"transportation": {{mvtLine, map[string]any{"class": "motorway"}, [][]tilePoint{{{1600, 0}, {1700, 4096}}}}, {mvtLine, map[string]any{"class": "minor"}, [][]tilePoint{{{0, 0}, {10, 10}}}}},
		"place": {
			{mvtPoint, map[string]any{"class": "city", "name": "東京", "name:latin": "Tokyo", "rank": 1, "capital": true, "pop": 1.5e7}, [][]tilePoint{{{2048, 2048}}}},
			{mvtPoint, map[string]any{"class": "hamlet", "name": "Nowhere"}, [][]tilePoint{{{100, 100}}}},
		},
		"building": {{mvtPolygon, map[string]any{}, [][]tilePoint{square(1, 1, 2, 2)}}},
	})
}

func TestDecodeTile(t *testing.T) {
	layers, err := decodeTile(sampleTile())
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 5 || layers[0].Name != "water" || layers[0].Extent != 4096 {
		t.Fatalf("layers = %+v", layers)
	}
	lake := layers[0].Features[3]
	if len(lake.Parts) != 2 || !lake.Outer[0] || lake.Outer[1] {
		t.Errorf("lake rings %v outer %v", len(lake.Parts), lake.Outer)
	}
	city := layers[3].Features[0]
	if city.Props["name:latin"] != "Tokyo" || city.Props["capital"] != true || city.Props["rank"] != 1.0 || city.Parts[0][0] != (tilePoint{2048, 2048}) {
		t.Errorf("city = %+v", city)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(sampleTile())
	zw.Close()
	if again, err := decodeTile(gz.Bytes()); err != nil || len(again) != 5 {
		t.Errorf("gzipped tile: %v", err)
	}
	for _, bad := range [][]byte{{0x1a, 0x05, 0x0a}, {0xff}, {0x1f, 0x8b, 0x00}, {0x1a, 0x02, 0x12, 0x09}} {
		if _, err := decodeTile(bad); err == nil {
			t.Errorf("%x should fail", bad)
		}
	}
	// Truncated geometry stops cleanly rather than reading past the end.
	parts, _ := geometry([]uint32{1 | 1<<3, 2}, mvtLine)
	if len(parts) != 0 {
		t.Errorf("short geometry = %v", parts)
	}
	if parts, _ := geometry([]uint32{9 << 3}, mvtLine); parts != nil {
		t.Error("unknown command")
	}
}

func TestAddTile(t *testing.T) {
	layers, _ := decodeTile(sampleTile())
	d := &Detail{Lines: map[string][][][2]float64{}}
	tile := Tile{1023, 600}
	d.addTile(tile, layers)
	d.Tiles = []Tile{tile}
	d.index()
	if len(d.Sea) != 2 || len(d.Islands) != 1 {
		t.Errorf("sea %d islands %d: the pond and the small lake are dropped", len(d.Sea), len(d.Islands))
	}
	if len(d.Lines["river"]) != 1 || len(d.Lines["motorway"]) != 1 || len(d.Lines["minor"]) != 0 {
		t.Errorf("lines = %v", d.Lines)
	}
	if len(d.Places) != 1 || d.Places[0].Name != "Tokyo" || d.Places[0].Rank != 5 {
		t.Errorf("places = %+v", d.Places)
	}
	// The ocean's edges along the tile's sides are seams, not coast; its east side is coast.
	coast := d.Lines["coast"]
	if len(coast) != 3 { // the ocean's east edge, the lake and its island
		t.Fatalf("coast = %d runs", len(coast))
	}
	b := tile.box()
	if !d.Covers(LatLon{(b[1] + b[3]) / 2, (b[0] + b[2]) / 2}) || d.Covers(LatLon{0, 0}) {
		t.Error("covers")
	}
	if got := d.Missing(b.Grow(0.5)); len(got) != 8 {
		t.Errorf("missing around one tile = %d", len(got))
	}
	d.trim()
	if len(d.Places) != 1 {
		t.Error("trim keeps what's inside")
	}
}

func TestDecodeValuesAndSkips(t *testing.T) {
	val := func(build func(*pw)) any {
		var w pw
		build(&w)
		var err error
		return decodeValue(w.b, &err)
	}
	if v := val(func(w *pw) { w.key(2, 5); w.b = binary.LittleEndian.AppendUint32(w.b, math.Float32bits(1.5)) }); v != 1.5 {
		t.Errorf("float = %v", v)
	}
	if v := val(func(w *pw) { w.key(4, 0); w.varint(uint64(7)) }); v != 7.0 {
		t.Errorf("int = %v", v)
	}
	if v := val(func(w *pw) { w.key(5, 0); w.varint(9) }); v != 9.0 {
		t.Errorf("uint = %v", v)
	}
	if v := val(func(w *pw) { w.key(6, 0); w.varint(uint64(zig(-3))) }); v != -3.0 {
		t.Errorf("sint = %v", v)
	}
	if v := val(func(w *pw) {
		w.key(9, 0)
		w.varint(1)
		w.key(10, 1)
		w.b = append(w.b, make([]byte, 8)...)
		w.key(11, 5)
		w.b = append(w.b, 0, 0, 0, 0)
	}); v != nil {
		t.Errorf("unknown fields are skipped: %v", v)
	}
	var err error
	decodeValue([]byte{0x15, 0x01}, &err)
	if err == nil {
		t.Error("a short float")
	}
	err = nil
	decodeFeature([]byte{0x0b}, &err) // wire type 3 isn't used by vector tiles
	if err == nil {
		t.Error("a bad wire type")
	}
	if l, _ := decodeTile(func() []byte { var w pw; w.key(1, 0); w.varint(5); return w.b }()); len(l) != 0 {
		t.Error("fields outside layers are skipped")
	}
}

// Tiles come from the network, so damaged ones must fail cleanly rather than crash.
func TestDamagedTilesDontPanic(t *testing.T) {
	tile := sampleTile()
	for n := range len(tile) {
		decodeTile(tile[:n])
	}
	for i := range len(tile) {
		for _, b := range []byte{0x00, 0xff, 0x80, 0x0a} {
			bad := bytes.Clone(tile)
			bad[i] = b
			decodeTile(bad)
		}
	}
}

func FuzzDecodeTile(f *testing.F) {
	f.Add(sampleTile())
	f.Fuzz(func(t *testing.T, data []byte) { decodeTile(data) })
}
