// Package geo provides offline reverse-geocoding for ZeroTX flight
// narration. Given a (lat, lon) point it returns the most specific
// nearby place name from a sqlite database built by tools/geobuild
// out of OpenStreetMap place=* nodes.
//
// Resolution scales with type: a flight that reaches peak altitude
// inside a neighbourhood gets the neighbourhood name, not the
// surrounding city. Each place type has a distance threshold beyond
// which it's no longer mentioned (a 200m flight shouldn't match a
// town 8km away). When nothing is in-threshold, Nearest returns nil
// and the narrator omits the location phrase.
//
// The database is a one-time build artefact (~30-50MB for Brazil-
// wide place nodes) and ships separately from the daemon binary.
// Daemon falls back to no-location narration if the file is missing.
package geo

import (
	"database/sql"
	"math"
	"sort"

	_ "modernc.org/sqlite"
)

// Match is a single resolved place. DistanceM is the great-circle
// distance from the queried point to the place's centroid.
type Match struct {
	Name      string
	PlaceType string
	Lat, Lon  float64
	DistanceM float64
}

// Lookup is an opened reverse-geocode database.
type Lookup struct {
	db *sql.DB
}

// Open returns a Lookup backed by the given sqlite file. The file
// must have been produced by tools/geobuild (schema below). Caller
// must Close when done.
//
// Schema:
//
//	CREATE TABLE places (
//	    id INTEGER PRIMARY KEY,
//	    name TEXT NOT NULL,
//	    place_type TEXT NOT NULL,
//	    lat REAL NOT NULL,
//	    lon REAL NOT NULL
//	);
//	CREATE VIRTUAL TABLE places_rtree USING rtree(
//	    id, min_lat, max_lat, min_lon, max_lon
//	);
//
// places_rtree is populated with each place's lat/lon as both min
// and max (point R-Tree). Spatial pre-filter happens on places_rtree;
// haversine ranking on the candidate set.
func Open(path string) (*Lookup, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	// Cheap smoke test: confirm the schema is what we expect.
	if _, err := db.Exec("SELECT 1 FROM places LIMIT 1"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Lookup{db: db}, nil
}

// Close releases the underlying sqlite handle.
func (l *Lookup) Close() error {
	if l == nil || l.db == nil {
		return nil
	}
	return l.db.Close()
}

// Per-type max distance to mention, in meters. A place's match is
// dropped if its centroid is farther than this from the query point.
// More specific types have tighter thresholds because their named
// area is smaller; a "town" can legitimately be the right name from
// 7km away, a "neighbourhood" cannot.
//
// Anything not in this map is dropped entirely (we only mention
// types we've thought about).
var typeMaxDistanceM = map[string]float64{
	"isolated_dwelling": 500,
	"farm":              500,
	"hamlet":            1000,
	"locality":          1000,
	"neighbourhood":     1500,
	"suburb":            1500,
	"quarter":           1500,
	"village":           3000,
	"town":              7000,
	"city":              15000,
}

// typePrecedence ranks specificity. Lower = more specific = preferred
// when multiple matches exist within their thresholds. Anything not
// listed here gets a high (less-preferred) value implicitly.
var typePrecedence = map[string]int{
	"isolated_dwelling": 1,
	"farm":              1,
	"hamlet":            2,
	"locality":          2,
	"neighbourhood":     3,
	"suburb":            3,
	"quarter":           3,
	"village":           4,
	"town":              5,
	"city":              6,
}

// Nearest returns the most-specific place within its threshold, or
// nil if nothing qualifies. The returned Match's DistanceM is the
// great-circle distance from the query point.
func (l *Lookup) Nearest(lat, lon float64) *Match {
	if l == nil || l.db == nil {
		return nil
	}

	// Bounding box pre-filter: 0.2 degrees in each direction is
	// roughly +/- 22km at the equator and tighter at higher
	// latitudes. Larger than the largest threshold (city, 15km),
	// so all candidates that could qualify are inside the box.
	const dDeg = 0.2

	rows, err := l.db.Query(`
		SELECT p.name, p.place_type, p.lat, p.lon
		FROM places p
		JOIN places_rtree r ON p.id = r.id
		WHERE r.min_lat >= ? AND r.max_lat <= ?
		  AND r.min_lon >= ? AND r.max_lon <= ?
	`, lat-dDeg, lat+dDeg, lon-dDeg, lon+dDeg)
	if err != nil {
		return nil
	}
	defer rows.Close()

	candidates := make([]Match, 0, 32)
	for rows.Next() {
		var m Match
		if err := rows.Scan(&m.Name, &m.PlaceType, &m.Lat, &m.Lon); err != nil {
			continue
		}
		m.DistanceM = HaversineM(lat, lon, m.Lat, m.Lon)
		max, ok := typeMaxDistanceM[m.PlaceType]
		if !ok || m.DistanceM > max {
			continue
		}
		candidates = append(candidates, m)
	}
	if len(candidates) == 0 {
		return nil
	}

	// Sort by precedence (specific first), then by distance.
	sort.Slice(candidates, func(i, j int) bool {
		pi := typePrecedence[candidates[i].PlaceType]
		pj := typePrecedence[candidates[j].PlaceType]
		if pi == 0 {
			pi = 99
		}
		if pj == 0 {
			pj = 99
		}
		if pi != pj {
			return pi < pj
		}
		return candidates[i].DistanceM < candidates[j].DistanceM
	})
	best := candidates[0]
	return &best
}

// HaversineM returns the great-circle distance in meters between two
// points specified as decimal degrees. Earth radius constant is the
// IUGG mean (6371008.8m); the small error vs WGS-84 ellipsoidal
// distance is irrelevant for our use case (placement narration at
// kilometer scales).
func HaversineM(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusM = 6371008.8
	rad := math.Pi / 180.0
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	la1 := lat1 * rad
	la2 := lat2 * rad
	sLat := math.Sin(dLat / 2)
	sLon := math.Sin(dLon / 2)
	a := sLat*sLat + math.Cos(la1)*math.Cos(la2)*sLon*sLon
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusM * c
}
