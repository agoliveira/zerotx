// Package recorder writes flight session data to SQLite for post-flight
// analysis. The design is "tmpfs while flying, save on disarm":
//
//   1. On daemon boot, an in-memory SQLite database is opened (or one
//      backed by a tmpfs path; the daemon doesn't care which). Events
//      and telemetry samples are written there throughout pre-flight.
//
//   2. On Arm, a new session row is inserted. Events and telemetry
//      written from then on are tagged with that session ID.
//
//   3. On Disarm, the session is closed, the in-memory DB is COPIED
//      (via SQLite's online backup API) to a persistent file at
//      <recordings dir>/<timestamp>-<model>.db. A fresh in-memory DB
//      replaces the working one so the next flight is independent.
//
//   4. Rolling cleanup keeps the N most-recent recordings; older
//      ones are deleted on each save.
//
// Power loss between Arm and Disarm = lost recording. This is by
// design: writing to flash storage during flight risks corrupting the
// FS or stalling the daemon's tick loop. The trade-off is acceptable
// for hobby use; a 12V SLA UPS is the proper redundancy.
//
// Why pure-Go SQLite (modernc.org/sqlite)?
// CGo would mean cross-compilation pain (the Pi target builds with
// native gcc; CGo cross-builds need a sysroot). Pure Go costs ~3MB
// in the binary and is slower than CGo SQLite, but at our write
// rate (single-digit rows per second) the perf difference is
// completely irrelevant.
package recorder

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// schemaSQL initialises a fresh recording database. The same schema
// is used for in-memory and saved-to-disk databases.
//
// All timestamps are microseconds since epoch (UTC). SQLite stores
// these as INTEGER which is 8-byte signed and good for ~292,000 years.
const schemaSQL = `
PRAGMA journal_mode = MEMORY;
PRAGMA synchronous = OFF;

CREATE TABLE IF NOT EXISTS sessions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at  TEXT NOT NULL,
    ended_at    TEXT,
    model_name  TEXT,
    model_path  TEXT,
    notes       TEXT
);

CREATE TABLE IF NOT EXISTS events (
    session_id  INTEGER NOT NULL,
    ts_us       INTEGER NOT NULL,
    kind        TEXT NOT NULL,
    name        TEXT,
    level       TEXT,
    detail      TEXT
);
CREATE INDEX IF NOT EXISTS events_session_ts ON events (session_id, ts_us);

CREATE TABLE IF NOT EXISTS telemetry (
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

    -- Attitude in degrees. Captured at the same cadence as the rest
    -- of telemetry (subject to the 200 ms throttle). Recorded so
    -- replay can drive the HUD artificial horizon and the GLCD pitch
    -- ladder; without these columns the replay UI would show level
    -- flight regardless of what the aircraft was actually doing.
    -- Roll: positive = right wing down. Pitch: positive = nose up.
    -- Yaw: 0-360, compass-style. Matches telemetry.Attitude.
    attitude_roll  REAL,
    attitude_pitch REAL,
    attitude_yaw   REAL
);
CREATE INDEX IF NOT EXISTS telemetry_session_ts ON telemetry (session_id, ts_us);
`

// telemetryThrottle caps telemetry sample frequency. The FC may emit
// at 10Hz+ but storing every duplicate row is wasteful. 5Hz is plenty
// for any post-flight analysis we'd do.
const telemetryThrottle = 200 * time.Millisecond

// Default rolling cleanup keeps this many most-recent saved recordings.
const defaultKeepRecordings = 10

// Config controls the recorder.
type Config struct {
	// RecordingsDir is where saved-on-disarm databases land. Created
	// if it doesn't exist. A typical value: ~/zerotx/recordings.
	RecordingsDir string

	// Keep is the number of most-recent recordings to retain. Older
	// files are deleted on each save. Zero falls back to default (10).
	Keep int

	// WorkingPath is the SQLite path used for the in-memory DB. The
	// pure-Go SQLite driver supports ":memory:" and shared-cache
	// memory URIs. Empty defaults to a fresh in-memory DB per session.
	// A tmpfs path (e.g. /run/zerotx/working.db) also works and lets
	// external tools peek at the live recording for debugging.
	WorkingPath string
}

// Recorder is the public surface. Methods are safe for concurrent use.
type Recorder struct {
	cfg Config

	mu        sync.Mutex
	db        *sql.DB
	sessionID int64       // 0 means "no active session" (pre-arm)
	startedAt time.Time   // session-start anchor for ts_us conversion

	// Per-session metadata captured at OnArm and used at SaveAndRotate.
	modelName string
	modelPath string

	// Telemetry throttle: we only insert rows at most once per
	// telemetryThrottle to avoid flooding the DB with near-duplicates.
	lastTelemetryAt time.Time

	// preserveReason, when non-empty at SaveAndRotate time, causes a
	// <recording>.preserve sidecar file to be written next to the
	// saved .db. cleanupOldRecordings skips any .db whose sidecar
	// exists, so preserved recordings survive rotation indefinitely.
	// Reset to empty after the sidecar is written.
	preserveReason string

	// Closed flag protects against late goroutines after Close.
	closed atomic.Bool
}

