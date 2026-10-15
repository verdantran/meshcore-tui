package mapview

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Street detail comes from OpenFreeMap's vector tiles (OpenMapTiles schema, OpenStreetMap data), fetched
// once for the mesh's area at one zoom. Their terms for offline use: at most 3,000 tiles or 50 MB a download,
// four requests at once, and a User-Agent naming the app.
const (
	DetailZoom   = 10
	DetailCredit = "OpenFreeMap © OpenMapTiles Data from OpenStreetMap"
	MaxTiles     = 400 // far more than any mesh needs, and well inside OpenFreeMap's limit
	tileExtent   = 4096.0
)

// Tile is an x, y at DetailZoom.
type Tile [2]int

// TilesFor lists the tiles covering a box, which may run past ±180.
func TilesFor(b Box) []Tile {
	n := 1 << DetailZoom
	tx := func(lon float64) int { return int(math.Floor((lon + 180) / 360 * float64(n))) }
	ty := func(lat float64) int {
		_, y := worldXY(LatLon{lat, 0}, DetailZoom)
		return int(math.Floor(y / 256))
	}
	var out []Tile
	for x := tx(b[0]); x <= tx(b[2]); x++ {
		for y := ty(b[3]); y <= ty(b[1]); y++ {
			out = append(out, Tile{((x % n) + n) % n, y})
		}
	}
	return out
}

func (t Tile) box() Box {
	nw := fromWorld(float64(t[0])*256, float64(t[1])*256, DetailZoom)
	se := fromWorld(float64(t[0]+1)*256, float64(t[1]+1)*256, DetailZoom)
	return Box{nw.Lon, se.Lat, se.Lon, nw.Lat}
}

// Detail is street detail for an area, kept as one file in the data directory.
type Detail struct {
	Tiles   []Tile                    `json:"tiles"`
	Sea     [][][2]float64            `json:"sea"`
	Islands [][][2]float64            `json:"islands"`
	Lines   map[string][][][2]float64 `json:"lines"` // coast, river, motorway, trunk, primary
	Places  []Place                   `json:"places"`

	have  map[Tile]bool
	boxes []Box
}

func (d *Detail) index() {
	d.have, d.boxes = map[Tile]bool{}, nil
	for _, t := range d.Tiles {
		d.have[t] = true
		d.boxes = append(d.boxes, t.box())
	}
}

// Covers reports whether street detail exists for a point.
func (d *Detail) Covers(p LatLon) bool {
	if d == nil {
		return false
	}
	for _, b := range d.boxes {
		if b.Contains(p) {
			return true
		}
	}
	return false
}

// Missing lists the tiles a box needs that the detail doesn't have yet.
func (d *Detail) Missing(b Box) []Tile {
	var out []Tile
	for _, t := range TilesFor(b) {
		if d == nil || !d.have[t] {
			out = append(out, t)
		}
	}
	return out
}

func LoadDetail(path string) (*Detail, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	var d Detail
	if err := json.NewDecoder(zr).Decode(&d); err != nil {
		return nil, err
	}
	d.index()
	return &d, nil
}

// Save writes the file atomically, so a crash never leaves a broken one.
func (d *Detail) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	err = json.NewEncoder(zw).Encode(d)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Fetcher downloads tiles. Tiles land in Dir as they arrive, so a download that stops part way resumes.
type Fetcher struct {
	Index  string // the TileJSON listing the tile URL
	Agent  string
	Dir    string
	Client *http.Client
}

const OpenFreeMap = "https://tiles.openfreemap.org/planet"

