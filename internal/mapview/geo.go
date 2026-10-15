// Package mapview draws the Map screen: a world base map, optional street detail for the mesh's area,
// nodes, routes and live traffic, as braille text. See SPEC.md §9.
package mapview

import (
	"math"
	"sort"
)

type LatLon struct{ Lat, Lon float64 }

// Unplaced reports a node that never set a location, which advertises 0,0.
func (p LatLon) Unplaced() bool { return math.Abs(p.Lat) < 0.01 && math.Abs(p.Lon) < 0.01 }

// View is a Web Mercator viewport measured in braille dots, which are roughly square.
type View struct {
	Center LatLon
	Zoom   float64
	W, H   int // in dots
}

const (
	MinZoom = 2.0
	MaxZoom = 15.0
)

func worldXY(p LatLon, zoom float64) (float64, float64) {
	scale := 256 * math.Exp2(zoom)
	lat := min(max(p.Lat, -85), 85)
	s := math.Sin(lat * math.Pi / 180)
	return (p.Lon + 180) / 360 * scale, (0.5 - math.Log((1+s)/(1-s))/(4*math.Pi)) * scale
}

func fromWorld(x, y, zoom float64) LatLon {
	scale := 256 * math.Exp2(zoom)
	n := math.Pi - 2*math.Pi*y/scale
	return LatLon{180 / math.Pi * math.Atan(math.Sinh(n)), x/scale*360 - 180}
}

// near puts a longitude on the side of the world closest to ref, so a view across 180° stays joined up.
func near(lon, ref float64) float64 {
	for lon-ref > 180 {
		lon -= 360
	}
	for lon-ref < -180 {
		lon += 360
	}
	return lon
}

// Project gives a point's position in dots from the view's top left.
func (v View) Project(p LatLon) (float64, float64) {
	p.Lon = near(p.Lon, v.Center.Lon)
	cx, cy := worldXY(v.Center, v.Zoom)
	x, y := worldXY(p, v.Zoom)
	return x - cx + float64(v.W)/2, y - cy + float64(v.H)/2
}

// projector is Project with the view's constants worked out once, for drawing many points.
func (v View) projector() func(LatLon) (float64, float64) {
	cx, cy := worldXY(v.Center, v.Zoom)
	scale := 256 * math.Exp2(v.Zoom)
	ox, oy := float64(v.W)/2-cx, float64(v.H)/2-cy
	ref := v.Center.Lon
	return func(p LatLon) (float64, float64) {
		lon := near(p.Lon, ref)
		lat := min(max(p.Lat, -85), 85)
		s := math.Sin(lat * math.Pi / 180)
		return (lon+180)/360*scale + ox, (0.5-math.Log((1+s)/(1-s))/(4*math.Pi))*scale + oy
	}
}

// At is the point under a dot.
func (v View) At(x, y float64) LatLon {
	cx, cy := worldXY(v.Center, v.Zoom)
	p := fromWorld(cx+x-float64(v.W)/2, cy+y-float64(v.H)/2, v.Zoom)
	p.Lon = near(p.Lon, 0)
	return p
}

// Pan moves the view by fractions of its size.
func (v View) Pan(fx, fy float64) View {
	v.Center = v.At(float64(v.W)*(0.5+fx), float64(v.H)*(0.5+fy))
	return v
}

func (v View) Zoomed(by float64) View {
	v.Zoom = min(max(v.Zoom+by, MinZoom), MaxZoom)
	return v
}

// Between is part way from one view to another, taking the short way across 180°.
func Between(a, b View, t float64) View {
	v := b
	lon := near(a.Center.Lon, b.Center.Lon)
	v.Center = LatLon{a.Center.Lat + (b.Center.Lat-a.Center.Lat)*t, near(lon+(b.Center.Lon-lon)*t, 0)}
	v.Zoom = a.Zoom + (b.Zoom-a.Zoom)*t
	return v
}

// Bounds is the area on screen as W, S, E, N; W and E can run past ±180 when the view crosses it.
func (v View) Bounds() Box {
	nw, se := v.At(0, 0), v.At(float64(v.W), float64(v.H))
	half := float64(v.W) / (256 * math.Exp2(v.Zoom)) * 180
	return Box{v.Center.Lon - half, se.Lat, v.Center.Lon + half, nw.Lat}
}