// New opens (or creates) the working database and applies the schema.
// Failure is non-fatal at the daemon level — recorder errors must not
// affect flight; the daemon checks the returned error and keeps going
// with a NoOp recorder if this fails.
func New(cfg Config) (*Recorder, error) {
	if cfg.Keep <= 0 {
		cfg.Keep = defaultKeepRecordings
	}
	if cfg.RecordingsDir == "" {
		return nil, fmt.Errorf("recorder: RecordingsDir required")
	}
	if err := os.MkdirAll(cfg.RecordingsDir, 0o755); err != nil {
		return nil, fmt.Errorf("recorder: create recordings dir: %w", err)
	}

	r := &Recorder{cfg: cfg}
	if err := r.openWorking(); err != nil {
		return nil, err
	}
	return r, nil
}

// openWorking opens (or re-opens) the working in-memory database. Used
// at startup and after a save-and-rotate to start fresh for the next
// flight.
func (r *Recorder) openWorking() error {
	dsn := r.cfg.WorkingPath
	if dsn == "" {
		// Anonymous in-memory; new file each time.
		dsn = ":memory:"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("recorder: open: %w", err)
	}
	// In-memory databases share connections badly: SQLite treats each
	// connection as a separate database unless we use a shared-cache
	// URI. Pinning to a single connection sidesteps the issue
	// completely and is fine for our write rate.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), schemaSQL); err != nil {
		db.Close()
		return fmt.Errorf("recorder: schema: %w", err)
	}
	r.db = db
	r.sessionID = 0
	r.startedAt = time.Time{}
	r.lastTelemetryAt = time.Time{}
	return nil
}

// OnArm starts a new session row. modelName and modelPath are recorded
// in the session metadata for later identification of the recording
// file. Idempotent: calling twice in a row is a no-op (the second arm
// without a disarm in between is treated as the same session).
func (r *Recorder) OnArm(modelName, modelPath string) {
	if r.closed.Load() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sessionID != 0 {
		// Already armed; nothing to do.
		return
	}
	now := time.Now().UTC()
	res, err := r.db.ExecContext(context.Background(),
		`INSERT INTO sessions (started_at, model_name, model_path) VALUES (?, ?, ?)`,
		now.Format(time.RFC3339Nano), modelName, modelPath)
	if err != nil {
		log.Printf("recorder: insert session: %v", err)
		return
	}
	id, err := res.LastInsertId()
	if err != nil {
		log.Printf("recorder: lastinsertid: %v", err)
		return
	}
	r.sessionID = id
	r.startedAt = now
	r.modelName = modelName
	r.modelPath = modelPath

	r.recordEventLocked("armed", "", "", nil)
}

// OnDisarm closes the active session, saves the database to a
// persistent file, runs cleanup, and resets the working DB for the
// next flight. Returns the saved-file path or "" if there was nothing
// to save.
func (r *Recorder) OnDisarm() string {
	if r.closed.Load() {
		return ""
	}
	r.mu.Lock()

	if r.sessionID == 0 {
		// No active session; OnDisarm without OnArm. Tolerated.
		r.mu.Unlock()
		return ""
	}
	r.recordEventLocked("disarmed", "", "", nil)
	now := time.Now().UTC()
	if _, err := r.db.ExecContext(context.Background(),
		`UPDATE sessions SET ended_at = ? WHERE id = ?`,
		now.Format(time.RFC3339Nano), r.sessionID); err != nil {
		log.Printf("recorder: update session ended_at: %v", err)
	}

	// Build the saved-file name. <UTC timestamp>-<sanitised model>.db
	// Use the session-started time so disarm timing doesn't change
	// the filename mid-flight (avoids surprises if the operator
	// has a slow clock sync after boot). Millisecond resolution so
	// rapid arm/disarm cycles (mainly: tests) don't collide.
	stamp := r.startedAt.Format("20060102-150405.000")
	stamp = strings.Replace(stamp, ".", "-", 1)
	name := stamp + "-" + sanitiseFilename(r.modelName) + ".db"
	dst := filepath.Join(r.cfg.RecordingsDir, name)

	r.mu.Unlock()

	// Save outside the lock so a slow disk doesn't block writers.
	// During the save, telemetry/events being written to the working
	// DB are still captured into the about-to-rotate-out database;
	// they're lost on rotation. This is fine: the operator already
	// disarmed, no flight is in progress.
	if err := r.saveWorkingTo(dst); err != nil {
		log.Printf("recorder: save %s: %v", dst, err)
	} else {
		log.Printf("recorder: saved %s", dst)
		// If a preserve flag was set during this session (e.g., the
		// recovery state machine asked us to preserve on failsafe),
		// write a sidecar marker so cleanupOldRecordings won't touch
		// this file. The sidecar's content is the reason text for
		// later debugging.
		r.mu.Lock()
		reason := r.preserveReason
		r.preserveReason = ""
		r.mu.Unlock()
		if reason != "" {
			r.writePreserveSidecar(dst, reason)
		}
		r.cleanupOldRecordings()
	}

	// Rotate: close the working DB and open a fresh one for the next
	// session. Lock again for the swap.
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.db.Close(); err != nil {
		log.Printf("recorder: close working db: %v", err)
	}
	if err := r.openWorking(); err != nil {
		log.Printf("recorder: re-open working db: %v", err)
		// On failure, leave the recorder in a state where future
		// writes silently no-op. The flight path is unaffected.
		r.db = nil
	}
	return dst
}

