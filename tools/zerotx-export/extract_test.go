package main

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// schemaSQL mirrors what the daemon's recorder package creates.
// Kept inline so the test doesn't reach across module boundaries.
const schemaSQL = `
CREATE TABLE sessions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at  TEXT NOT NULL,
    ended_at    TEXT,
    model_name  TEXT,
    model_path  TEXT,
    notes       TEXT
);
CREATE TABLE events (
    session_id  INTEGER NOT NULL,
    ts_us       INTEGER NOT NULL,
    kind        TEXT NOT NULL,
    name        TEXT,
    level       TEXT,
    detail      TEXT
);
CREATE TABLE telemetry (
    session_id  INTEGER NOT NULL,
    ts_us       INTEGER NOT NULL,
    bat_volts   REAL,
    bat_amps    REAL,
    bat_pct     INTEGER,
    bat_mah     INTEGER,
    gps_lat     REAL,
    gps_lon     REAL,
    gps_alt     INTEGER,
    gps_kmh     REAL,
    gps_hdg     REAL,
    gps_sats    INTEGER,
    link_rssi   INTEGER,
    link_lq     INTEGER,
    link_snr    INTEGER,
    fm_mode     TEXT,
    attitude_roll  REAL,
    attitude_pitch REAL,
    attitude_yaw   REAL
);`

// buildTestDB creates a temporary .db file with a single session
// + a small but realistic set of telemetry and events.
func buildTestDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flight.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}

	now := time.Date(2025, 5, 19, 17, 30, 0, 0, time.UTC)
	if _, err := db.Exec(
		`INSERT INTO sessions (id, started_at, model_name) VALUES (1, ?, ?)`,
		now.Format(time.RFC3339Nano), "test-model"); err != nil {
		t.Fatalf("session insert: %v", err)
	}

	// Five samples: first sets the ground reference (alt=120).
	// Subsequent samples climb and descend.
	for i, alt := range []int32{120, 145, 180, 210, 180} {
		ts := now.Add(time.Duration(i) * time.Second).UnixMicro()
		if _, err := db.Exec(`
			INSERT INTO telemetry (session_id, ts_us, gps_lat, gps_lon, gps_alt, gps_kmh, gps_hdg)
			VALUES (1, ?, ?, ?, ?, ?, ?)`,
			ts, -22.90+float64(i)*0.0001, -47.06+float64(i)*0.0001, alt, 35.5, 90.0); err != nil {
			t.Fatalf("telemetry insert %d: %v", i, err)
		}
	}

	// Two events: arm at t=0, peak-altitude at t=3.
	armTs := now.UnixMicro()
	peakTs := now.Add(3 * time.Second).UnixMicro()
	if _, err := db.Exec(`
		INSERT INTO events (session_id, ts_us, kind, name, level, detail) VALUES
			(1, ?, 'flight', 'arm', 'info', NULL),
			(1, ?, 'flight', 'peak-altitude', 'info', '{"meters":210}')`,
		armTs, peakTs); err != nil {
		t.Fatalf("event insert: %v", err)
	}

	// One event with NO matching telemetry (before the first sample)
	// to exercise the nearestSample edge case.
	earlyTs := now.Add(-1 * time.Second).UnixMicro()
	if _, err := db.Exec(`
		INSERT INTO events (session_id, ts_us, kind, name, level, detail) VALUES
			(1, ?, 'flight', 'home-set', 'info', '{"lat":-22.91,"lon":-47.07}')`,
		earlyTs); err != nil {
		t.Fatalf("home-set insert: %v", err)
	}

	return path
}

