package mapview

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"meshcore-tui/internal/motion"
	"meshcore-tui/internal/theme"
)

func TestNearAndBoxes(t *testing.T) {
	if near(-179, 179) != 181 || near(179, -179) != -181 || near(10, 0) != 10 {
		t.Error("near")
	}
	b := Box{179.5, -17, 180.5, -16}
	if !b.Contains(LatLon{-16.5, -179.8}) || b.Contains(LatLon{-16.5, 170}) {
		t.Error("a box across 180 holds points on both sides")
	}
	if !(Box{-180, -20, -179, -10}).Overlaps(b) || (Box{0, 0, 1, 1}).Overlaps(b) {
		t.Error("overlaps across 180")
	}
	if g := (Box{0, 0, 10, 10}).Grow(0.1); g != (Box{-1, -1, 11, 11}) {
		t.Errorf("grow = %v", g)
	}
	if d := KmBetween(LatLon{51.44, -2.6}, LatLon{51.45, -2.6}); math.Abs(d-1.112) > 0.01 {
		t.Errorf("km = %v", d)
	}
}

func TestViewProjectsAndMoves(t *testing.T) {
	v := View{Center: LatLon{51.44, -2.6}, Zoom: 10, W: 200, H: 100}
	if x, y := v.Project(v.Center); x != 100 || y != 50 {
		t.Errorf("centre at %v,%v", x, y)
	}
	p := v.At(150, 20)
	if x, y := v.Project(p); math.Abs(x-150) > 1e-6 || math.Abs(y-20) > 1e-6 {
		t.Error("At undoes Project")
	}
	if east := v.Pan(0.5, 0); east.Center.Lon <= v.Center.Lon || math.Abs(east.Center.Lat-v.Center.Lat) > 1e-9 {
		t.Error("pan east")
	}
	if v.Zoomed(10).Zoom != MaxZoom || v.Zoomed(-20).Zoom != MinZoom {
		t.Error("zoom is clamped")
	}
	b := v.Bounds()
	if !b.Contains(v.Center) || b[0] >= b[2] || b[1] >= b[3] {
		t.Errorf("bounds = %v", b)
	}
	mid := Between(View{Center: LatLon{0, 179}, Zoom: 4}, View{Center: LatLon{10, -179}, Zoom: 6}, 0.5)
	if math.Abs(math.Abs(mid.Center.Lon)-180) > 1e-9 || mid.Center.Lat != 5 || mid.Zoom != 5 {
		t.Errorf("between takes the short way across 180: %+v", mid.Center)
	}
	// A mesh across 180° fits as one group, not the whole world.
	f := View{W: 200, H: 100}.Fit([]LatLon{{-16.6, 179.8}, {-16.9, -179.9}}, 0.1)
	if f.Zoom < 8 || math.Abs(math.Abs(f.Center.Lon)-179.95) > 0.01 {
		t.Errorf("fit across 180 = %+v", f)
	}
	if (View{W: 10, H: 10}).Fit(nil, 0).Zoom != 0 {
		t.Error("nothing to fit")
	}
}

func TestMeshArea(t *testing.T) {
	pts := []LatLon{{51.44, -2.6}, {51.40, -2.64}, {51.48, -2.56}, {51.42, -2.41}, {0, 0}, {57.4, 1.4}}
	keep, box, ok := MeshArea(pts)
	if !ok || len(keep) != 4 {
		t.Fatalf("kept %d of %v", len(keep), pts)
	}
	if !box.Contains(LatLon{51.44, -2.6}) || box.Contains(LatLon{57.4, 1.4}) {
		t.Errorf("box = %v", box)
	}
	if _, _, ok := MeshArea([]LatLon{{0, 0}}); ok {
		t.Error("only unplaced nodes")
	}
}

func TestTilesFor(t *testing.T) {
	tiles := TilesFor(Box{179.5, -17, 180.3, -16.5})
	wraps := false
	for _, tl := range tiles {
		wraps = wraps || tl[0] == 0
		if tl[0] < 0 || tl[0] >= 1<<DetailZoom {
			t.Fatalf("tile out of range: %v", tl)
		}
	}
	if !wraps || len(tiles) < 4 {
		t.Errorf("tiles across 180 = %v", tiles)
	}
}

