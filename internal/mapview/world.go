package mapview

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"sync"
)

//go:generate go run worldgen.go

// Natural Earth is public domain, so the world map ships inside the app and works offline anywhere.
//
//go:embed world.json.gz
var worldGz []byte

type Place struct {
	Name string  `json:"n"`
	Lat  float64 `json:"a"`
	Lon  float64 `json:"o"`
	Rank int     `json:"r"` // 0 is the biggest city; 10 the smallest town
}

type World struct {
	Land    [][][2]float64 `json:"land"`
	Borders [][][2]float64 `json:"borders"`
	Places  []Place        `json:"places"`

	landBox, borderBox []Box
	landLL, borderLL   [][]LatLon
}

var (
	worldOnce sync.Once
	world     *World
)

// LoadWorld decodes the built-in map the first time it's wanted; it takes a moment, so call it off the UI loop.
func LoadWorld() *World {
	worldOnce.Do(func() {
		zr, err := gzip.NewReader(bytes.NewReader(worldGz))
		if err != nil {
			panic(err)
		}
		var w World
		if err := json.NewDecoder(zr).Decode(&w); err != nil {
			panic(err)
		}
		for _, r := range w.Land {
			w.landBox, w.landLL = append(w.landBox, ringBox(r)), append(w.landLL, toLL(r))
		}
		for _, l := range w.Borders {
			w.borderBox, w.borderLL = append(w.borderBox, ringBox(l)), append(w.borderLL, toLL(l))
		}
		world = &w
	})
	return world
}

func ringBox(pts [][2]float64) Box {
	b := Box{180, 90, -180, -90}
	for _, p := range pts {
		b = Box{min(b[0], p[0]), min(b[1], p[1]), max(b[2], p[0]), max(b[3], p[1])}
	}
	return b
}
