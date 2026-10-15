package mapview

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Stops turns a packet's journey into route stops: the sender (or -1 if it isn't placed), each hop the path
// names, then us. A hop's hash matches every placed repeater or room whose key starts with it; when several
// do, they're ranked by how far out of the way each is between the stops either side: a candidate that adds
// 5 km is about a third as likely as one on the direct line.
func Stops(from int, hops [][]byte, nodes []Node, self int) []Stop {
	stops := []Stop{one(from)}
	for _, h := range hops {
		st := Stop{Hash: strings.ToUpper(fmt.Sprintf("%X", h))}
		for i, n := range nodes {
			if (n.Kind == KindRepeater || n.Kind == KindRoom) && !n.Pos.Unplaced() && bytes.HasPrefix(n.Key[:], h) {
				st.Nodes = append(st.Nodes, i)
			}
		}
		for range st.Nodes {
			st.Weights = append(st.Weights, 1/float64(len(st.Nodes)))
		}
		stops = append(stops, st)
	}
	stops = append(stops, one(self))
	for i := range stops {
		if len(stops[i].Nodes) > 1 {
			rank(stops, i, nodes)
		}
	}
	return stops
}

func one(n int) Stop {
	if n < 0 {
		return Stop{}
	}
	return Stop{Nodes: []int{n}, Weights: []float64{1}}
}

func rank(stops []Stop, i int, nodes []Node) {
	nearest := func(step int) (LatLon, bool) {
		for j := i + step; j >= 0 && j < len(stops); j += step {
			if len(stops[j].Nodes) == 1 {
				return nodes[stops[j].Nodes[0]].Pos, true
			}
		}
		return LatLon{}, false
	}
	prev, hasPrev := nearest(-1)
	next, hasNext := nearest(1)
	st := &stops[i]
	total := 0.0
	for k, n := range st.Nodes {
		detour := 0.0
		switch {
		case hasPrev && hasNext:
			detour = KmBetween(prev, nodes[n].Pos) + KmBetween(nodes[n].Pos, next) - KmBetween(prev, next)
		case hasPrev:
			detour = KmBetween(prev, nodes[n].Pos)
		case hasNext:
			detour = KmBetween(nodes[n].Pos, next)
		}
		st.Weights[k] = math.Exp(-detour / 5)
		total += st.Weights[k]
	}
	for k := range st.Weights {
		st.Weights[k] /= total
	}
	order := make([]int, len(st.Nodes))
	for k := range order {
		order[k] = k
	}
	sort.SliceStable(order, func(a, b int) bool { return st.Weights[order[a]] > st.Weights[order[b]] })
	ns, ws := make([]int, len(order)), make([]float64, len(order))
	for k, o := range order {
		ns[k], ws[k] = st.Nodes[o], st.Weights[o]
	}
	st.Nodes, st.Weights = ns, ws
}
