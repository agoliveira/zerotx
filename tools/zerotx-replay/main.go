// Command zerotx-replay summarises a flight recording.
//
// Reads a SQLite recording produced by the daemon's recorder package
// (typically at ~/zerotx/recordings/*.db) and prints a flight summary:
// session metadata, telemetry stats, the chronological event log, and
// any audio/alarm-class events grouped together.
//
// MVP scope is intentionally narrow: this tool reads what's in the
// recording and formats it. It does NOT re-evaluate any analysis (no
// re-running narrator, no re-running alert rules). The recordings
// already capture what the daemon decided at the time; this tool
// makes that legible.
//
// Lives as a separate Go module so it can build standalone on a
// desktop or server (cartman, stan) without pulling the daemon's
// hardware-specific dependencies (SDL2, serial, etc).
//
// Usage:
//
//	zerotx-replay path/to/flight.db
//	zerotx-replay -json path/to/flight.db
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	jsonOut := flag.Bool("json", false, "emit JSON instead of human-readable text")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [-json] <recording.db>\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	path := flag.Arg(0)

	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(os.Stderr, "zerotx-replay: %v\n", err)
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "zerotx-replay: open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	summary, err := summarise(db, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "zerotx-replay: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(summary); err != nil {
			fmt.Fprintf(os.Stderr, "zerotx-replay: encode: %v\n", err)
			os.Exit(1)
		}
	} else {
		printText(os.Stdout, summary)
	}
}

// Summary is the structured shape of the flight report. JSON tags
// drive the -json output; text rendering is in printText.
type Summary struct {
	File     string         `json:"file"`
	Session  SessionInfo    `json:"session"`
	Duration time.Duration  `json:"duration"`
	Stats    TelemetryStats `json:"telemetryStats"`
	Modes    []ModeInterval `json:"modes"`
	Events   []EventRow     `json:"events"`
	Alerts   []EventRow     `json:"alerts"`
}

type SessionInfo struct {
	ID        int64     `json:"id"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	Model     string    `json:"model,omitempty"`
	ModelPath string    `json:"modelPath,omitempty"`
	Notes     string    `json:"notes,omitempty"`
}

// TelemetryStats summarises numeric telemetry over the session.
// Pointer types so "no data" is distinguishable from zero - matches
// the recorder's TelemetrySample convention.
type TelemetryStats struct {
	BatVoltsStart *float64 `json:"batVoltsStart,omitempty"`
	BatVoltsEnd   *float64 `json:"batVoltsEnd,omitempty"`
	BatVoltsMin   *float64 `json:"batVoltsMin,omitempty"`
	BatVoltsMax   *float64 `json:"batVoltsMax,omitempty"`
	BatAmpsPeak   *float64 `json:"batAmpsPeak,omitempty"`
	BatMAhEnd     *int     `json:"batMAhEnd,omitempty"`
	BatPctEnd     *int     `json:"batPctEnd,omitempty"`

	GpsAltMaxM    *int     `json:"gpsAltMaxM,omitempty"`
	GpsKmhMax     *float64 `json:"gpsKmhMax,omitempty"`
	GpsSatsMax    *int     `json:"gpsSatsMax,omitempty"`
	GpsLatStart   *float64 `json:"gpsLatStart,omitempty"`
	GpsLonStart   *float64 `json:"gpsLonStart,omitempty"`
	GpsLatEnd     *float64 `json:"gpsLatEnd,omitempty"`
	GpsLonEnd     *float64 `json:"gpsLonEnd,omitempty"`

	LinkLQMin     *int     `json:"linkLqMin,omitempty"`
	LinkRssiMax   *int     `json:"linkRssiMax,omitempty"`

	SampleCount   int      `json:"sampleCount"`
}

// ModeInterval describes a span the aircraft spent in a given flight
// mode. Computed from consecutive telemetry samples whose fm_mode
// differs.
type ModeInterval struct {
	Mode     string        `json:"mode"`
	Start    time.Duration `json:"start"`    // seconds since session start
	Duration time.Duration `json:"duration"`
}

// EventRow mirrors one row in the events table.
type EventRow struct {
	Offset time.Duration `json:"offset"` // duration since session start
	Kind   string        `json:"kind"`
	Name   string        `json:"name,omitempty"`
	Level  string        `json:"level,omitempty"`
	Detail interface{}   `json:"detail,omitempty"`
}
