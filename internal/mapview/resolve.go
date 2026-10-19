package mapview

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
)

// MaxCandidates is how many repeaters a hop is shared between at most: the likeliest, by fit.
const MaxCandidates = 3

// Stops turns a packet's journey into route stops: the sender (or -1 if it isn't placed), each hop the path
// names, then us. A hop's hash matches every placed repeater or room whose key starts with it; when several
// do, they're ranked by how far out of the way each is between the stops either side: a candidate that adds
// 5 km is about a third as likely as one on the direct line. Only the likeliest MaxCandidates are kept.
func Stops(from int, hops [][]byte, nodes []Node, self int) []Stop {
	return new(Index).Stops(from, hops, nodes, self)
}

// Index finds the repeaters and rooms a hop could be by their first key byte, so many routes over the same
// nodes don't each scan them all. Nodes appended to the slice later are picked up as it's used.
type Index struct {
	by  [256][]int // placed
	all [256][]int // placed or not
	n   int
	sum uint64
}

func (x *Index) add(nodes []Node) {
	for ; x.n < len(nodes); x.n++ {
		n := nodes[x.n]
		if n.Kind != KindRepeater && n.Kind != KindRoom {
			continue
		}
		x.all[n.Key[0]] = append(x.all[n.Key[0]], x.n)
		if !n.Pos.Unplaced() {
			x.by[n.Key[0]] = append(x.by[n.Key[0]], x.n)
			h := fnv.New64a()
			var b [24]byte
			binary.LittleEndian.PutUint64(b[:], uint64(x.n))
			binary.LittleEndian.PutUint64(b[8:], math.Float64bits(n.Pos.Lat))
			binary.LittleEndian.PutUint64(b[16:], math.Float64bits(n.Pos.Lon))
			h.Write(b[:])
			h.Write(n.Key[:])
			x.sum += h.Sum64()
		}
	}
}

// Sum changes when the candidates do, or move: the same hops over the same Sum give the same stops.
func (x *Index) Sum(nodes []Node) uint64 {
	x.add(nodes)
	return x.sum
}

func (x *Index) Stops(from int, hops [][]byte, nodes []Node, self int) []Stop {
	x.add(nodes)
	stops := []Stop{one(from)}
	for _, h := range hops {
		st := Stop{Hash: strings.ToUpper(fmt.Sprintf("%X", h))}
		if len(h) > 0 {
			for _, i := range x.by[h[0]] {
				if bytes.HasPrefix(nodes[i].Key[:], h) {
					st.Nodes = append(st.Nodes, i)
				}
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

// Matches are the repeaters and rooms, placed or not, whose keys start with h.
func (x *Index) Matches(h []byte, nodes []Node) []int {
	x.add(nodes)
	if len(h) == 0 {
		return nil
	}
	var out []int
	for _, i := range x.all[h[0]] {
		if bytes.HasPrefix(nodes[i].Key[:], h) {
			out = append(out, i)
		}
	}
	return out
}

// Hop is who a path hash names: every repeater or room whose key starts with it.
type Hop struct {
	Hash  []byte
	Nodes []int
	Guess bool // several share it, and where they are makes Nodes[0] the likeliest
}

// Hops names a path's hashes for showing them. Unlike Stops it counts unplaced nodes too, so a hash several
// share is only a guess when every one of them is placed and the route ranks one above the rest.
func (x *Index) Hops(from int, hops [][]byte, nodes []Node, self int) []Hop {
	var stops []Stop
	out := make([]Hop, len(hops))
	for i, h := range hops {
		hop := Hop{Hash: h, Nodes: x.Matches(h, nodes)}
		if len(hop.Nodes) > 1 && placed(hop.Nodes, nodes) {
			if stops == nil {
				stops = x.Stops(from, hops, nodes, self)
			}
			if st := stops[i+1]; len(st.Nodes) > 1 && st.Weights[0] > st.Weights[1] {
				hop.Nodes, hop.Guess = st.Nodes, true
			}
		}
		out[i] = hop
	}
	return out
}

func placed(is []int, nodes []Node) bool {
	for _, i := range is {
		if nodes[i].Pos.Unplaced() {
			return false
		}
	}
	return true
}

// Label is how a hop reads: its one node's name, the likeliest with a "?", or the names it could be.
func (h Hop) Label(nodes []Node) string {
	switch {
	case len(h.Nodes) == 0:
		return fmt.Sprintf("%X", h.Hash)
	case len(h.Nodes) == 1:
		return nodes[h.Nodes[0]].Name
	case h.Guess:
		return nodes[h.Nodes[0]].Name + "?"
	}
	names := make([]string, len(h.Nodes))
	for k, i := range h.Nodes {
		names[k] = nodes[i].Name
	}
	sort.Strings(names)
	if len(names) > MaxCandidates {
		names = append(names[:MaxCandidates], fmt.Sprintf("%d more", len(names)-MaxCandidates))
	}
	return strings.Join(names, " or ")
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
	}
	order := make([]int, len(st.Nodes))
	for k := range order {
		order[k] = k
	}
	sort.SliceStable(order, func(a, b int) bool { return st.Weights[order[a]] > st.Weights[order[b]] })
	order = order[:min(len(order), MaxCandidates)]
	ns, ws := make([]int, len(order)), make([]float64, len(order))
	total := 0.0
	for k, o := range order {
		ns[k], ws[k] = st.Nodes[o], st.Weights[o]
		total += ws[k]
	}
	for k := range ws {
		if total > 0 && !math.IsInf(total, 0) {
			ws[k] /= total
		} else {
			ws[k] = 1 / float64(len(ws)) // so far off the route that every weight underflowed
		}
	}
	st.Nodes, st.Weights = ns, ws
}
