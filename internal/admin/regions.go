package admin

import "strings"

// Region is one line of the firmware's `region` tree.
type Region struct {
	Name  string
	Depth int
	Home  bool
	Flood bool
}

// ParseRegions reads `region` output: one space of indent per level, "^" marks home, " F" allows flood.
// Replies are cut at 160 bytes, so a deep tree can arrive truncated; a partial last line is dropped.
func ParseRegions(text string) []Region {
	var out []Region
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" || i == len(lines)-1 && len(text) >= 160 {
			continue
		}
		r := Region{Depth: len(l) - len(strings.TrimLeft(l, " "))}
		f := strings.Fields(l)
		r.Name = f[0]
		r.Flood = len(f) > 1 && f[1] == "F"
		if n, ok := strings.CutSuffix(r.Name, "^"); ok {
			r.Name, r.Home = n, true
		}
		out = append(out, r)
	}
	return out
}