// Box is W, S, E, N in degrees. W can be more than E's value minus 360 when it crosses 180°.
type Box [4]float64

// Contains allows for a box that runs past ±180.
func (b Box) Contains(p LatLon) bool {
	lon := near(p.Lon, (b[0]+b[2])/2)
	return lon >= b[0] && lon <= b[2] && p.Lat >= b[1] && p.Lat <= b[3]
}

// Overlaps tries the other box a world to either side, for views across 180°.
func (b Box) Overlaps(o Box) bool {
	for _, shift := range []float64{-360, 0, 360} {
		if o[0]+shift <= b[2] && o[2]+shift >= b[0] && o[1] <= b[3] && o[3] >= b[1] {
			return true
		}
	}
	return false
}

func (b Box) Grow(f float64) Box {
	w, h := (b[2]-b[0])*f, (b[3]-b[1])*f
	return Box{b[0] - w, b[1] - h, b[2] + w, b[3] + h}
}

// Fit centres the view on points and zooms in as far as keeps them all inside, less a margin.
func (v View) Fit(pts []LatLon, margin float64) View {
	if len(pts) == 0 {
		return v
	}
	box := extentOf(pts)
	v.Center = LatLon{(box[1] + box[3]) / 2, near((box[0]+box[2])/2, 0)}
	v.Zoom = MinZoom
	for z := 13.0; z > MinZoom; z -= 0.25 {
		x0, y0 := worldXY(LatLon{box[3], box[0]}, z)
		x1, y1 := worldXY(LatLon{box[1], box[2]}, z)
		if x1-x0 <= float64(v.W)*(1-margin) && y1-y0 <= float64(v.H)*(1-margin) {
			v.Zoom = z
			break
		}
	}
	return v
}

// extentOf boxes points, keeping a group across 180° together rather than spanning the world.
func extentOf(pts []LatLon) Box {
	b := Box{1e9, 90, -1e9, -90}
	for _, p := range pts {
		lon := near(p.Lon, pts[0].Lon)
		b = Box{min(b[0], lon), min(b[1], p.Lat), max(b[2], lon), max(b[3], p.Lat)}
	}
	return b
}

func KmBetween(a, b LatLon) float64 {
	const r = 6371.0
	dLat := (b.Lat - a.Lat) * math.Pi / 180
	dLon := near(b.Lon-a.Lon, 0) * math.Pi / 180
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(a.Lat*math.Pi/180)*math.Cos(b.Lat*math.Pi/180)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}

// MeshArea finds the mesh among node positions: those near the median, skipping 0,0 and outliers more than
// four times the median distance away (and at least 25 km). It returns them and a box around them with a margin.
func MeshArea(pts []LatLon) ([]LatLon, Box, bool) {
	var placed []LatLon
	for _, p := range pts {
		if !p.Unplaced() {
			placed = append(placed, p)
		}
	}
	if len(placed) == 0 {
		return nil, Box{}, false
	}
	ref := placed[0].Lon
	lats, lons := make([]float64, len(placed)), make([]float64, len(placed))
	for i, p := range placed {
		lats[i], lons[i] = p.Lat, near(p.Lon, ref)
	}
	sort.Float64s(lats)
	sort.Float64s(lons)
	mid := LatLon{lats[len(lats)/2], lons[len(lons)/2]}
	dist := make([]float64, len(placed))
	for i, p := range placed {
		dist[i] = KmBetween(mid, p)
	}
	sorted := append([]float64(nil), dist...)
	sort.Float64s(sorted)
	limit := max(4*sorted[len(sorted)/2], 25)
	var keep []LatLon
	for i, p := range placed {
		if dist[i] <= limit {
			keep = append(keep, LatLon{p.Lat, near(p.Lon, mid.Lon)})
		}
	}
	b := extentOf(keep)
	padW, padH := max((b[2]-b[0])*0.15, 0.1), max((b[3]-b[1])*0.15, 0.06)
	return keep, Box{b[0] - padW, b[1] - padH, b[2] + padW, b[3] + padH}, true
}