func (f Fetcher) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.Agent)
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenFreeMap answered %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// Fetch downloads tiles and adds what the map draws from them to d, which may be nil to start afresh.
// progress is called after each tile with how many are done. It returns the bytes downloaded.
func (f Fetcher) Fetch(ctx context.Context, d *Detail, tiles []Tile, progress func(done int)) (*Detail, int, error) {
	if len(tiles) > MaxTiles {
		return nil, 0, fmt.Errorf("that area needs %d tiles, more than a mesh should", len(tiles))
	}
	idx, err := f.get(ctx, f.Index)
	if err != nil {
		return nil, 0, err
	}
	var tj struct{ Tiles []string }
	if err := json.Unmarshal(idx, &tj); err != nil || len(tj.Tiles) == 0 {
		return nil, 0, errors.New("couldn't read OpenFreeMap's tile list")
	}
	out := &Detail{Lines: map[string][][][2]float64{}}
	if d != nil {
		*out = *d
		out.Lines = map[string][][][2]float64{}
		for k, v := range d.Lines {
			out.Lines[k] = append([][][2]float64(nil), v...)
		}
	}
	var (
		mu       sync.Mutex
		firstErr error
		total    int
		done     int
		wg       sync.WaitGroup
		sem      = make(chan struct{}, 4)
	)
	for _, t := range tiles {
		wg.Add(1)
		sem <- struct{}{}
		go func(t Tile) {
			defer wg.Done()
			defer func() { <-sem }()
			cache := filepath.Join(f.Dir, fmt.Sprintf("%d-%d-%d.pbf", DetailZoom, t[0], t[1]))
			data, err := os.ReadFile(cache)
			fresh := 0
			if err != nil && ctx.Err() == nil {
				url := strings.NewReplacer("{z}", fmt.Sprint(DetailZoom), "{x}", fmt.Sprint(t[0]), "{y}", fmt.Sprint(t[1])).Replace(tj.Tiles[0])
				if data, err = f.get(ctx, url); err == nil && f.Dir != "" {
					os.MkdirAll(f.Dir, 0o755)
					os.WriteFile(cache, data, 0o644)
				}
				fresh = len(data)
			}
			var layers []mvtLayer
			if err == nil {
				layers, err = decodeTile(data)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			total += fresh
			out.addTile(t, layers)
			out.Tiles = append(out.Tiles, t)
			done++
			if progress != nil {
				progress(done)
			}
		}(t)
	}
	wg.Wait()
	if firstErr != nil {
		if ctx.Err() != nil {
			firstErr = ctx.Err()
		}
		return nil, total, firstErr
	}
	out.index()
	out.trim()
	return out, total, nil
}

// ClearCache deletes downloaded tiles once they're in the detail file.
func (f Fetcher) ClearCache() error {
	if f.Dir == "" {
		return nil
	}
	return os.RemoveAll(f.Dir)
}

func (d *Detail) addTile(t Tile, layers []mvtLayer) {
	for _, l := range layers {
		ext := l.Extent
		ll := func(p tilePoint) [2]float64 {
			n := math.Exp2(DetailZoom)
			x, y := (float64(t[0])+p[0]/ext)/n, (float64(t[1])+p[1]/ext)/n
			lat := math.Atan(math.Sinh(math.Pi*(1-2*y))) * 180 / math.Pi
			return [2]float64{math.Round((x*360-180)*1e5) / 1e5, math.Round(lat*1e5) / 1e5}
		}
		lines := func(f mvtFeature) [][][2]float64 {
			var out [][][2]float64
			for _, part := range f.Parts {
				pts := make([][2]float64, len(part))
				for i, p := range part {
					pts[i] = ll(p)
				}
				out = append(out, pts)
			}
			return out
		}
		for _, f := range l.Features {
			class, _ := f.Props["class"].(string)
			switch l.Name {
			case "water":
				if f.Type == mvtPolygon && keepWater(class, f, ext) {
					d.addWater(f, ll, ext)
				}
			case "waterway":
				if f.Type == mvtLine && (class == "river" || class == "canal") {
					d.Lines["river"] = append(d.Lines["river"], lines(f)...)
				}
			case "transportation":
				if f.Type == mvtLine && (class == "motorway" || class == "trunk" || class == "primary") {
					d.Lines[class] = append(d.Lines[class], lines(f)...)
				}
			case "place":
				rank := map[string]int{"city": 5, "town": 8}[class]
				name, _ := f.Props["name:latin"].(string)
				if name == "" {
					name, _ = f.Props["name"].(string)
				}
				if f.Type == mvtPoint && rank > 0 && name != "" && len(f.Parts) > 0 && len(f.Parts[0]) > 0 {
					q := ll(f.Parts[0][0])
					d.Places = append(d.Places, Place{Name: name, Lat: q[1], Lon: q[0], Rank: rank})
				}
			}
		}
	}
}

// keepWater keeps the sea, rivers drawn as areas, and lakes big enough to see at this zoom (about 2 km).
func keepWater(class string, f mvtFeature, ext float64) bool {
	switch class {
	case "ocean", "river":
		return true
	case "lake":
		for _, r := range f.Parts {
			b := Box{1e9, 1e9, -1e9, -1e9}
			for _, p := range r {
				b = Box{min(b[0], p[0]), min(b[1], p[1]), max(b[2], p[0]), max(b[3], p[1])}
			}
			if b[2]-b[0] > ext/10 || b[3]-b[1] > ext/10 {
				return true
			}
		}
	}
	return false
}

// addWater keeps water areas and draws their edges as coastline, except where a tile cut them off.
func (d *Detail) addWater(f mvtFeature, ll func(tilePoint) [2]float64, ext float64) {
	edge := func(v float64) int {
		switch {
		case v <= 0:
			return -1
		case v >= ext:
			return 1
		}
		return 0
	}
	seam := func(a, b tilePoint) bool {
		return edge(a[0]) != 0 && edge(a[0]) == edge(b[0]) || edge(a[1]) != 0 && edge(a[1]) == edge(b[1])
	}
	// Edge points are pushed a little past the tile, so neighbouring tiles' water overlaps instead of
	// leaving a hairline of land along the seam.
	bleed := func(p tilePoint) tilePoint {
		for i := range p {
			switch edge(p[i]) {
			case -1:
				p[i] = min(p[i], -6)
			case 1:
				p[i] = max(p[i], ext+6)
			}
		}
		return p
	}
	for i, ring := range f.Parts {
		pts := make([][2]float64, len(ring))
		var run [][2]float64
		flush := func() {
			if len(run) > 1 {
				d.Lines["coast"] = append(d.Lines["coast"], run)
			}
			run = nil
		}
		for k, p := range ring {
			pts[k] = ll(bleed(p))
			prev := ring[(k+len(ring)-1)%len(ring)]
			if seam(prev, p) {
				flush()
				continue
			}
			if len(run) == 0 {
				run = append(run, ll(prev))
			}
			run = append(run, ll(p))
		}
		flush()
		if f.Outer[i] {
			d.Sea = append(d.Sea, pts)
		} else {
			d.Islands = append(d.Islands, pts)
		}
	}
}

// trim drops lines that never enter the tiles held, and towns repeated by neighbouring tiles.
func (d *Detail) trim() {
	for k, ls := range d.Lines {
		var keep [][][2]float64
		for _, l := range ls {
			for _, q := range l {
				if d.Covers(LatLon{q[1], q[0]}) {
					keep = append(keep, l)
					break
				}
			}
		}
		d.Lines[k] = keep
	}
	seen := map[string]bool{}
	var places []Place
	for _, p := range d.Places {
		if !seen[p.Name] && d.Covers(LatLon{p.Lat, p.Lon}) {
			seen[p.Name] = true
			places = append(places, p)
		}
	}
	d.Places = places
}