func tileServer(t *testing.T, fail func(path string) bool) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "test-agent" {
			t.Errorf("user agent %q", r.Header.Get("User-Agent"))
		}
		if fail != nil && fail(r.URL.Path) {
			http.Error(w, "nope", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/planet" {
			w.Write([]byte(`{"tiles":["` + srv.URL + `/t/{z}/{x}/{y}.pbf"]}`))
			return
		}
		hits.Add(1)
		w.Write(sampleTile())
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestFetchResumeAndSave(t *testing.T) {
	stop := atomic.Bool{}
	srv, hits := tileServer(t, func(p string) bool { return stop.Load() && strings.HasPrefix(p, "/t/") })
	dir := t.TempDir()
	f := Fetcher{Index: srv.URL + "/planet", Agent: "test-agent", Dir: filepath.Join(dir, "tiles")}
	box := Box{-2.9, 51.3, -2.3, 51.6}
	tiles := TilesFor(box)
	var progress atomic.Int32
	d, n, err := f.Fetch(context.Background(), nil, tiles, func(k int) { progress.Store(int32(k)) })
	if err != nil {
		t.Fatal(err)
	}
	if int(progress.Load()) != len(tiles) || n != len(tiles)*len(sampleTile()) || len(d.Tiles) != len(tiles) {
		t.Errorf("progress %d, %d bytes, %d tiles", progress.Load(), n, len(d.Tiles))
	}
	if len(d.Missing(box)) != 0 || !d.Covers(LatLon{51.45, -2.6}) {
		t.Error("the area is covered")
	}

	// Tiles already downloaded are read from the cache, so a stopped download carries on.
	stop.Store(true)
	before := hits.Load()
	again, n, err := f.Fetch(context.Background(), nil, tiles, nil)
	if err != nil || n != 0 || hits.Load() != before || len(again.Tiles) != len(tiles) {
		t.Errorf("from the cache: %v, %d bytes, %d hits", err, n, hits.Load()-before)
	}
	more := TilesFor(Box{-2.3, 51.3, -2.0, 51.6})
	if _, _, err := f.Fetch(context.Background(), d, d.Missing(Box{-2.9, 51.3, -2.0, 51.6}), nil); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("server error = %v (%d more tiles)", err, len(more))
	}

	path := filepath.Join(dir, "detail.json.gz")
	if err := d.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := LoadDetail(path)
	if err != nil || len(back.Tiles) != len(d.Tiles) || len(back.Lines["motorway"]) != len(d.Lines["motorway"]) || !back.Covers(LatLon{51.45, -2.6}) {
		t.Fatalf("round trip: %v", err)
	}
	if err := f.ClearCache(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.Dir); !os.IsNotExist(err) {
		t.Error("cache cleared")
	}
	os.WriteFile(path, []byte("not gzip"), 0o644)
	if _, err := LoadDetail(path); err == nil {
		t.Error("a broken file")
	}
	if _, err := LoadDetail(filepath.Join(dir, "none")); !os.IsNotExist(err) {
		t.Error("no file")
	}
	if err := d.Save(filepath.Join(path, "under-a-file")); err == nil {
		t.Error("can't save under a file")
	}
}