// LogEvent records a generic event. Safe to call before OnArm; events
// recorded pre-arm are tagged with session_id=0 and remain in the
// working DB for context, but do not appear in the saved file (since
// the saved file only includes the rotated session).
//
// Wait — that's actually true: pre-arm events get session_id=0 and
// then are wiped on rotate. We could instead include them in the
// next saved session by retroactively assigning, but that complicates
// the model for marginal value. Simpler to drop pre-arm events from
// the saved file; the operator can read live logs via the GUI for
// pre-arm context.
func (r *Recorder) LogEvent(kind, name, level string, detail interface{}) {
	if r.closed.Load() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordEventLocked(kind, name, level, detail)
}

// recordEventLocked is the locked-mutex variant. Caller holds r.mu.
func (r *Recorder) recordEventLocked(kind, name, level string, detail interface{}) {
	if r.db == nil {
		return
	}
	tsUs := r.tsUsLocked()
	var detailJSON sql.NullString
	if detail != nil {
		b, err := json.Marshal(detail)
		if err == nil {
			detailJSON = sql.NullString{String: string(b), Valid: true}
		}
	}
	if _, err := r.db.ExecContext(context.Background(),
		`INSERT INTO events (session_id, ts_us, kind, name, level, detail) VALUES (?, ?, ?, ?, ?, ?)`,
		r.sessionID, tsUs, kind, name, level, detailJSON); err != nil {
		log.Printf("recorder: insert event: %v", err)
	}
}

