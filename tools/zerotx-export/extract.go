// Package main: zerotx-export reads SQLite recordings produced by
// the daemon's recorder package and emits GPX or KML for use in
// Google Earth, qgroundcontrol, or any other post-flight analyzer.
//
// This file contains the data extraction layer: opens the .db,
// queries session + telemetry + events tables, and returns a Flight
// struct that the gpx.go / kml.go writers serialize. Schema
// knowledge is duplicated here intentionally; the tool is small
// and standalone, and the daemon's recorder package lives in an
// internal/ subtree we can't import from a separate module.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Flight is everything we extracted from one recording session.
// All times are in UTC at extract time; the writers apply
// time.Local just before formatting so the output is in the
// operator's wall-clock zone.
type Flight struct {
	SessionID int64
	Started   time.Time
	Ended     time.Time // may be zero if the session never disarmed cleanly
	ModelName string

	// GroundAlt is the reference altitude used to compute relative
	// altitudes. Set to the gps_alt of the first telemetry sample
	// with a valid GPS fix. If no valid fix was ever recorded,
	// stays at zero (and altitude in the export is meaningless;
	// the writers tolerate that case by emitting <ele>0</ele>).
	GroundAlt int32

	Track     []Sample   // ordered by time ascending
	Waypoints []Waypoint // arm, disarm, failsafe, RTH, peaks, home-set
}

// Sample is one telemetry sample with valid GPS data.
type Sample struct {
	Time       time.Time
	LatDeg     float64
	LonDeg     float64
	AltMeters  int32 // MSL as recorded; relative-mode subtracts GroundAlt at write time
	GroundKmh  float64
	HeadingDeg float64
}

// Waypoint is a notable event lifted into a named point for the
// GPX/KML output. Lat/lon/alt are taken from the closest preceding
// telemetry sample (or the event's own detail blob when it has
// position data, like home-set).
type Waypoint struct {
	Time      time.Time
	LatDeg    float64
	LonDeg    float64
	AltMeters int32
	Name      string // human label: "Takeoff", "Failsafe", ...
	Kind      string // event kind: "arm", "failsafe", ...
	Detail    string // optional one-line extra (e.g. "180 m" for peaks)
}

// ExtractFlight opens dbPath, reads its single session, and returns
// the populated Flight. Multi-session recordings aren't a thing in
// the current daemon design (one .db per arm/disarm cycle), so we
// pick whichever session has the most telemetry samples on the off
// chance the file has more than one.
func ExtractFlight(dbPath string) (*Flight, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	defer db.Close()

	// Defensive: lots of dbs we extract from come off other machines
	// with possibly different SQLite versions. Don't let metadata
	// PRAGMA flutter cause weird errors.
	if _, err := db.Exec("PRAGMA journal_mode = OFF;"); err != nil {
		// Non-fatal: read-only access doesn't need it.
		_ = err
	}

	sid, err := pickSession(db)
	if err != nil {
		return nil, err
	}

	f := &Flight{SessionID: sid}
	if err := readSessionMeta(db, sid, f); err != nil {
		return nil, fmt.Errorf("session meta: %w", err)
	}
	if err := readTelemetry(db, sid, f); err != nil {
		return nil, fmt.Errorf("telemetry: %w", err)
	}
	if err := readEvents(db, sid, f); err != nil {
		return nil, fmt.Errorf("events: %w", err)
	}
	return f, nil
}

