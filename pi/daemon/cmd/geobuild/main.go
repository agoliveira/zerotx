// Command geobuild reads OSM data in OPL format (one feature per
// line) on stdin or from -in, filters for place=* nodes, and writes
// a sqlite database with R-Tree spatial index suitable for
// internal/geo to query.
//
// OPL format reference:
//
//	https://osmcode.org/opl-file-format/
//
// We only care about node lines. A node line looks like:
//
//	n123456 v1 dV c789 t2024-01-01T00:00:00Z i1 uuser Tname=Salto,place=town x-47.29 y-23.20
//
// Field types we read: T (tags) x (lon) y (lat). Everything else is
// ignored.
//
// Tag values that contain commas, equals, percent signs, spaces,
// or newlines are %-encoded by osmium. The decode is just URL-style
// percent-decoding restricted to the byte set OPL escapes.
//
// Usage:
//
//	# typical pipeline:
//	osmium tags-filter -o places.opl -f opl brazil-latest.osm.pbf \
//	    n/place=town,village,suburb,neighbourhood,quarter,hamlet,locality,isolated_dwelling,farm,city
//	geobuild -in places.opl -out places.db
//
// The output schema matches what internal/geo expects.
package main

import (
	"bufio"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

// kept ranks the place types we care to keep. The set must be a
// superset of internal/geo's typeMaxDistanceM.
var kept = map[string]bool{
	"isolated_dwelling": true,
	"farm":              true,
	"hamlet":            true,
	"locality":          true,
	"neighbourhood":     true,
	"suburb":            true,
	"quarter":           true,
	"village":           true,
	"town":              true,
	"city":              true,
}

func main() {
	in := flag.String("in", "-", "input OPL file (\"-\" for stdin)")
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

	// Re-create the output file from scratch so a re-run produces
	// a clean db without stale rows.
	_ = os.Remove(*out)
	db, err := sql.Open("sqlite", "file:"+*out)
	if err != nil {
		log.Fatalf("geobuild: open db: %v", err)
	}
	defer db.Close()

	for _, s := range []string{
		// PRAGMAs for fast bulk insert.
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
	scan.Buffer(make([]byte, 0, 1<<20), 1<<20)

	var read, kept int
	id := int64(1)
	for scan.Scan() {
		read++
		line := scan.Text()
		if len(line) == 0 || line[0] != 'n' {
			continue
		}
		name, ptype, lat, lon, ok := parseNode(line)
		if !ok {
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

	fmt.Fprintf(os.Stderr, "geobuild: read %d lines, kept %d places, wrote %s\n", read, kept, *out)
}

// parseNode pulls the fields we care about from one OPL node line.
// Returns ok=false if the line lacks a usable name + place tag, or
// has malformed coordinates, or its place type is not in `kept`.
func parseNode(line string) (name, ptype string, lat, lon float64, ok bool) {
	// Fields are separated by spaces but tag values may contain
	// %-encoded spaces (decoded as 0x20 only after we split).
	// Iterate field-by-field by leading char.
	fields := strings.Split(line, " ")
	var tags string
	var hasX, hasY bool
	for _, f := range fields {
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case 'T':
			tags = f[1:]
		case 'x':
			v, err := strconv.ParseFloat(f[1:], 64)
			if err != nil {
				return
			}
			lon = v
			hasX = true
		case 'y':
			v, err := strconv.ParseFloat(f[1:], 64)
			if err != nil {
				return
			}
			lat = v
			hasY = true
		}
	}
	if !hasX || !hasY {
		return
	}
	if tags == "" {
		return
	}
	// Tags: comma-separated key=value pairs, %-encoded.
	// Pull name and place out.
	for _, kv := range strings.Split(tags, ",") {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		k := oplDecode(kv[:eq])
		v := oplDecode(kv[eq+1:])
		switch k {
		case "name":
			name = v
		case "place":
			ptype = v
		}
	}
	if name == "" || ptype == "" {
		return
	}
	if !kept[ptype] {
		return
	}
	ok = true
	return
}

// oplDecode reverses OPL's percent-encoding. OPL escapes the bytes
// 0x00-0x20, '%', ',', '=' as %XX (uppercase hex). We do a permissive
// pass: any %XX where XX is two hex digits is decoded; anything else
// is passed through.
func oplDecode(s string) string {
	if !strings.ContainsRune(s, '%') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '%' && i+2 < len(s) {
			h := hexNibble(s[i+1])
			l := hexNibble(s[i+2])
			if h >= 0 && l >= 0 {
				b.WriteByte(byte(h<<4 | l))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
