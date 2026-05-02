// Command geobuild reads OSM features as line-delimited GeoJSON
// (RFC 8142 / "geojsonseq") on stdin or from -in, computes a
// representative point for each feature (Point coords for nodes,
// bounding-box center for ways/relations), maps OSM tags to the
// internal place_type taxonomy used by internal/geo, and writes a
// sqlite database with R-Tree spatial index.
//
// Each input line is one GeoJSON Feature. Features may optionally
// be prefixed with the RFC 7464 record-separator byte (0x1E); we
// tolerate it.
//
// Mapping rules:
//
//	place=city/town/village/suburb/neighbourhood/quarter/hamlet/
//	      locality/isolated_dwelling/farm/island
//	  -> place_type = the value verbatim
//	natural=peak / spring
//	  -> place_type = "peak" / "spring"
//	leisure=park / stadium
//	  -> place_type = "park" / "stadium"
//	amenity=university / hospital
//	  -> place_type = "university" / "hospital"
//	boundary=protected_area
//	  -> place_type = "protected_area"
//	landuse=industrial / residential / commercial (with name)
//	  -> place_type = "landuse"
//
// Features without a name tag are dropped. Features whose computed
// geometry has a bounding-box diagonal larger than maxDiagonalKm
// are also dropped: a 100km-long protected area centroided to a
// point is more confusing than informative for flight narration.
//
// Usage:
//
//	osmium tags-filter -o filtered.osm.pbf brazil-latest.osm.pbf <tag predicates>
//	osmium export -f geojsonseq -o features.geojsonseq filtered.osm.pbf
//	geobuild -in features.geojsonseq -out places.db
package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"

	_ "modernc.org/sqlite"
)

// maxDiagonalKm caps the bounding-box size of a feature we'll
// represent as a single point. Beyond this, the "centroid" is too
// fictional to mention as a place. Tuned by hand: a 10km park is
// big but its center is still meaningful; a 100km protected area
// isn't.
const maxDiagonalKm = 10.0

// placeTypeFromTags maps an OSM tag set to the internal taxonomy.
// Returns "" if no rule fires.
func placeTypeFromTags(tags map[string]string) string {
	if v := tags["place"]; v != "" {
		switch v {
		case "city", "town", "village", "suburb", "neighbourhood",
			"quarter", "hamlet", "locality", "isolated_dwelling",
			"farm", "island":
			return v
		}
	}
	if v := tags["natural"]; v != "" {
		switch v {
		case "peak", "spring":
			return v
		}
	}
	if v := tags["leisure"]; v != "" {
		switch v {
		case "park", "stadium":
			return v
		}
	}
	if v := tags["amenity"]; v != "" {
		switch v {
		case "university", "hospital":
			return v
		}
	}
	if tags["boundary"] == "protected_area" {
		return "protected_area"
	}
	if v := tags["landuse"]; v != "" {
		switch v {
		case "industrial", "residential", "commercial":
			return "landuse"
		}
	}
	return ""
}

// feature is a partial GeoJSON Feature shape. We only read the
// fields we need.
type feature struct {
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties"`
	Geometry   geometry               `json:"geometry"`
}

type geometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