// pickSession returns the session_id with the most telemetry rows.
// Most recordings have exactly one session (one arm/disarm), but
// we tolerate multi-session files gracefully.
func pickSession(db *sql.DB) (int64, error) {
	rows, err := db.Query(`
		SELECT s.id, COUNT(t.ts_us)
		FROM sessions s
		LEFT JOIN telemetry t ON t.session_id = s.id
		GROUP BY s.id
		ORDER BY COUNT(t.ts_us) DESC, s.id DESC
		LIMIT 1`)
	if err != nil {
		return 0, fmt.Errorf("pickSession query: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, errors.New("no sessions in recording")
	}
	var id, n int64
	if err := rows.Scan(&id, &n); err != nil {
		return 0, fmt.Errorf("pickSession scan: %w", err)
	}
	return id, nil
}

func readSessionMeta(db *sql.DB, sid int64, f *Flight) error {
	var startedTxt, endedTxt sql.NullString
	var modelName sql.NullString
	err := db.QueryRow(`SELECT started_at, ended_at, model_name FROM sessions WHERE id = ?`, sid).
		Scan(&startedTxt, &endedTxt, &modelName)
	if err != nil {
		return err
	}
	if startedTxt.Valid {
		t, err := parseDBTime(startedTxt.String)
		if err != nil {
			return fmt.Errorf("parse started_at %q: %w", startedTxt.String, err)
		}
		f.Started = t
	}
	if endedTxt.Valid {
		if t, err := parseDBTime(endedTxt.String); err == nil {
			f.Ended = t
		}
	}
	if modelName.Valid {
		f.ModelName = modelName.String
	}
	return nil
}

// parseDBTime handles whichever RFC3339-ish format the recorder
// happens to emit. Recorder writes time.Now().UTC().Format(time.RFC3339Nano)
// today; we accept both nano- and second-precision.
func parseDBTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// readTelemetry pulls every GPS-valid telemetry sample and
// captures GroundAlt from the first one. "Valid" means non-NULL
// lat/lon AND not the (0,0) null-island wire artifact.
func readTelemetry(db *sql.DB, sid int64, f *Flight) error {
	rows, err := db.Query(`
		SELECT ts_us, gps_lat, gps_lon, gps_alt, gps_kmh, gps_hdg
		FROM telemetry
		WHERE session_id = ?
		  AND gps_lat IS NOT NULL AND gps_lon IS NOT NULL
		  AND NOT (gps_lat = 0 AND gps_lon = 0)
		ORDER BY ts_us ASC`, sid)
	if err != nil {
		return err
	}
	defer rows.Close()

	groundAltSet := false
	for rows.Next() {
		var (
			tsUs       int64
			lat, lon   float64
			alt        sql.NullInt32
			kmh, hdg   sql.NullFloat64
		)
		if err := rows.Scan(&tsUs, &lat, &lon, &alt, &kmh, &hdg); err != nil {
			return err
		}
		s := Sample{
			Time:   time.Unix(0, tsUs*1000).UTC(),
			LatDeg: lat,
			LonDeg: lon,
		}
		if alt.Valid {
			s.AltMeters = alt.Int32
			if !groundAltSet {
				f.GroundAlt = alt.Int32
				groundAltSet = true
			}
		}
		if kmh.Valid {
			s.GroundKmh = kmh.Float64
		}
		if hdg.Valid {
			s.HeadingDeg = hdg.Float64
		}
		f.Track = append(f.Track, s)
	}
	return rows.Err()
}

// readEvents pulls the named events we want to expose as waypoints.
// Each event's lat/lon/alt is filled from the closest preceding
// telemetry sample, OR from the event's own detail blob if it
// carries position (home-set does).
func readEvents(db *sql.DB, sid int64, f *Flight) error {
	rows, err := db.Query(`
		SELECT ts_us, kind, name, detail
		FROM events
		WHERE session_id = ?
		  AND name IN ('arm', 'disarm', 'failsafe', 'rth-active',
		               'home-set', 'peak-altitude', 'peak-distance')
		ORDER BY ts_us ASC`, sid)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			tsUs   int64
			kind   string
			name   sql.NullString
			detail sql.NullString
		)
		if err := rows.Scan(&tsUs, &kind, &name, &detail); err != nil {
			return err
		}
		evTime := time.Unix(0, tsUs*1000).UTC()
		w := Waypoint{
			Time: evTime,
			Kind: name.String, // event "name" column is what we display
			Name: humanWaypointName(name.String),
		}
		// Position priority:
		//   1) The event's own detail blob if it carries lat/lon
		//      (home-set does this; possibly others in the future).
		//   2) Nearest preceding telemetry sample's lat/lon/alt.
		if detail.Valid && fillFromDetail(&w, detail.String) {
			// Detail provided lat/lon; alt may still be 0. Find a
			// nearby telemetry sample for the altitude.
			if alt, ok := nearestAlt(f.Track, evTime); ok {
				w.AltMeters = alt
			}
		} else if lat, lon, alt, ok := nearestSample(f.Track, evTime); ok {
			w.LatDeg = lat
			w.LonDeg = lon
			w.AltMeters = alt
		}
		// If detail has useful extra data (peak values), surface it.
		if detail.Valid {
			w.Detail = summarizeDetail(name.String, detail.String)
		}
		f.Waypoints = append(f.Waypoints, w)
	}
	return rows.Err()
}

