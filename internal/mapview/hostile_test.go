package mapview

import (
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/verdantran/meshcore-tui/internal/theme"
)

func TestSceneLeavesOutNodesOffScreen(t *testing.T) {
	nodes := []Node{{Name: "me", Kind: KindSelf, Pos: LatLon{51.44, -2.6}}, {Name: "near", Kind: KindRepeater, Pos: LatLon{51.5, -2.5}},
		{Name: "far", Kind: KindRepeater, Pos: LatLon{-33.9, 151.2}}}
	v := View{Center: LatLon{51.44, -2.6}, Zoom: 9, W: 200, H: 120}
	var names []string
	for _, m := range buildScene(Input{World: LoadWorld(), Nodes: nodes, Layers: Layers{Labels: true, Selected: -1}}, v, PaletteFor(theme.Neon)).marks {
		names = append(names, m.label)
	}
	if !slices.Equal(names, []string{"me", "near"}) {
		t.Errorf("marks = %q", names)
	}
}

func TestNearEndsOnAnyLongitude(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, lon := range []float64{math.Inf(1), math.Inf(-1), math.NaN(), 1e300, -1e300} {
			near(lon, 0)
			View{Zoom: 5, W: 100, H: 100}.Project(LatLon{51, lon})
			MeshArea([]LatLon{{51, -2}, {51, lon}})
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("near never returned")
	}
	for _, c := range []struct{ lon, ref, want float64 }{{190, 0, -170}, {-190, 0, 170}, {725, 0, 5}, {-2.6, -2.5, -2.6}, {179, -179, -181}} {
		if got := near(c.lon, c.ref); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("near(%v, %v) = %v, want %v", c.lon, c.ref, got, c.want)
		}
	}
}

func TestStopsFarOffTheRoute(t *testing.T) {
	nodes := []Node{{Name: "me", Kind: KindSelf, Pos: LatLon{51, -2}}}
	for i := range 3 {
		nodes = append(nodes, Node{Name: "far", Kind: KindRepeater, Pos: LatLon{-51, 178}, Key: [32]byte{0xAA, byte(i)}})
	}
	st := Stops(0, [][]byte{{0xAA}}, nodes, 0)[1]
	for _, w := range st.Weights {
		if math.Abs(w-1.0/3) > 1e-9 {
			t.Fatalf("weights = %v", st.Weights)
		}
	}
}

func TestStopsKeepTheLikeliest(t *testing.T) {
	nodes := []Node{{Name: "me", Kind: KindSelf, Pos: LatLon{51.44, -2.6}}}
	for i := range 300 {
		nodes = append(nodes, Node{Kind: KindRepeater, Pos: LatLon{51.44 + float64(i)*0.01, -2.6}, Key: [32]byte{0xAA, byte(i), byte(i >> 8)}})
	}
	var x Index
	sum := x.Sum(nodes)
	st := x.Stops(0, [][]byte{{0xAA}}, nodes, 0)
	if !slices.Equal(st[1].Nodes, []int{1, 2, 3}) {
		t.Errorf("stops = %+v", st)
	}
	if st := x.Stops(0, [][]byte{{0xAA, 0, 1}}, nodes, 0); !slices.Equal(st[1].Nodes, []int{257}) {
		t.Errorf("a longer hash = %+v", st)
	}
	total := 0.0
	for _, w := range st[1].Weights {
		total += w
	}
	if math.Abs(total-1) > 1e-9 {
		t.Errorf("weights = %v", st[1].Weights)
	}
	if x.Sum(nodes) != sum {
		t.Error("the same nodes changed the sum")
	}
	nodes[5].Pos.Lat += 0.001
	if new(Index).Sum(nodes) == sum {
		t.Error("a candidate moved and the sum stayed")
	}
	if x.Sum(append(nodes, Node{Kind: KindRoom, Pos: LatLon{1, 1}, Key: [32]byte{0xAA}})) == sum {
		t.Error("an appended candidate left the sum")
	}
}

// groupMarks, looking only in the cells around a mark, groups exactly as checking every group in turn does.
func TestGroupMarksMatchesEveryGroup(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	var marks []sMark
	for i := range 2000 {
		marks = append(marks, sMark{pos: LatLon{51 + rng.Float64()*2, -3 + rng.Float64()*3}, glyph: '▲', label: fmt.Sprint(i)})
	}
	marks[700].glyph = '◉'
	for _, zoom := range []float64{4, 6.5, 8} {
		got := groupMarks(append([]sMark(nil), marks...), zoom)
		if want := groupSlowly(append([]sMark(nil), marks...), zoom); !reflect.DeepEqual(got, want) {
			t.Errorf("zoom %v: %d groups, want %d", zoom, len(got), len(want))
		}
	}
}

func groupSlowly(marks []sMark, zoom float64) [][]sMark {
	sort.SliceStable(marks, func(i, j int) bool { return marks[i].glyph == '◉' && marks[j].glyph != '◉' })
	var groups [][]sMark
	for _, m := range marks {
		x, y := worldXY(m.pos, zoom)
		g := slices.IndexFunc(groups, func(g []sMark) bool {
			gx, gy := worldXY(g[0].pos, zoom)
			return math.Hypot(gx-x, gy-y) <= clusterRadius
		})
		if g < 0 {
			groups = append(groups, nil)
			g = len(groups) - 1
		}
		groups[g] = append(groups[g], m)
	}
	return groups
}