func TestFetchFailures(t *testing.T) {
	ctx := context.Background()
	if _, _, err := (Fetcher{}).Fetch(ctx, nil, make([]Tile, MaxTiles+1), nil); err == nil || !strings.Contains(err.Error(), "more than a mesh") {
		t.Errorf("too many tiles: %v", err)
	}
	srv, _ := tileServer(t, func(p string) bool { return p == "/planet" })
	if _, _, err := (Fetcher{Index: srv.URL + "/planet", Agent: "test-agent"}).Fetch(ctx, nil, []Tile{{1, 1}}, nil); err == nil {
		t.Error("no tile list")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[]")) }))
	defer bad.Close()
	if _, _, err := (Fetcher{Index: bad.URL}).Fetch(ctx, nil, []Tile{{1, 1}}, nil); err == nil || !strings.Contains(err.Error(), "tile list") {
		t.Errorf("unreadable list: %v", err)
	}
	if _, _, err := (Fetcher{Index: "http://127.0.0.1:1/none"}).Fetch(ctx, nil, []Tile{{1, 1}}, nil); err == nil {
		t.Error("nothing listening")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	srv2, _ := tileServer(t, nil)
	if _, _, err := (Fetcher{Index: srv2.URL + "/planet", Agent: "test-agent"}).Fetch(cancelled, nil, []Tile{{1, 1}}, nil); err == nil {
		t.Error("cancelled")
	}
	if (Fetcher{}).ClearCache() != nil {
		t.Error("no cache to clear")
	}
	if (*Detail)(nil).Covers(LatLon{}) || len((*Detail)(nil).Missing(Box{0, 0, 0.1, 0.1})) == 0 {
		t.Error("no detail covers nothing")
	}
}

func bristol() []Node {
	return []Node{
		{Name: "me", Kind: KindSelf, Pos: LatLon{51.44, -2.598}, Key: [32]byte{0xC0}},
		{Name: "Dundry", Kind: KindRepeater, Pos: LatLon{51.399, -2.639}, Key: [32]byte{0xA3}},
		{Name: "Purdown", Kind: KindRepeater, Pos: LatLon{51.484, -2.564}, Key: [32]byte{0x77}},
		{Name: "Kelston", Kind: KindRepeater, Pos: LatLon{51.399, -2.431}, Key: [32]byte{0x5F, 1}},
		{Name: "Lansdown", Kind: KindRepeater, Pos: LatLon{51.42, -2.41}, Key: [32]byte{0x5F, 2}},
		{Name: "Bob", Kind: KindChat, Pos: LatLon{51.381, -2.36}, Key: [32]byte{0xB0}},
		{Name: "Clifton Room", Kind: KindRoom, Pos: LatLon{51.456, -2.62}, Key: [32]byte{0x6C}},
		{Name: "Hidden 🛰", Kind: KindRepeater, Key: [32]byte{0x99}},
		{Name: "Sensor", Kind: KindOther, Pos: LatLon{51.43, -2.7}},
	}
}

func TestStops(t *testing.T) {
	nodes := bristol()
	stops := Stops(5, [][]byte{{0x5F}, {0x99}, {0x77}}, nodes, 0)
	if len(stops) != 5 || stops[0].Nodes[0] != 5 || stops[4].Nodes[0] != 0 {
		t.Fatalf("stops = %+v", stops)
	}
	if amb := stops[1]; amb.Hash != "5F" || len(amb.Nodes) != 2 || amb.Weights[0] < amb.Weights[1] || math.Abs(amb.Weights[0]+amb.Weights[1]-1) > 1e-9 {
		t.Errorf("5F is Kelston or Lansdown, likeliest first: %+v", amb)
	}
	// A candidate well off the line between its neighbours is ranked last, and unlikely.
	far := append(bristol(), Node{Name: "Far 5F", Kind: KindRepeater, Pos: LatLon{51.9, -3.5}, Key: [32]byte{0x5F, 3}})
	if amb := Stops(5, [][]byte{{0x5F}, {0x77}}, far, 0)[1]; far[amb.Nodes[2]].Name != "Far 5F" || amb.Weights[2] > 0.01 {
		t.Errorf("far candidate: %+v", amb)
	}
	if len(stops[2].Nodes) != 0 {
		t.Error("a repeater without a location isn't placed")
	}
	r := Route{Stops: stops, Links: []Link{{}, {}, {}, {SNR: 6, Known: true}}}
	pts, links, uncertain := r.Placed(nodes)
	if len(pts) != 4 || len(links) != 3 || !uncertain[0] || !uncertain[1] || uncertain[2] || !links[2].Known {
		t.Errorf("placed %d, links %+v, uncertain %v", len(pts), links, uncertain)
	}
	if km := r.Km(nodes); km < 10 || km > 40 {
		t.Errorf("km = %v", km)
	}
	if got := Stops(-1, nil, nodes, -1); len(got) != 2 || len(got[0].Nodes) != 0 {
		t.Errorf("unplaced ends = %+v", got)
	}
}

func TestDrawAndAnimate(t *testing.T) {
	nodes := bristol()
	var pts []LatLon
	for _, n := range nodes {
		if !n.Pos.Unplaced() {
			pts = append(pts, n.Pos)
		}
	}
	routes := []Route{
		{What: "ADVERT", Stops: Stops(5, [][]byte{{0x5F}, {0x77}}, nodes, 0), Links: []Link{{}, {SNR: -2, Known: true}, {SNR: 6, Known: true}}},
		{What: "GRP_TXT", Stops: Stops(6, [][]byte{{0xA3}}, nodes, 0), Links: []Link{{}, {SNR: 9, Known: true}}},
	}
	w, h := 100, 30
	v := View{W: w * 2, H: h * 4}.Fit(pts, 0.12)
	pal := PaletteFor(theme.Neon)
	layers := Layers{Labels: true, Roads: true, Selected: -1}
	world := LoadWorld()
	plain := func(c *Canvas) string { return ansi.Strip(strings.Join(c.Render(true), "\n")) }
	c := Draw(Input{World: world, Nodes: nodes, Routes: routes, Layers: layers}, v, w, h, pal)
	out := plain(c)
	for _, want := range []string{"◉", "▲", "●", "■", "Dundry", "Purdown", "Bob"} {
		if !strings.Contains(out, want) {
			t.Errorf("map lacks %q:\n%s", want, out)
		}
	}
	for _, row := range c.Render(true) {
		if ansi.StringWidth(row) != w {
			t.Fatalf("row width %d", ansi.StringWidth(row))
		}
	}

	// Picking a route names the likelihood of each candidate for its ambiguous hop and fades the rest.
	layers.Selected = 0
	picked := plain(Draw(Input{World: world, Nodes: nodes, Routes: routes, Layers: layers}, v, w, h, pal))
	if !strings.Contains(picked, "Kelston? ") || !strings.Contains(picked, "%") {
		t.Errorf("picked route:\n%s", picked)
	}

	// Zoomed out, nearby nodes merge into one marker.
	far := v
	far.Zoom = 5
	out = plain(Draw(Input{World: world, Nodes: nodes, Routes: routes, Layers: Layers{Labels: true, Selected: -1}}, far, w, h, pal))
	if !strings.Contains(out, "me + ") {
		t.Errorf("clustered:\n%s", out)
	}

	// Animations, at each motion level.
	now := time.Now()
	anims := []Anim{
		{Kind: Comet, Start: now.Add(-200 * time.Millisecond), Path: []LatLon{nodes[5].Pos, nodes[2].Pos, nodes[0].Pos}, Links: []Link{{}, {SNR: 5, Known: true}}},
		{Kind: Comet, Start: now.Add(-100 * time.Millisecond), Path: []LatLon{nodes[0].Pos, nodes[1].Pos}, Outward: true},
		{Kind: Comet, Start: now, Path: []LatLon{nodes[0].Pos}},
		{Kind: Pulse, Start: now.Add(-100 * time.Millisecond), At: nodes[1].Pos},
		{Kind: Ripple, Start: now.Add(-100 * time.Millisecond), At: nodes[5].Pos},
		{Kind: Ripple, Start: now.Add(-100 * time.Millisecond), At: nodes[0].Pos, Outward: true},
		{Kind: Pulse, Start: now.Add(-time.Hour), At: nodes[1].Pos},
	}
	if !anims[0].Running(now) || anims[6].Running(now) || anims[4].Allowed(motion.Reduced) || !anims[3].Allowed(motion.Reduced) || anims[3].Allowed(motion.Off) {
		t.Error("running and allowed")
	}
	if anims[0].Dur() != 2*HopDur || anims[3].Dur() != pulseDur || anims[4].Dur() != rippleDur {
		t.Error("durations")
	}
	for _, level := range []motion.Level{motion.Full, motion.Reduced, motion.Off} {
		a := c.Clone()
		DrawAnims(a, v, anims, nodes[0].Pos, level, now, pal)
		changed := plain(a) != plain(c)
		if changed == (level == motion.Off) {
			t.Errorf("level %v changed the map: %v", level, changed)
		}
	}

	// Street detail replaces the base map where it's held, and its towns are used.
	layersData, _ := decodeTile(sampleTile())
	d := &Detail{Lines: map[string][][][2]float64{}}
	tl := TilesFor(Box{-2.6, 51.44, -2.6, 51.44})[0]
	d.addTile(tl, layersData)
	d.Tiles = []Tile{tl}
	d.index()
	tb := tl.box()
	zv := View{Center: LatLon{(tb[1] + tb[3]) / 2, (tb[0] + tb[2]) / 2}, Zoom: 10, W: w * 2, H: h * 4}
	withDetail := Draw(Input{World: world, Detail: d, Nodes: nodes, Layers: Layers{Labels: true, Roads: true, Selected: -1}}, zv, w, h, pal)
	if !strings.Contains(plain(withDetail), "TOKYO") {
		t.Errorf("detail towns:\n%s", plain(withDetail))
	}
	Credit(withDetail, DetailCredit, pal)
	last := strings.Split(plain(withDetail), "\n")[h-1]
	if !strings.HasSuffix(last, DetailCredit+" ") {
		t.Errorf("credit bottom right: %q", last)
	}
	mono := withDetail.Render(false)
	if strings.Contains(strings.Join(mono, ""), "\x1b[") {
		t.Error("mono has no colour")
	}
}

func TestCanvasEdges(t *testing.T) {
	c := NewCanvas(4, 2, theme.RGB{})
	c.Dot(-1, 0, theme.RGB{}, 1)
	c.Dot(100, 100, theme.RGB{}, 1)
	c.Line(-500, -500, -400, -400, theme.RGB{}, 1, 0)
	c.Line(0, 0, 9000, 0, theme.RGB{}, 1, 0)
	c.Text(3, 0, "東", theme.RGB{R: 1}, false) // a wide character with no room is dropped
	c.Text(0, 1, "a東b", theme.RGB{R: 1}, false)
	rows := c.Render(false)
	if rows[0] != "    " || rows[1] != "a東b" {
		t.Errorf("rows = %q", rows)
	}
	if c.Free(0, 1, 2, 99) || !c.Free(0, 0, 3, 99) {
		t.Error("free")
	}
	c.Dot(3, 4, theme.RGB{}, 1) // under the wide character: ignored
	if c.Render(false)[1] != "a東b" {
		t.Error("dots don't overwrite text")
	}
	if luminance(theme.Paper.Bg) < 0.5 || PaletteFor(theme.Paper).Sea == PaletteFor(theme.Neon).Sea {
		t.Error("light themes get their own sea")
	}
}

func TestRoutesFadeAndTraffic(t *testing.T) {
	nodes := bristol()
	now := time.Now()
	if RouteFade(now.Add(-time.Minute), now) != 0 || RouteFade(now.Add(-6*time.Minute), now) != 0.5 || RouteFade(now.Add(-time.Hour), now) != 1 {
		t.Error("fade")
	}
	var routes []Route
	for i := range RoutesShown + 2 {
		routes = append(routes, Route{At: now.Add(-time.Duration(i) * time.Second), Stops: Stops(5, [][]byte{{0x77}}, nodes, 0), Links: []Link{{}, {Known: true}}})
	}
	routes = append(routes, Route{At: now.Add(-time.Hour), Stops: Stops(6, nil, nodes, 0), Links: []Link{{}}})
	in := Input{World: LoadWorld(), Nodes: nodes, Routes: routes, Now: now, Layers: Layers{Selected: -1}}
	v := View{W: 200, H: 120, Center: LatLon{51.44, -2.6}, Zoom: 10}
	count := func(in Input) (n int) {
		for _, l := range buildScene(in, v, PaletteFor(theme.Neon)).lines {
			if l.prio == prioRoute || l.prio == prioRouteSel {
				n++
			}
		}
		return n
	}
	if got := count(in); got != RoutesShown*2 {
		t.Errorf("only the newest routes are drawn: %d segments", got)
	}
	in.Layers.Selected = len(routes) - 1
	if count(in) != 1 {
		t.Error("a picked route is drawn however old")
	}
	in.Layers = Layers{Selected: -1, Traffic: true}
	in.Routes = nil
	in.Flows = []Flow{{A: nodes[0].Pos, B: nodes[1].Pos, Level: 1}, {A: nodes[1].Pos, B: nodes[2].Pos, Level: 0.2}}
	var traffic []sLine
	for _, l := range buildScene(in, v, PaletteFor(theme.Neon)).lines {
		if l.prio == prioTraffic {
			traffic = append(traffic, l)
		}
	}
	if len(traffic) != 2 || !traffic[0].bold || traffic[1].bold || traffic[0].col == traffic[1].col {
		t.Errorf("busier links are bolder and more saturated: %+v", traffic)
	}
	in.Layers.Traffic = false
	for _, l := range buildScene(in, v, PaletteFor(theme.Neon)).lines {
		if l.prio == prioTraffic {
			t.Fatal("hidden")
		}
	}
}

// referenceMask is the plain way to fill: every edge checked on every row. The edge table must agree with it.
func referenceMask(c *Canvas, rings [][][2]float64, inside func(int) bool, signs []int) []bool {
	w := c.W * 2
	mask := make([]bool, w*c.H*4)
	for y := 0; y < c.H*4; y++ {
		fy := float64(y) + 0.5
		var xs []crossing
		for r, ring := range rings {
			for i := range ring {
				a, b := ring[i], ring[(i+1)%len(ring)]
				if (a[1] <= fy) != (b[1] <= fy) {
					d := signs[r]
					if a[1] > b[1] {
						d = -d
					}
					xs = append(xs, crossing{a[0] + (fy-a[1])/(b[1]-a[1])*(b[0]-a[0]), d})
				}
			}
		}
		sort.Slice(xs, func(i, j int) bool { return xs[i].x < xs[j].x })
		wind := 0
		for i := 0; i+1 < len(xs); i++ {
			if wind += xs[i].d; inside(wind) {
				fill(mask[y*w:(y+1)*w], xs[i].x, xs[i+1].x)
			}
		}
	}
	return mask
}

func TestFillMatchesReference(t *testing.T) {
	world := LoadWorld()
	for _, v := range []View{
		{Center: LatLon{51.44, -2.6}, Zoom: 9, W: 240, H: 160},
		{Center: LatLon{54, -3}, Zoom: 5, W: 240, H: 160},
		{Center: LatLon{-16.7, 179.9}, Zoom: 8, W: 240, H: 160},
		{Center: LatLon{60.39, 5.32}, Zoom: 8, W: 240, H: 160},
	} {
		c := NewCanvas(v.W/2, v.H/4, theme.RGB{})
		var rings [][][2]float64
		view := v.Bounds().Grow(0.1)
		for i, r := range world.Land {
			if world.landBox[i].Overlaps(view) {
				ring := make([][2]float64, len(r))
				for j, p := range r {
					ring[j][0], ring[j][1] = v.Project(LatLon{p[1], p[0]})
				}
				rings = append(rings, ring)
			}
		}
		ones := make([]int, len(rings))
		for i := range ones {
			ones[i] = 1
		}
		got, want := c.evenOddMask(rings), referenceMask(c, rings, func(w int) bool { return w%2 != 0 }, ones)
		diff := 0
		for i := range got {
			if got[i] != want[i] {
				diff++
			}
		}
		if diff > 0 {
			t.Errorf("%+v: %d dots differ", v.Center, diff)
		}
	}
}

func TestProjectorMatchesProject(t *testing.T) {
	v := View{Center: LatLon{-16.7, 179.9}, Zoom: 8.5, W: 300, H: 200}
	p := v.projector()
	for _, q := range []LatLon{{-16.6, -179.8}, {51, -2}, {89, 10}, {-16.7, 179.9}} {
		x0, y0 := v.Project(q)
		x1, y1 := p(q)
		if math.Abs(x0-x1) > 1e-6 || math.Abs(y0-y1) > 1e-6 {
			t.Errorf("%v: %v,%v vs %v,%v", q, x0, y0, x1, y1)
		}
	}
}

// BenchmarkDraw is a full redraw, as during a pan or zoom: run with go test -bench Draw ./internal/mapview.
func BenchmarkDraw(b *testing.B) {
	nodes := bristol()
	in := Input{World: LoadWorld(), Nodes: nodes, Layers: Layers{Labels: true, Roads: true, Traffic: true, Selected: -1}, Now: time.Now()}
	v := View{Center: LatLon{51.44, -2.6}, Zoom: 9, W: 392, H: 224}
	pal := PaletteFor(theme.Neon)
	for b.Loop() {
		Draw(in, v, 196, 56, pal).Render(true)
	}
}