func TestExtractFlight_BasicShape(t *testing.T) {
	path := buildTestDB(t)
	f, err := ExtractFlight(path)
	if err != nil {
		t.Fatalf("ExtractFlight: %v", err)
	}
	if f.SessionID != 1 {
		t.Errorf("SessionID = %d, want 1", f.SessionID)
	}
	if f.ModelName != "test-model" {
		t.Errorf("ModelName = %q, want test-model", f.ModelName)
	}
	if len(f.Track) != 5 {
		t.Errorf("len(Track) = %d, want 5", len(f.Track))
	}
	if f.GroundAlt != 120 {
		t.Errorf("GroundAlt = %d, want 120 (first sample's alt)", f.GroundAlt)
	}
	if len(f.Waypoints) != 3 {
		t.Errorf("len(Waypoints) = %d, want 3 (arm, peak-altitude, home-set)", len(f.Waypoints))
	}
}

func TestExtractFlight_HomeSetUsesDetailLatLon(t *testing.T) {
	path := buildTestDB(t)
	f, err := ExtractFlight(path)
	if err != nil {
		t.Fatalf("ExtractFlight: %v", err)
	}
	var home *Waypoint
	for i := range f.Waypoints {
		if f.Waypoints[i].Kind == "home-set" {
			home = &f.Waypoints[i]
			break
		}
	}
	if home == nil {
		t.Fatal("home-set waypoint not found")
	}
	// Verify the detail-blob lat/lon won (-22.91, -47.07), not the
	// nearest-telemetry lat/lon (-22.90, -47.06).
	if home.LatDeg != -22.91 || home.LonDeg != -47.07 {
		t.Errorf("home lat/lon = (%v, %v), want (-22.91, -47.07) from detail blob",
			home.LatDeg, home.LonDeg)
	}
}

func TestExtractFlight_DropsZeroIslandSamples(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zeros.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, started_at) VALUES (1, '2025-05-19T17:30:00Z')`); err != nil {
		t.Fatal(err)
	}
	// Three rows: only the middle one has a real fix.
	if _, err := db.Exec(`
		INSERT INTO telemetry (session_id, ts_us, gps_lat, gps_lon, gps_alt) VALUES
			(1, 1000, 0, 0, 0),
			(1, 2000, -22.9, -47.06, 120),
			(1, 3000, NULL, NULL, NULL)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	f, err := ExtractFlight(path)
	if err != nil {
		t.Fatalf("ExtractFlight: %v", err)
	}
	if len(f.Track) != 1 {
		t.Errorf("len(Track) = %d, want 1 (only the real-fix sample)", len(f.Track))
	}
}

func TestWriteGPX_ContainsExpectedElements(t *testing.T) {
	f, err := ExtractFlight(buildTestDB(t))
	if err != nil {
		t.Fatalf("ExtractFlight: %v", err)
	}
	var buf bytes.Buffer
	if err := WriteGPX(&buf, f, AltRelative); err != nil {
		t.Fatalf("WriteGPX: %v", err)
	}
	out := buf.String()

	required := []string{
		`<?xml version="1.0"`,
		`<gpx`,
		`version="1.1"`,
		`creator="zerotx-export"`,
		`<metadata>`,
		`<wpt`,
		`Takeoff`,
		`Peak altitude`,
		`<trk>`,
		`<trkseg>`,
		`<trkpt`,
	}
	for _, s := range required {
		if !strings.Contains(out, s) {
			t.Errorf("GPX output missing %q", s)
		}
	}

	// Relative-altitude mode: first sample's <ele> should be 0
	// (gps_alt 120 minus GroundAlt 120).
	if !strings.Contains(out, "<ele>0</ele>") {
		t.Error("expected at least one <ele>0</ele> in relative mode")
	}

	// MSL mode emits the raw 120.
	buf.Reset()
	if err := WriteGPX(&buf, f, AltAbsolute); err != nil {
		t.Fatalf("WriteGPX MSL: %v", err)
	}
	if !strings.Contains(buf.String(), "<ele>120</ele>") {
		t.Error("expected <ele>120</ele> in MSL mode")
	}
}