// LogTelemetry inserts a telemetry sample. Throttled internally to
// telemetryThrottle (5Hz) to avoid duplicate rows when the FC emits
// faster than that.
//
// Snap is what the daemon's telemetry package produces — we accept it
// as a typed parameter set rather than an interface to keep the SQL
// straightforward.
func (r *Recorder) LogTelemetry(t TelemetrySample) {
	if r.closed.Load() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db == nil || r.sessionID == 0 {
		return // nothing armed; pre-arm telemetry not retained
	}
	now := time.Now()
	if !r.lastTelemetryAt.IsZero() && now.Sub(r.lastTelemetryAt) < telemetryThrottle {
		return
	}
	r.lastTelemetryAt = now

	tsUs := r.tsUsLocked()
	if _, err := r.db.ExecContext(context.Background(),
		`INSERT INTO telemetry (
			session_id, ts_us,
			bat_volts, bat_amps, bat_pct, bat_mah,
			gps_lat, gps_lon, gps_alt, gps_kmh, gps_hdg, gps_sats,
			link_rssi, link_lq, link_snr,
			fm_mode,
			attitude_roll, attitude_pitch, attitude_yaw
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.sessionID, tsUs,
		t.BatVolts, t.BatAmps, t.BatPct, t.BatMAh,
		t.GpsLat, t.GpsLon, t.GpsAlt, t.GpsKmh, t.GpsHdg, t.GpsSats,
		t.LinkRSSI, t.LinkLQ, t.LinkSNR,
		t.FlightMode,
		t.AttitudeRoll, t.AttitudePitch, t.AttitudeYaw); err != nil {
		log.Printf("recorder: insert telemetry: %v", err)
	}
}

// TelemetrySample is the daemon-friendly shape we expect from the
// telemetry package. All fields are pointer-typed so "no data" is
// distinguishable from "zero" (battery voltage 0.0 is a real reading).
type TelemetrySample struct {
	BatVolts   *float64
	BatAmps    *float64
	BatPct     *int
	BatMAh     *int

	GpsLat     *float64
	GpsLon     *float64
	GpsAlt     *int
	GpsKmh     *float64
	GpsHdg     *float64
	GpsSats    *int

	LinkRSSI   *int
	LinkLQ     *int
	LinkSNR    *int

	FlightMode *string

	// Attitude in degrees. nil if no fresh attitude frame seen in
	// telemetry (Stale=true on the daemon side). Replay consumes
	// these to animate the HUD artificial horizon and the GLCD
	// pitch ladder; with all three nil the replay shows level.
	AttitudeRoll  *float64
	AttitudePitch *float64
	AttitudeYaw   *float64
}

// tsUsLocked returns the timestamp in microseconds relative to the
// session's started_at anchor. Caller holds r.mu.
//
// Pre-arm (sessionID==0) timestamps are 0; they're not flushed to
// disk anyway and the value doesn't matter.
func (r *Recorder) tsUsLocked() int64 {
	if r.sessionID == 0 || r.startedAt.IsZero() {
		return 0
	}
	return time.Since(r.startedAt).Microseconds()
}

// saveWorkingTo copies the in-memory database to a file using SQLite's
// VACUUM INTO, which is the simplest path that doesn't require the
// pure-Go driver to expose the backup API directly.
func (r *Recorder) saveWorkingTo(dst string) error {
	r.mu.Lock()
	if r.db == nil {
		r.mu.Unlock()
		return fmt.Errorf("no working db to save")
	}
	// VACUUM INTO writes a clean copy of the database to the named
	// file. Equivalent in our case to the backup API and supported
	// natively by modernc.org/sqlite.
	_, err := r.db.ExecContext(context.Background(),
		fmt.Sprintf(`VACUUM INTO %s`, sqliteQuote(dst)))
	r.mu.Unlock()
	return err
}

// sqliteQuote returns the SQLite-quoted form of a string literal,
// suitable for use directly in SQL where parameter binding doesn't
// fit (VACUUM INTO requires a literal, not a parameter).
func sqliteQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// cleanupOldRecordings deletes recordings beyond the cfg.Keep newest.
// Recordings with a sibling .preserve sidecar file are skipped: they
// don't count toward the Keep limit and are never deleted by cleanup.
// PreserveCurrentSession is how the daemon flags a recording (the
// sidecar is written by SaveAndRotate when the session's
// preserveReason was non-empty).
func (r *Recorder) cleanupOldRecordings() {
	files, err := os.ReadDir(r.cfg.RecordingsDir)
	if err != nil {
		log.Printf("recorder: read dir: %v", err)
		return
	}
	type entry struct {
		name string
		mod  time.Time
	}
	var dbs []entry
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if !strings.HasSuffix(f.Name(), ".db") {
			continue
		}
		// Skip preserved recordings entirely; they don't count toward
		// the Keep limit and are not eligible for cleanup.
		if _, err := os.Stat(filepath.Join(r.cfg.RecordingsDir, f.Name()+preserveSuffix)); err == nil {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		dbs = append(dbs, entry{name: f.Name(), mod: info.ModTime()})
	}
	if len(dbs) <= r.cfg.Keep {
		return
	}
	// Sort by mod time descending; keep the first cfg.Keep, delete the rest.
	sort.Slice(dbs, func(i, j int) bool { return dbs[i].mod.After(dbs[j].mod) })
	for _, e := range dbs[r.cfg.Keep:] {
		path := filepath.Join(r.cfg.RecordingsDir, e.name)
		if err := os.Remove(path); err != nil {
			log.Printf("recorder: cleanup %s: %v", path, err)
			continue
		}
		log.Printf("recorder: cleaned up %s", path)
	}
}

// preserveSuffix is the filename extension for the sidecar marker file
// that protects a recording from cleanup. <recording>.db preserved by
// <recording>.db.preserve. The sidecar's content is a one-line reason.
const preserveSuffix = ".preserve"

// PreserveCurrentSession marks the active recording so the saved file
// survives the post-disarm cleanup-on-rotate sweep. The reason is
// written to a <recording>.db.preserve sidecar file alongside the
// saved .db, viewable in any file manager and editable / removable
// if the operator wants to release the recording back to normal
// cleanup later.
//
// Safe to call multiple times during a session; the most recent
// reason wins. No effect if no recording is active (preserveReason
// is reset to empty after SaveAndRotate, so a stale flag from a
// previous session can't accidentally preserve a future one).
//
// Typical caller: the recovery package, on ReasonFailsafe trigger.
// Manual recovery triggers do NOT call this -- a flight isn't
// necessarily lost just because the operator wanted to see the
// recovery view.
func (r *Recorder) PreserveCurrentSession(reason string) {
	if r.closed.Load() {
		return
	}
	r.mu.Lock()
	r.preserveReason = reason
	r.mu.Unlock()
	log.Printf("recorder: preserve flag set (reason=%q); sidecar will be written on next SaveAndRotate", reason)
}

// writePreserveSidecar creates the marker file. Errors are logged
// but do not fail the rotation -- a missing sidecar is a graceful
// degradation (the recording is at the front of the queue anyway
// for a while; the operator can manually re-create the sidecar
// before it ages out).
func (r *Recorder) writePreserveSidecar(dbPath, reason string) {
	sidecar := dbPath + preserveSuffix
	content := []byte(reason + "\n")
	if err := os.WriteFile(sidecar, content, 0o644); err != nil {
		log.Printf("recorder: write preserve sidecar %s: %v", sidecar, err)
		return
	}
	log.Printf("recorder: wrote preserve sidecar %s", sidecar)
}

// Recordings returns the saved recordings (newest first) for the
// "Recordings" tab. Reads the directory each time; cheap.
func (r *Recorder) Recordings() ([]Recording, error) {
	files, err := os.ReadDir(r.cfg.RecordingsDir)
	if err != nil {
		return nil, err
	}
	out := make([]Recording, 0, len(files))
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".db") {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		out = append(out, Recording{
			Name:    f.Name(),
			Path:    filepath.Join(r.cfg.RecordingsDir, f.Name()),
			Size:    info.Size(),
			ModTime: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime > out[j].ModTime })
	return out, nil
}

// Recording is a saved-recording summary for the GUI.
type Recording struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// Event is one row from the events table, in the shape callers want
// to see (timestamp as ISO string relative to flight start; detail
// already deserialised when possible). Used by the debug events
// endpoint and (later) by the post-flight narrator.
type Event struct {
	TsMs   int64                  `json:"tsMs"`   // ms since flight start (session begin)
	Kind   string                 `json:"kind"`
	Name   string                 `json:"name"`
	Level  string                 `json:"level"`
	Detail map[string]interface{} `json:"detail,omitempty"`
}

// CurrentSessionEvents returns the events logged so far for the
// current armed session, ordered by timestamp. Returns an empty
// slice when not armed or the working DB hasn't been opened.
//
// The session_id used is whichever is currently active; when not
// armed, this returns events with session_id=0 (pre-arm bookkeeping)
// or empty. For post-flight narration the daemon should call this
// just before OnDisarm rotates the session, so the events are
// captured while still in the working DB.
func (r *Recorder) CurrentSessionEvents() ([]Event, error) {
	if r.closed.Load() {
		return nil, nil
	}
	r.mu.Lock()
	db := r.db
	sessionID := r.sessionID
	r.mu.Unlock()
	if db == nil {
		return nil, nil
	}
	rows, err := db.QueryContext(context.Background(),
		`SELECT ts_us, kind, name, level, detail FROM events WHERE session_id = ? ORDER BY ts_us ASC`,
		sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Event{}
	for rows.Next() {
		var (
			tsUs   int64
			kind   string
			name   string
			level  string
			detail sql.NullString
		)
		if err := rows.Scan(&tsUs, &kind, &name, &level, &detail); err != nil {
			return nil, err
		}
		ev := Event{
			TsMs:  tsUs / 1000,
			Kind:  kind,
			Name:  name,
			Level: level,
		}
		if detail.Valid && detail.String != "" {
			var d map[string]interface{}
			if err := json.Unmarshal([]byte(detail.String), &d); err == nil {
				ev.Detail = d
			}
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Summary is a post-flight summary of a saved recording. Computed on
// demand by opening the SQLite file, running aggregate queries, and
// returning the result. Cheap enough to compute live (single-digit
// milliseconds for typical flights); we don't cache.
type Summary struct {
	Name           string   `json:"name"`
	ModelName      string   `json:"modelName"`
	StartedAt      string   `json:"startedAt"`
	EndedAt        string   `json:"endedAt"`
	DurationSec    int      `json:"durationSec"`
	EventCount     int      `json:"eventCount"`
	TelemetryCount int      `json:"telemetryCount"`

	// Battery
	BatStartV      *float64 `json:"batStartV,omitempty"`
	BatEndV        *float64 `json:"batEndV,omitempty"`
	BatMaxA        *float64 `json:"batMaxA,omitempty"`
	BatUsedMAh     *int     `json:"batUsedMAh,omitempty"`

	// GPS
	GpsMaxAlt      *int     `json:"gpsMaxAlt,omitempty"`
	GpsMaxKmh      *float64 `json:"gpsMaxKmh,omitempty"`

	// Link
	LinkMinRSSI    *int     `json:"linkMinRssi,omitempty"`
	LinkMinLQ      *int     `json:"linkMinLq,omitempty"`

	// Modes seen during the flight (in order of first appearance).
	FlightModes    []string `json:"flightModes,omitempty"`

	// Alarms by level.
	AlarmCounts    map[string]int `json:"alarmCounts,omitempty"`
}

// Summarize opens the saved recording at path read-only and computes
// a Summary. Returns an error if the file is missing or unreadable;
// missing fields in the recording (e.g. no GPS at all) appear as nil
// pointers in the result rather than failing.
func Summarize(path string) (*Summary, error) {
	// Open read-only via DSN flag. modernc.org/sqlite supports
	// query parameters in the DSN.
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer db.Close()

	s := &Summary{
		Name:        filepath.Base(path),
		AlarmCounts: map[string]int{},
	}

	// Session metadata: started_at, ended_at, model_name. Take the
	// first (only, in our model) session row.
	var endedAt sql.NullString
	if err := db.QueryRow(
		`SELECT model_name, started_at, COALESCE(ended_at, '') FROM sessions LIMIT 1`,
	).Scan(&s.ModelName, &s.StartedAt, &endedAt); err != nil {
		// No session row at all — empty file. Return what we have.
		return s, nil
	}
	s.EndedAt = endedAt.String
	if s.StartedAt != "" && s.EndedAt != "" {
		t1, e1 := time.Parse(time.RFC3339Nano, s.StartedAt)
		t2, e2 := time.Parse(time.RFC3339Nano, s.EndedAt)
		if e1 == nil && e2 == nil {
			s.DurationSec = int(t2.Sub(t1).Seconds())
		}
	}

	// Counts. Cheap.
	db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&s.EventCount)
	db.QueryRow(`SELECT COUNT(*) FROM telemetry`).Scan(&s.TelemetryCount)

	// Battery: starting (first non-null), ending (last non-null),
	// peak current (max), used capacity (max, since it's monotonic
	// or near it).
	if v, ok := nullableFloat(db, `SELECT bat_volts FROM telemetry WHERE bat_volts IS NOT NULL ORDER BY ts_us ASC LIMIT 1`); ok {
		s.BatStartV = &v
	}
	if v, ok := nullableFloat(db, `SELECT bat_volts FROM telemetry WHERE bat_volts IS NOT NULL ORDER BY ts_us DESC LIMIT 1`); ok {
		s.BatEndV = &v
	}
	if v, ok := nullableFloat(db, `SELECT MAX(bat_amps) FROM telemetry WHERE bat_amps IS NOT NULL`); ok {
		s.BatMaxA = &v
	}
	if v, ok := nullableInt(db, `SELECT MAX(bat_mah) FROM telemetry WHERE bat_mah IS NOT NULL`); ok {
		s.BatUsedMAh = &v
	}

	// GPS: max altitude, max speed.
	if v, ok := nullableInt(db, `SELECT MAX(gps_alt) FROM telemetry WHERE gps_alt IS NOT NULL`); ok {
		s.GpsMaxAlt = &v
	}
	if v, ok := nullableFloat(db, `SELECT MAX(gps_kmh) FROM telemetry WHERE gps_kmh IS NOT NULL`); ok {
		s.GpsMaxKmh = &v
	}

	// Link: worst RSSI (closest to zero, since negative dBm), worst LQ.
	// CRSF RSSI is negative dBm; "worst" = closest to zero, so MAX.
	// LQ is a percentage; "worst" = lowest value, so MIN.
	if v, ok := nullableInt(db, `SELECT MAX(link_rssi) FROM telemetry WHERE link_rssi IS NOT NULL`); ok {
		s.LinkMinRSSI = &v
	}
	if v, ok := nullableInt(db, `SELECT MIN(link_lq) FROM telemetry WHERE link_lq IS NOT NULL`); ok {
		s.LinkMinLQ = &v
	}

	// Flight modes seen, in order of first appearance.
	if rows, err := db.Query(`
		SELECT fm_mode, MIN(ts_us) AS first_seen
		FROM telemetry
		WHERE fm_mode IS NOT NULL AND fm_mode != ''
		GROUP BY fm_mode
		ORDER BY first_seen ASC
	`); err == nil {
		for rows.Next() {
			var mode string
			var firstSeen int64
			if err := rows.Scan(&mode, &firstSeen); err == nil {
				s.FlightModes = append(s.FlightModes, mode)
			}
		}
		rows.Close()
	}

	// Alarm counts by level. Alarms are events with kind='audio' and
	// level in (warning, critical). We could narrow further but the
	// simplest sensible aggregate is: how many warnings, how many
	// criticals fired during the flight.
	if rows, err := db.Query(`
		SELECT level, COUNT(*) FROM events
		WHERE kind = 'audio' AND level IN ('warning','critical')
		GROUP BY level
	`); err == nil {
		for rows.Next() {
			var level string
			var count int
			if err := rows.Scan(&level, &count); err == nil {
				s.AlarmCounts[level] = count
			}
		}
		rows.Close()
	}

	return s, nil
}

// Detail is the full content of a saved recording, suitable for
// driving the replay UI. Includes session metadata, every event,
// and every telemetry sample. Sizes for a typical 10-minute flight
// at the 5 Hz telemetry throttle: ~3000 telemetry rows + tens of
// events, totaling roughly 150-200 KB JSON. Small enough to ship
// in one response without pagination.
type Detail struct {
	Name      string             `json:"name"`
	Session   SessionMeta        `json:"session"`
	Events    []DetailEvent      `json:"events"`
	Telemetry []DetailTelemetry  `json:"telemetry"`
}

// SessionMeta is the session row's user-facing fields. Times are
// ISO8601 (RFC3339Nano) strings, matching what's stored in SQLite.
type SessionMeta struct {
	ModelName string `json:"modelName,omitempty"`
	ModelPath string `json:"modelPath,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	EndedAt   string `json:"endedAt,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

// DetailEvent is one events row. TsMs is milliseconds from session
// start (matches the resolution the replay clock needs). Detail is
// the raw JSON blob stored in SQLite, passed through verbatim --
// the client parses it.
type DetailEvent struct {
	TsMs   int64  `json:"tsMs"`
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	Level  string `json:"level,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// DetailTelemetry is one telemetry row, in the same units as the
// original TelemetrySample but with optional pointers replaced by
// JSON 'omitempty' so missing values are absent in the wire JSON
// rather than serialized as null. Replay clients iterate these
// rows in ts order and synthesize state messages for the renderer.
type DetailTelemetry struct {
	TsMs int64 `json:"tsMs"`

	BatVolts *float64 `json:"batVolts,omitempty"`
	BatAmps  *float64 `json:"batAmps,omitempty"`
	BatPct   *int     `json:"batPct,omitempty"`
	BatMAh   *int     `json:"batMah,omitempty"`

	GpsLat  *float64 `json:"gpsLat,omitempty"`
	GpsLon  *float64 `json:"gpsLon,omitempty"`
	GpsAlt  *int     `json:"gpsAlt,omitempty"`
	GpsKmh  *float64 `json:"gpsKmh,omitempty"`
	GpsHdg  *float64 `json:"gpsHdg,omitempty"`
	GpsSats *int     `json:"gpsSats,omitempty"`

	LinkRSSI *int `json:"linkRssi,omitempty"`
	LinkLQ   *int `json:"linkLq,omitempty"`
	LinkSNR  *int `json:"linkSnr,omitempty"`

	FlightMode *string `json:"flightMode,omitempty"`

	AttitudeRoll  *float64 `json:"attitudeRoll,omitempty"`
	AttitudePitch *float64 `json:"attitudePitch,omitempty"`
	AttitudeYaw   *float64 `json:"attitudeYaw,omitempty"`
}

// LoadDetail opens the saved recording at path read-only and reads
// the entire session + events + telemetry into a Detail. Used by
// the /api/v1/recordings/detail endpoint for replay playback.
//
// Missing columns in older recordings (notably the attitude_* columns
// added later) are tolerated: the function uses defensive SQL with
// a pragma_table_info introspection to skip absent columns. Pointer
// fields stay nil for those rows; the replay client renders the
// horizon as level when attitude is absent.
//
// All rows are read into memory. For a 10-minute flight this is
// ~150-200 KB; not a concern. For substantially longer recordings
// (an hour+), a streaming approach would be more appropriate -- but
// the recorder caps retained recordings at 10 by default and a
// typical flight is well under 20 minutes.
func LoadDetail(path string) (*Detail, error) {
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer db.Close()

	out := &Detail{Name: filepath.Base(path)}

	// Session metadata. One row; if no session exists the recording
	// is empty (pre-arm save?), return the shell with empty slices.
	var modelName, modelPath, startedAt sql.NullString
	var endedAt, notes sql.NullString
	if err := db.QueryRow(
		`SELECT model_name, model_path, started_at, COALESCE(ended_at, ''), COALESCE(notes, '')
		 FROM sessions LIMIT 1`,
	).Scan(&modelName, &modelPath, &startedAt, &endedAt, &notes); err != nil {
		// No session row: empty file. Caller gets an empty Detail.
		return out, nil
	}
	out.Session = SessionMeta{
		ModelName: modelName.String,
		ModelPath: modelPath.String,
		StartedAt: startedAt.String,
		EndedAt:   endedAt.String,
		Notes:     notes.String,
	}

	// Events. Order by ts so the client can iterate linearly.
	eventRows, err := db.Query(
		`SELECT ts_us, kind, COALESCE(name, ''), COALESCE(level, ''), COALESCE(detail, '')
		 FROM events ORDER BY ts_us ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("events query: %w", err)
	}
	for eventRows.Next() {
		var tsUs int64
		var kind, name, level, detail string
		if err := eventRows.Scan(&tsUs, &kind, &name, &level, &detail); err != nil {
			eventRows.Close()
			return nil, fmt.Errorf("events scan: %w", err)
		}
		out.Events = append(out.Events, DetailEvent{
			TsMs: tsUs / 1000, Kind: kind, Name: name, Level: level, Detail: detail,
		})
	}
	eventRows.Close()

	// Telemetry. Detect attitude column presence for forward-compat
	// with old recordings that pre-date the attitude schema change.
	hasAttitude := tableHasColumn(db, "telemetry", "attitude_roll")

	telemetryQuery := `SELECT ts_us,
		bat_volts, bat_amps, bat_pct, bat_mah,
		gps_lat, gps_lon, gps_alt, gps_kmh, gps_hdg, gps_sats,
		link_rssi, link_lq, link_snr,
		fm_mode`
	if hasAttitude {
		telemetryQuery += `, attitude_roll, attitude_pitch, attitude_yaw`
	}
	telemetryQuery += ` FROM telemetry ORDER BY ts_us ASC`

	telRows, err := db.Query(telemetryQuery)
	if err != nil {
		return nil, fmt.Errorf("telemetry query: %w", err)
	}
	defer telRows.Close()
	for telRows.Next() {
		var tsUs int64
		var bv, ba sql.NullFloat64
		var bp, bm sql.NullInt64
		var glat, glon sql.NullFloat64
		var galt sql.NullInt64
		var gkmh, ghdg sql.NullFloat64
		var gsats sql.NullInt64
		var lrssi, llq, lsnr sql.NullInt64
		var fmMode sql.NullString
		var aRoll, aPitch, aYaw sql.NullFloat64

		var scanErr error
		if hasAttitude {
			scanErr = telRows.Scan(&tsUs,
				&bv, &ba, &bp, &bm,
				&glat, &glon, &galt, &gkmh, &ghdg, &gsats,
				&lrssi, &llq, &lsnr,
				&fmMode,
				&aRoll, &aPitch, &aYaw)
		} else {
			scanErr = telRows.Scan(&tsUs,
				&bv, &ba, &bp, &bm,
				&glat, &glon, &galt, &gkmh, &ghdg, &gsats,
				&lrssi, &llq, &lsnr,
				&fmMode)
		}
		if scanErr != nil {
			return nil, fmt.Errorf("telemetry scan: %w", scanErr)
		}

		row := DetailTelemetry{TsMs: tsUs / 1000}
		if bv.Valid {
			v := bv.Float64; row.BatVolts = &v
		}
		if ba.Valid {
			v := ba.Float64; row.BatAmps = &v
		}
		if bp.Valid {
			v := int(bp.Int64); row.BatPct = &v
		}
		if bm.Valid {
			v := int(bm.Int64); row.BatMAh = &v
		}
		if glat.Valid {
			v := glat.Float64; row.GpsLat = &v
		}
		if glon.Valid {
			v := glon.Float64; row.GpsLon = &v
		}
		if galt.Valid {
			v := int(galt.Int64); row.GpsAlt = &v
		}
		if gkmh.Valid {
			v := gkmh.Float64; row.GpsKmh = &v
		}
		if ghdg.Valid {
			v := ghdg.Float64; row.GpsHdg = &v
		}
		if gsats.Valid {
			v := int(gsats.Int64); row.GpsSats = &v
		}
		if lrssi.Valid {
			v := int(lrssi.Int64); row.LinkRSSI = &v
		}
		if llq.Valid {
			v := int(llq.Int64); row.LinkLQ = &v
		}
		if lsnr.Valid {
			v := int(lsnr.Int64); row.LinkSNR = &v
		}
		if fmMode.Valid {
			v := fmMode.String; row.FlightMode = &v
		}
		if aRoll.Valid {
			v := aRoll.Float64; row.AttitudeRoll = &v
		}
		if aPitch.Valid {
			v := aPitch.Float64; row.AttitudePitch = &v
		}
		if aYaw.Valid {
			v := aYaw.Float64; row.AttitudeYaw = &v
		}
		out.Telemetry = append(out.Telemetry, row)
	}

	return out, nil
}

// tableHasColumn reports whether a column exists on a table. Uses
// SQLite's pragma_table_info which is the canonical way to introspect.
// Returns false on any error (broken file, missing table, etc.) which
// is the safe default for the caller's downgrade path.
func tableHasColumn(db *sql.DB, table, column string) bool {
	rows, err := db.Query(
		`SELECT 1 FROM pragma_table_info(?) WHERE name = ?`, table, column)
	if err != nil {
		return false
	}
	defer rows.Close()
	return rows.Next()
}

// nullableFloat runs a single-column query and returns the float
// value plus ok=true if non-null, or (0, false) on null/error.
func nullableFloat(db *sql.DB, query string) (float64, bool) {
	var v sql.NullFloat64
	if err := db.QueryRow(query).Scan(&v); err != nil {
		return 0, false
	}
	if !v.Valid {
		return 0, false
	}
	return v.Float64, true
}

// nullableInt runs a single-column query and returns the int value
// plus ok=true if non-null, or (0, false) on null/error.
func nullableInt(db *sql.DB, query string) (int, bool) {
	var v sql.NullInt64
	if err := db.QueryRow(query).Scan(&v); err != nil {
		return 0, false
	}
	if !v.Valid {
		return 0, false
	}
	return int(v.Int64), true
}

// Close stops the recorder. Any active session is NOT saved (that's
// intentional: Close is for daemon shutdown, where assuming flight is
// over isn't safe — the operator may have killed the daemon mid-flight
// for any number of reasons; the disarm path is the explicit save
// trigger).
func (r *Recorder) Close() {
	if !r.closed.CompareAndSwap(false, true) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		r.db.Close()
		r.db = nil
	}
}

