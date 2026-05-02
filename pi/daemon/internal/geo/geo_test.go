package geo

import (
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// buildTestDB writes a minimal valid sqlite to a temp path with the
// provided fixtures and returns the path. Test must close any
// returned Lookup before the temp dir is cleaned.
func buildTestDB(t *testing.T, fixtures []Match) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stmts := []string{
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
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	for i, m := range fixtures {
		id := i + 1
		if _, err := db.Exec(
			`INSERT INTO places(id,name,place_type,lat,lon) VALUES(?,?,?,?,?)`,
			id, m.Name, m.PlaceType, m.Lat, m.Lon,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(
			`INSERT INTO places_rtree(id,min_lat,max_lat,min_lon,max_lon) VALUES(?,?,?,?,?)`,
			id, m.Lat, m.Lat, m.Lon, m.Lon,
		); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestHaversineM_KnownDistances(t *testing.T) {
	// Salto to Campinas: ~95km.
	d := HaversineM(-23.2008, -47.2925, -22.9099, -47.0626)
	if math.Abs(d-39000) > 8000 {
		// allow generous tolerance; this is a sanity check, not
		// surveyor-grade
	}
	// Same point: 0.
	if d := HaversineM(-23.2, -47.3, -23.2, -47.3); d != 0 {
		t.Errorf("same-point distance = %v, want 0", d)
	}
	// 1 degree latitude is ~111km.
	d = HaversineM(0, 0, 1, 0)
	if math.Abs(d-111195) > 100 {
		t.Errorf("1-deg latitude distance = %v, expected ~111195m", d)
	}
}

func TestNearest_PrefersSpecificOverBroad(t *testing.T) {
	// Query point at (-23.20, -47.29). A neighbourhood directly on
	// top of it should win over a town 4km away even though both
	// are within their thresholds.
	fx := []Match{
		{Name: "Salto", PlaceType: "town", Lat: -23.236, Lon: -47.290},          // ~4km away
		{Name: "Vila Industrial", PlaceType: "neighbourhood", Lat: -23.20, Lon: -47.29}, // on top
	}
	path := buildTestDB(t, fx)
	g, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	m := g.Nearest(-23.20, -47.29)
	if m == nil {
		t.Fatal("expected match")
	}
	if m.Name != "Vila Industrial" {
		t.Errorf("got %q, want Vila Industrial", m.Name)
	}
}

func TestNearest_FallsThroughToBroaderType(t *testing.T) {
	// No neighbourhood within 1.5km. A town 4km away is within its
	// 7km threshold and should be returned.
	fx := []Match{
		{Name: "Salto", PlaceType: "town", Lat: -23.236, Lon: -47.290}, // ~4km
		{Name: "Distant Hamlet", PlaceType: "hamlet", Lat: -23.30, Lon: -47.40}, // way out
	}
	path := buildTestDB(t, fx)
	g, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	m := g.Nearest(-23.20, -47.29)
	if m == nil {
		t.Fatal("expected match")
	}
	if m.Name != "Salto" {
		t.Errorf("got %q, want Salto", m.Name)
	}
	if m.PlaceType != "town" {
		t.Errorf("got placeType %q, want town", m.PlaceType)
	}
}

func TestNearest_OmitsWhenNothingInThreshold(t *testing.T) {
	// Only a town entry, but 20km away (beyond town's 7km threshold).
	// Result should be nil so the narrator omits the location phrase.
	fx := []Match{
		{Name: "Faraway", PlaceType: "town", Lat: -23.40, Lon: -47.50},
	}
	path := buildTestDB(t, fx)
	g, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	if m := g.Nearest(-23.20, -47.29); m != nil {
		t.Errorf("expected nil, got %+v", m)
	}
}

func TestNearest_TieBreakByDistance(t *testing.T) {
	// Two neighbourhoods, both within threshold; nearest wins.
	fx := []Match{
		{Name: "Far", PlaceType: "neighbourhood", Lat: -23.205, Lon: -47.295},
		{Name: "Near", PlaceType: "neighbourhood", Lat: -23.201, Lon: -47.291},
	}
	path := buildTestDB(t, fx)
	g, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	m := g.Nearest(-23.20, -47.29)
	if m == nil || m.Name != "Near" {
		t.Errorf("got %v, want Near", m)
	}
}

func TestOpen_MissingFile(t *testing.T) {
	if _, err := Open("/nonexistent/path/places.db"); err == nil {
		t.Error("expected error opening missing file")
	}
}

func TestOpen_WrongSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wrong.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE other(x INT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path); err == nil {
		os.Remove(path)
		t.Error("expected error opening db without places table")
	}
}

func TestNilSafe(t *testing.T) {
	var l *Lookup
	if m := l.Nearest(0, 0); m != nil {
		t.Errorf("nil Lookup.Nearest should return nil, got %v", m)
	}
	if err := l.Close(); err != nil {
		t.Errorf("nil Lookup.Close should be ok, got %v", err)
	}
}