func TestWriteKML_ContainsExpectedElements(t *testing.T) {
	f, err := ExtractFlight(buildTestDB(t))
	if err != nil {
		t.Fatalf("ExtractFlight: %v", err)
	}
	var buf bytes.Buffer
	if err := WriteKML(&buf, f, AltRelative); err != nil {
		t.Fatalf("WriteKML: %v", err)
	}
	out := buf.String()

	required := []string{
		`<?xml version="1.0"`,
		`<kml`,
		`<Document>`,
		`<Placemark>`,
		`<LineString>`,
		`Takeoff`,
		`Peak altitude`,
		`<altitudeMode>relativeToGround</altitudeMode>`,
		`<extrude>1</extrude>`,
	}
	for _, s := range required {
		if !strings.Contains(out, s) {
			t.Errorf("KML output missing %q", s)
		}
	}

	// MSL mode should swap altitudeMode.
	buf.Reset()
	if err := WriteKML(&buf, f, AltAbsolute); err != nil {
		t.Fatalf("WriteKML MSL: %v", err)
	}
	if !strings.Contains(buf.String(), `<altitudeMode>absolute</altitudeMode>`) {
		t.Error("expected <altitudeMode>absolute</altitudeMode> in MSL mode")
	}
}

func TestParseAltitude(t *testing.T) {
	cases := map[string]AltMode{
		"relative": AltRelative,
		"REL":      AltRelative,
		"agl":      AltRelative,
		"msl":      AltAbsolute,
		"absolute": AltAbsolute,
		"ABS":      AltAbsolute,
	}
	for input, want := range cases {
		got, err := parseAltitude(input)
		if err != nil {
			t.Errorf("parseAltitude(%q) error: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("parseAltitude(%q) = %v, want %v", input, got, want)
		}
	}
	if _, err := parseAltitude("bogus"); err == nil {
		t.Error("parseAltitude(\"bogus\") = nil error, want error")
	}
}

func TestInferFormatFromPath(t *testing.T) {
	cases := map[string]string{
		"a.gpx":         "gpx",
		"a.GPX":         "gpx",
		"a.kml":         "kml",
		"flight.kml":    "kml",
		"flight.txt":    "",
		"noext":         "",
		"/path/x.gpx":   "gpx",
	}
	for input, want := range cases {
		got := inferFormatFromPath(input)
		if got != want {
			t.Errorf("inferFormatFromPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNearestSample_Edges(t *testing.T) {
	base := time.Now().UTC()
	track := []Sample{
		{Time: base.Add(0 * time.Second), LatDeg: 1, LonDeg: 10, AltMeters: 100},
		{Time: base.Add(5 * time.Second), LatDeg: 2, LonDeg: 20, AltMeters: 200},
		{Time: base.Add(10 * time.Second), LatDeg: 3, LonDeg: 30, AltMeters: 300},
	}
	// Before track start: should return first sample.
	lat, _, _, ok := nearestSample(track, base.Add(-1*time.Second))
	if !ok || lat != 1 {
		t.Errorf("before-start: lat=%v want 1", lat)
	}
	// After track end: should return last sample.
	lat, _, _, ok = nearestSample(track, base.Add(20*time.Second))
	if !ok || lat != 3 {
		t.Errorf("after-end: lat=%v want 3", lat)
	}
	// Closer to second sample.
	lat, _, _, ok = nearestSample(track, base.Add(6*time.Second))
	if !ok || lat != 2 {
		t.Errorf("near-second: lat=%v want 2", lat)
	}
	// Equidistant ties go to before (i-1).
	lat, _, _, ok = nearestSample(track, base.Add(2500*time.Millisecond))
	if !ok || lat != 1 {
		t.Errorf("tie: lat=%v want 1 (before wins)", lat)
	}
	// Empty track.
	_, _, _, ok = nearestSample(nil, base)
	if ok {
		t.Error("empty track returned ok=true")
	}
}

func TestExtractFlight_NoSessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = ExtractFlight(path)
	if err == nil {
		t.Error("expected error for empty DB; got nil")
	}
}