// sanitiseFilename returns a version of name suitable for use in a
// filename. Replaces non-alphanumerics with hyphens, collapses runs,
// trims, and bounds the length. Empty input becomes "session".
func sanitiseFilename(name string) string {
	if name == "" {
		return "session"
	}
	var b strings.Builder
	prevHyphen := false
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
			prevHyphen = false
		} else if !prevHyphen {
			b.WriteRune('-')
			prevHyphen = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "session"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// === Null recorder ===

// NoOpRecorder satisfies the same contract as Recorder but does
// nothing. Used when recorder.New fails or recording is disabled.
// All methods are no-ops; safe for concurrent use.
type NoOpRecorder struct{}

func (NoOpRecorder) OnArm(string, string)     {}
func (NoOpRecorder) OnDisarm() string         { return "" }
func (NoOpRecorder) LogEvent(string, string, string, interface{}) {}
func (NoOpRecorder) LogTelemetry(TelemetrySample) {}
func (NoOpRecorder) Recordings() ([]Recording, error) { return nil, nil }
func (NoOpRecorder) CurrentSessionEvents() ([]Event, error) { return nil, nil }
func (NoOpRecorder) PreserveCurrentSession(string) {}
func (NoOpRecorder) Close()                   {}

// Interface is the surface used by the daemon. Recorder and NoOpRecorder
// both satisfy it; the daemon holds an Interface variable so swapping
// in NoOpRecorder on construction failure is transparent.
type Interface interface {
	OnArm(modelName, modelPath string)
	OnDisarm() string
	LogEvent(kind, name, level string, detail interface{})
	LogTelemetry(TelemetrySample)
	Recordings() ([]Recording, error)
	CurrentSessionEvents() ([]Event, error)
	// PreserveCurrentSession marks the active recording so the
	// saved file survives the post-disarm cleanup sweep. Mechanism:
	// a <recording>.db.preserve sidecar file written at SaveAndRotate.
	PreserveCurrentSession(reason string)
	Close()
}

// Compile-time interface checks.
var _ Interface = (*Recorder)(nil)
var _ Interface = NoOpRecorder{}