func main() {
	in := flag.String("in", "-", "input geojsonseq file (\"-\" for stdin)")
	out := flag.String("out", "", "output sqlite path (required)")
	flag.Parse()

	if *out == "" {
		log.Fatal("geobuild: -out is required")
	}

	var r io.Reader = os.Stdin
	if *in != "-" {
		f, err := os.Open(*in)
		if err != nil {
			log.Fatalf("geobuild: open %s: %v", *in, err)
		}
		defer f.Close()
		r = f
	}

	_ = os.Remove(*out)
	db, err := sql.Open("sqlite", "file:"+*out)
	if err != nil {
		log.Fatalf("geobuild: open db: %v", err)
	}
	defer db.Close()

	for _, s := range []string{
		`PRAGMA journal_mode = OFF`,
		`PRAGMA synchronous = OFF`,
		`PRAGMA temp_store = MEMORY`,
		`CREATE TABLE places (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			place_type TEXT NOT NULL,
			lat REAL NOT NULL,
			lon REAL NOT NULL
		)`,
		`CREATE VIRTUAL TABLE places_rtree USING rtree(
			id, min_lat, max_lat, min_lon, max_lon
		)`,
	} {
		if _, err := db.Exec(s); err != nil {
			log.Fatalf("geobuild: schema: %v", err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	insP, err := tx.Prepare(`INSERT INTO places(id,name,place_type,lat,lon) VALUES(?,?,?,?,?)`)
	if err != nil {
		log.Fatal(err)
	}
	defer insP.Close()
	insR, err := tx.Prepare(`INSERT INTO places_rtree(id,min_lat,max_lat,min_lon,max_lon) VALUES(?,?,?,?,?)`)
	if err != nil {
		log.Fatal(err)
	}
	defer insR.Close()

	scan := bufio.NewScanner(r)
	// GeoJSON lines can be long for big multipolygons; bump the
	// scanner buffer well past Go's default 64KB cap.
	scan.Buffer(make([]byte, 0, 1<<20), 1<<26)

	var read, kept, skipped int
	id := int64(1)
	for scan.Scan() {
		read++
		line := scan.Bytes()
		// Tolerate the optional RFC 7464 0x1E record separator.
		for len(line) > 0 && line[0] == 0x1E {
			line = line[1:]
		}
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var f feature
		if err := json.Unmarshal(line, &f); err != nil {
			skipped++
			continue
		}
		name, _ := f.Properties["name"].(string)
		if name == "" {
			skipped++
			continue
		}
		tags := stringTags(f.Properties)
		ptype := placeTypeFromTags(tags)
		if ptype == "" {
			skipped++
			continue
		}
		lat, lon, ok := representativePoint(f.Geometry)
		if !ok {
			skipped++
			continue
		}
		if _, err := insP.Exec(id, name, ptype, lat, lon); err != nil {
			log.Fatalf("geobuild: insert place: %v", err)
		}
		if _, err := insR.Exec(id, lat, lat, lon, lon); err != nil {
			log.Fatalf("geobuild: insert rtree: %v", err)
		}
		id++
		kept++
	}
	if err := scan.Err(); err != nil {
		log.Fatalf("geobuild: scan: %v", err)
	}
	if err := tx.Commit(); err != nil {
		log.Fatalf("geobuild: commit: %v", err)
	}
	if _, err := db.Exec(`PRAGMA optimize`); err != nil {
		log.Fatalf("geobuild: optimize: %v", err)
	}

	fmt.Fprintf(os.Stderr, "geobuild: read %d features, kept %d, skipped %d, wrote %s\n",
		read, kept, skipped, *out)
}

// stringTags reduces the JSON-decoded properties map to a
// string->string map. OSM tags are always strings; anything that
// isn't (rare oddball) is dropped.
func stringTags(props map[string]interface{}) map[string]string {
	out := make(map[string]string, len(props))
	for k, v := range props {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// representativePoint returns a single (lat, lon) that stands in
// for the feature. For Point, that's the coordinates directly.
// For other geometries we use the bounding-box center, but only
// if the bbox diagonal is below maxDiagonalKm. ok=false means the
// feature is too sprawling or the geometry was unparseable.
func representativePoint(g geometry) (lat, lon float64, ok bool) {
	switch g.Type {
	case "Point":
		var c [2]float64
		if err := json.Unmarshal(g.Coordinates, &c); err != nil {
			return 0, 0, false
		}
		// GeoJSON: [lon, lat]
		return c[1], c[0], true
	case "Polygon":
		var rings [][][2]float64
		if err := json.Unmarshal(g.Coordinates, &rings); err != nil {
			return 0, 0, false
		}
		return centerOfRings(rings)
	case "MultiPolygon":
		var polys [][][][2]float64
		if err := json.Unmarshal(g.Coordinates, &polys); err != nil {
			return 0, 0, false
		}
		// Flatten outer rings of all polygons. Good enough for
		// "where is this thing" purposes.
		var all [][][2]float64
		for _, p := range polys {
			if len(p) > 0 {
				all = append(all, p[0])
			}
		}
		return centerOfRings(all)
	case "LineString", "MultiLineString":
		// Layer A+B doesn't include rivers/streams; if we ever
		// pick those up we'll need polyline distance, not centroid.
		return 0, 0, false
	}
	return 0, 0, false
}

// centerOfRings returns the bounding-box center of a polygon's
// rings (we use the outer ring; holes don't change the bbox much
// for our purposes). Drops the feature if the bbox diagonal in km
// is too large.
func centerOfRings(rings [][][2]float64) (lat, lon float64, ok bool) {
	if len(rings) == 0 || len(rings[0]) == 0 {
		return 0, 0, false
	}
	minLon, minLat := math.Inf(1), math.Inf(1)
	maxLon, maxLat := math.Inf(-1), math.Inf(-1)
	for _, ring := range rings {
		for _, pt := range ring {
			lo, la := pt[0], pt[1]
			if lo < minLon {
				minLon = lo
			}
			if lo > maxLon {
				maxLon = lo
			}
			if la < minLat {
				minLat = la
			}
			if la > maxLat {
				maxLat = la
			}
		}
	}
	if math.IsInf(minLon, 0) || math.IsInf(minLat, 0) {
		return 0, 0, false
	}
	cLat := (minLat + maxLat) / 2
	cLon := (minLon + maxLon) / 2
	// Bounding-box diagonal in km. If it's too large, the
	// centroid is misleading.
	diag := haversineKm(minLat, minLon, maxLat, maxLon)
	if diag > maxDiagonalKm {
		return 0, 0, false
	}
	return cLat, cLon, true
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthKm = 6371.0088
	rad := math.Pi / 180.0
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	la1 := lat1 * rad
	la2 := lat2 * rad
	sLat := math.Sin(dLat / 2)
	sLon := math.Sin(dLon / 2)
	a := sLat*sLat + math.Cos(la1)*math.Cos(la2)*sLon*sLon
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthKm * c
}
