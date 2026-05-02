package main

import (
	"github.com/agoliveira/zerotx/pi/daemon/internal/geo"
)

// geoAdapter bridges *geo.Lookup (which returns rich Match objects)
// to the narrator.GeoLookup interface (which only needs a name
// string). Returns "" when no place is in-threshold so the narrator
// omits the location phrase.
type geoAdapter struct {
	g *geo.Lookup
}

func (a geoAdapter) NearestName(lat, lon float64) string {
	if a.g == nil {
		return ""
	}
	m := a.g.Nearest(lat, lon)
	if m == nil {
		return ""
	}
	return m.Name
}