// humanWaypointName maps event names to operator-friendly labels.
func humanWaypointName(eventName string) string {
	switch eventName {
	case "arm":
		return "Takeoff"
	case "disarm":
		return "Landing"
	case "failsafe":
		return "Failsafe"
	case "rth-active":
		return "RTH"
	case "home-set":
		return "Home"
	case "peak-altitude":
		return "Peak altitude"
	case "peak-distance":
		return "Peak distance"
	default:
		return eventName
	}
}

// fillFromDetail parses the detail JSON looking for lat/lon. Returns
// true if it found and populated both.
func fillFromDetail(w *Waypoint, detail string) bool {
	var d struct {
		Lat *float64 `json:"lat"`
		Lon *float64 `json:"lon"`
	}
	if err := json.Unmarshal([]byte(detail), &d); err != nil {
		return false
	}
	if d.Lat == nil || d.Lon == nil {
		return false
	}
	w.LatDeg = *d.Lat
	w.LonDeg = *d.Lon
	return true
}

// summarizeDetail returns a short string fragment (e.g. "180 m")
// extracted from event-specific detail JSON. Best-effort; returns
// "" if nothing useful is found.
func summarizeDetail(eventName, detail string) string {
	switch eventName {
	case "peak-altitude":
		var d struct {
			AltMeters *float64 `json:"altMeters"`
			Meters    *float64 `json:"meters"`
		}
		if err := json.Unmarshal([]byte(detail), &d); err == nil {
			if d.AltMeters != nil {
				return fmt.Sprintf("%.0f m", *d.AltMeters)
			}
			if d.Meters != nil {
				return fmt.Sprintf("%.0f m", *d.Meters)
			}
		}
	case "peak-distance":
		var d struct {
			DistMeters *float64 `json:"distMeters"`
			Meters     *float64 `json:"meters"`
		}
		if err := json.Unmarshal([]byte(detail), &d); err == nil {
			if d.DistMeters != nil {
				return fmt.Sprintf("%.0f m", *d.DistMeters)
			}
			if d.Meters != nil {
				return fmt.Sprintf("%.0f m", *d.Meters)
			}
		}
	}
	return ""
}

// nearestSample returns the lat/lon/alt of the telemetry sample
// whose time is closest to t. Tracks are sorted by time so we
// binary-search.
func nearestSample(track []Sample, t time.Time) (lat, lon float64, alt int32, ok bool) {
	if len(track) == 0 {
		return 0, 0, 0, false
	}
	i := sort.Search(len(track), func(i int) bool {
		return !track[i].Time.Before(t)
	})
	// i is the first index with Time >= t. Compare i and i-1.
	if i == len(track) {
		s := track[i-1]
		return s.LatDeg, s.LonDeg, s.AltMeters, true
	}
	if i == 0 {
		s := track[0]
		return s.LatDeg, s.LonDeg, s.AltMeters, true
	}
	before := track[i-1]
	after := track[i]
	if t.Sub(before.Time) <= after.Time.Sub(t) {
		return before.LatDeg, before.LonDeg, before.AltMeters, true
	}
	return after.LatDeg, after.LonDeg, after.AltMeters, true
}

// nearestAlt is the same as nearestSample but returns only the
// altitude. Used when the event's own detail already provided lat/lon.
func nearestAlt(track []Sample, t time.Time) (int32, bool) {
	_, _, alt, ok := nearestSample(track, t)
	return alt, ok
}
