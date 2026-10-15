package mapview

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	"math"
)

// A small reader for Mapbox Vector Tiles (protobuf), the format OpenFreeMap serves. It reads only what the map
// uses: layer names, feature properties, and geometry in tile coordinates.

type featureType int

const (
	mvtPoint featureType = 1 + iota
	mvtLine
	mvtPolygon
)

type tilePoint [2]float64

type mvtFeature struct {
	Type  featureType
	Props map[string]any
	// Parts are points, lines, or polygon rings; for polygons, Outer marks which rings start a new polygon.
	Parts [][]tilePoint
	Outer []bool
}

type mvtLayer struct {
	Name     string
	Extent   float64
	Features []mvtFeature
}

var errTile = errors.New("not a vector tile")

type pbuf struct {
	b   []byte
	err error
}

func (p *pbuf) varint() uint64 {
	v, n := binary.Uvarint(p.b)
	if n <= 0 {
		p.err, p.b = errTile, nil
		return 0
	}
	p.b = p.b[n:]
	return v
}

func (p *pbuf) bytes() []byte {
	n := p.varint()
	if p.err != nil || n > uint64(len(p.b)) {
		p.err, p.b = errTile, nil
		return nil
	}
	out := p.b[:n]
	p.b = p.b[n:]
	return out
}

func (p *pbuf) skip(wire uint64) {
	switch wire {
	case 0:
		p.varint()
	case 1:
		p.fixed(8)
	case 2:
		p.bytes()
	case 5:
		p.fixed(4)
	default:
		p.err, p.b = errTile, nil
	}
}

func (p *pbuf) fixed(n int) []byte {
	if len(p.b) < n {
		p.err, p.b = errTile, nil
		return nil
	}
	out := p.b[:n]
	p.b = p.b[n:]
	return out
}

func (p *pbuf) packed() []uint32 {
	inner := pbuf{b: p.bytes()}
	var out []uint32
	for len(inner.b) > 0 && inner.err == nil {
		out = append(out, uint32(inner.varint()))
	}
	if inner.err != nil {
		p.err = inner.err
	}
	return out
}

// decodeTile reads a tile, gunzipping it first if the server sent it compressed.
func decodeTile(data []byte) ([]mvtLayer, error) {
	if len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if data, err = io.ReadAll(zr); err != nil {
			return nil, err
		}
	}
	p := pbuf{b: data}
	var layers []mvtLayer
	for len(p.b) > 0 && p.err == nil {
		key := p.varint()
		if key>>3 == 3 && key&7 == 2 {
			l, err := decodeLayer(p.bytes())
			if err != nil {
				return nil, err
			}
			layers = append(layers, l)
			continue
		}
		p.skip(key & 7)
	}
	return layers, p.err
}

type rawFeature struct {
	typ  featureType
	tags []uint32
	geom []uint32
}

func decodeLayer(b []byte) (mvtLayer, error) {
	p := pbuf{b: b}
	l := mvtLayer{Extent: 4096}
	var keys []string
	var values []any
	var raws []rawFeature
	for len(p.b) > 0 && p.err == nil {
		key := p.varint()
		switch {
		case key == 1<<3|2:
			l.Name = string(p.bytes())
		case key == 2<<3|2:
			raws = append(raws, decodeFeature(p.bytes(), &p.err))
		case key == 3<<3|2:
			keys = append(keys, string(p.bytes()))
		case key == 4<<3|2:
			values = append(values, decodeValue(p.bytes(), &p.err))
		case key == 5<<3:
			l.Extent = float64(p.varint())
		default:
			p.skip(key & 7)
		}
	}
	if p.err != nil {
		return l, p.err
	}
	for _, r := range raws {
		f := mvtFeature{Type: r.typ, Props: map[string]any{}}
		for i := 0; i+1 < len(r.tags); i += 2 {
			if int(r.tags[i]) < len(keys) && int(r.tags[i+1]) < len(values) {
				f.Props[keys[r.tags[i]]] = values[r.tags[i+1]]
			}
		}
		f.Parts, f.Outer = geometry(r.geom, r.typ)
		l.Features = append(l.Features, f)
	}
	return l, nil
}

func decodeFeature(b []byte, err *error) rawFeature {
	p := pbuf{b: b}
	var f rawFeature
	for len(p.b) > 0 && p.err == nil {
		key := p.varint()
		switch key {
		case 2<<3 | 2:
			f.tags = p.packed()
		case 3 << 3:
			f.typ = featureType(p.varint())
		case 4<<3 | 2:
			f.geom = p.packed()
		default:
			p.skip(key & 7)
		}
	}
	if p.err != nil {
		*err = p.err
	}
	return f
}

func decodeValue(b []byte, err *error) any {
	p := pbuf{b: b}
	var v any
	for len(p.b) > 0 && p.err == nil {
		key := p.varint()
		switch key {
		case 1<<3 | 2:
			v = string(p.bytes())
		case 2<<3 | 5:
			if b := p.fixed(4); b != nil {
				v = float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
			}
		case 3<<3 | 1:
			if b := p.fixed(8); b != nil {
				v = math.Float64frombits(binary.LittleEndian.Uint64(b))
			}
		case 4 << 3:
			v = float64(int64(p.varint()))
		case 5 << 3:
			v = float64(p.varint())
		case 6 << 3:
			u := p.varint()
			v = float64(int64(u>>1) ^ -int64(u&1))
		case 7 << 3:
			v = p.varint() != 0
		default:
			p.skip(key & 7)
		}
	}
	if p.err != nil {
		*err = p.err
	}
	return v
}

// geometry runs the tile's drawing commands: MoveTo, LineTo and ClosePath with zigzag-encoded deltas.
func geometry(cmds []uint32, typ featureType) ([][]tilePoint, []bool) {
	var parts [][]tilePoint
	var cur []tilePoint
	var x, y int32
	zz := func(v uint32) int32 { return int32(v>>1) ^ -int32(v&1) }
	flush := func() {
		if len(cur) > 0 {
			parts = append(parts, cur)
			cur = nil
		}
	}
	for i := 0; i < len(cmds); {
		id, count := cmds[i]&7, int(cmds[i]>>3)
		i++
		switch id {
		case 1, 2:
			for range count {
				if i+1 >= len(cmds) {
					flush()
					return parts, outers(parts, typ)
				}
				x += zz(cmds[i])
				y += zz(cmds[i+1])
				i += 2
				if id == 1 && typ != mvtPoint {
					flush()
				}
				cur = append(cur, tilePoint{float64(x), float64(y)})
			}
		case 7:
			flush()
		default:
			return parts, outers(parts, typ)
		}
	}
	flush()
	return parts, outers(parts, typ)
}

// outers marks polygon rings that start a polygon: exterior rings have positive area in tile coordinates.
func outers(parts [][]tilePoint, typ featureType) []bool {
	if typ != mvtPolygon {
		return nil
	}
	out := make([]bool, len(parts))
	for i, r := range parts {
		a := 0.0
		for j := range r {
			p, q := r[j], r[(j+1)%len(r)]
			a += p[0]*q[1] - q[0]*p[1]
		}
		out[i] = a > 0
	}
	return out
}
