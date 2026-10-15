//go:build ignore

// worldgen builds world.json.gz, the base map built into the app, from Natural Earth (public domain):
// land simplified to about 500 m, country borders, and ranked towns.
//
//	go generate ./internal/mapview            # downloads the pinned release
//	go run worldgen.go -src DIR               # or uses .geojson files already in DIR
package main

import (
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
)

const (
	release   = "v5.1.2"
	tolerance = 0.005 // degrees, about 500 m
)

var layers = map[string]string{
	"land":    "ne_10m_land",
	"places":  "ne_10m_populated_places_simple",
	"borders": "ne_10m_admin_0_boundary_lines_land",
}

type feature struct {
	Geometry struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
	Properties map[string]any `json:"properties"`
}

func read(src, name string) []feature {
	var data []byte
	path := filepath.Join(src, name+".geojson")
	if b, err := os.ReadFile(path); err == nil && src != "" {
		data = b
	} else {
		url := "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/" + release + "/geojson/" + name + ".geojson"
		resp, err := http.Get(url)
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			panic(url + ": " + resp.Status)
		}
		data, _ = io.ReadAll(resp.Body)
	}
	var fc struct{ Features []feature }
	if err := json.Unmarshal(data, &fc); err != nil {
		panic(err)
	}
	return fc.Features
}

type pt = [2]float64

// simplify is Douglas–Peucker in degrees.
func simplify(pts []pt, tol float64) []pt {
	if len(pts) < 3 {
		return pts
	}
	keep := make([]bool, len(pts))
	keep[0], keep[len(pts)-1] = true, true
	stack := [][2]int{{0, len(pts) - 1}}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		a, b := pts[s[0]], pts[s[1]]
		dx, dy := b[0]-a[0], b[1]-a[1]
		n2 := dx*dx + dy*dy
		best, idx := 0.0, -1
		for i := s[0] + 1; i < s[1]; i++ {
			p := pts[i]
			t := 0.0
			if n2 > 0 {
				t = min(max(((p[0]-a[0])*dx+(p[1]-a[1])*dy)/n2, 0), 1)
			}
			d := math.Pow(p[0]-a[0]-t*dx, 2) + math.Pow(p[1]-a[1]-t*dy, 2)
			if d > best {
				best, idx = d, i
			}
		}
		if best > tol*tol {
			keep[idx] = true
			stack = append(stack, [2]int{s[0], idx}, [2]int{idx, s[1]})
		}
	}
	var out []pt
	for i, k := range keep {
		if k {
			out = append(out, round(pts[i]))
		}
	}
	return out
}

func round(p pt) pt { return pt{math.Round(p[0]*1e3) / 1e3, math.Round(p[1]*1e3) / 1e3} }

func rings(f feature) [][]pt {
	var out [][]pt
	switch f.Geometry.Type {
	case "Polygon":
		var p [][]pt
		json.Unmarshal(f.Geometry.Coordinates, &p)
		out = p
	case "MultiPolygon":
		var mp [][][]pt
		json.Unmarshal(f.Geometry.Coordinates, &mp)
		for _, p := range mp {
			out = append(out, p...)
		}
	case "LineString":
		var l []pt
		json.Unmarshal(f.Geometry.Coordinates, &l)
		out = [][]pt{l}
	case "MultiLineString":
		json.Unmarshal(f.Geometry.Coordinates, &out)
	}
	return out
}

func main() {
	src := flag.String("src", "", "directory holding the Natural Earth .geojson files; empty downloads them")
	out := flag.String("out", "world.json.gz", "file to write")
	flag.Parse()

	var w struct {
		Land    [][]pt  `json:"land"`
		Borders [][]pt  `json:"borders"`
		Places  []place `json:"places"`
	}
	for _, f := range read(*src, layers["land"]) {
		for _, r := range rings(f) {
			if s := simplify(r, tolerance); len(s) >= 4 {
				w.Land = append(w.Land, s)
			}
		}
	}
	for _, f := range read(*src, layers["borders"]) {
		for _, l := range rings(f) {
			if s := simplify(l, tolerance); len(s) >= 2 {
				w.Borders = append(w.Borders, s)
			}
		}
	}
	for _, f := range read(*src, layers["places"]) {
		p := f.Properties
		name, _ := p["nameascii"].(string)
		if name == "" {
			name, _ = p["name"].(string)
		}
		lat, _ := p["latitude"].(float64)
		lon, _ := p["longitude"].(float64)
		rank, _ := p["scalerank"].(float64)
		w.Places = append(w.Places, place{name, math.Round(lat*1e4) / 1e4, math.Round(lon*1e4) / 1e4, int(rank)})
	}
	file, err := os.Create(*out)
	if err != nil {
		panic(err)
	}
	defer file.Close()
	zw, _ := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err := json.NewEncoder(zw).Encode(w); err != nil {
		panic(err)
	}
	zw.Close()
	fmt.Printf("Natural Earth %s: %d land rings, %d borders, %d places\n", release, len(w.Land), len(w.Borders), len(w.Places))
}

type place struct {
	Name string  `json:"n"`
	Lat  float64 `json:"a"`
	Lon  float64 `json:"o"`
	Rank int     `json:"r"`
}
