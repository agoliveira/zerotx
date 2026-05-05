package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// summarise reads everything we need from the recording and returns
// a populated Summary. The DB layout is fixed by the recorder
// package; we don't introspect schema or version-tag.
func summarise(db *sql.DB, path string) (Summary, error) {
	var s Summary
	s.File = filepath.Base(path)

	// Sessions table holds zero or more sessions. The recorder writes
	// one session per arm/disarm cycle; multiple sessions in one file
	// means the DB was reused (currently the recorder rotates files
	// per session so this is rare). We pick the latest session by
	// started_at as a defensive default.
	var startedAtStr, endedAtStr sql.NullString
	var notes sql.NullString
	var modelName, modelPath sql.NullString
	row := db.QueryRow(`
		SELECT id, started_at, ended_at, model_name, model_path, notes
		FROM sessions
		ORDER BY started_at DESC
		LIMIT 1
	`)
	if err := row.Scan(&s.Session.ID, &startedAtStr, &endedAtStr,
		&modelName, &modelPath, &notes); err != nil {
		if err == sql.ErrNoRows {
			return s, fmt.Errorf("no sessions in recording")
		}
		return s, fmt.Errorf("read session: %w", err)
	}
	if t, ok := parseTimestamp(startedAtStr.String); ok {
		s.Session.StartedAt = t
	}
	if t, ok := parseTimestamp(endedAtStr.String); ok {
		s.Session.EndedAt = t
	}
	s.Session.Model = modelName.String
	s.Session.ModelPath = modelPath.String
	s.Session.Notes = notes.String

	if !s.Session.StartedAt.IsZero() && !s.Session.EndedAt.IsZero() {
		s.Duration = s.Session.EndedAt.Sub(s.Session.StartedAt)
	}

	stats, modes, err := readTelemetry(db, s.Session.ID)
	if err != nil {
		return s, fmt.Errorf("read telemetry: %w", err)
	}
	s.Stats = stats
	s.Modes = modes

	events, err := readEvents(db, s.Session.ID)
	if err != nil {
		return s, fmt.Errorf("read events: %w", err)
	}
	s.Events = events

	// Alerts subset: kind=alarm OR (kind=audio AND level in
	// {warning, critical}). The audio "alerts" are the things the
	// operator actually heard called out during flight.
	for _, e := range events {
		if e.Kind == "alarm" {
			s.Alerts = append(s.Alerts, e)
			continue
		}
		if e.Kind == "audio" && (e.Level == "warning" || e.Level == "critical") {
			s.Alerts = append(s.Alerts, e)
		}
	}

	return s, nil
}

// parseTimestamp accepts the recorder's ISO-8601 string format. Empty
// strings yield (zero, false).
func parseTimestamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02T15:04:05.999999999Z",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// readTelemetry walks the telemetry table for the session and builds
// summary stats plus mode-interval reconstruction. Single pass; the
// table is indexed by (session_id, ts_us) so iteration is ordered.
func readTelemetry(db *sql.DB, sessionID int64) (TelemetryStats, []ModeInterval, error) {
	rows, err := db.Query(`
		SELECT ts_us,
		       bat_volts, bat_amps, bat_pct, bat_mah,
		       gps_lat, gps_lon, gps_alt, gps_kmh, gps_sats,
		       link_rssi, link_lq,
		       fm_mode
		FROM telemetry
		WHERE session_id = ?
		ORDER BY ts_us ASC
	`, sessionID)
	if err != nil {
		return TelemetryStats{}, nil, err
	}
	defer rows.Close()

	var stats TelemetryStats
	var modes []ModeInterval
	var curMode string
	var curModeStart time.Duration
	var lastOffset time.Duration

	for rows.Next() {
		var tsUs int64
		var batV, batA, gpsLat, gpsLon, gpsKmh sql.NullFloat64
		var batPct, batMAh, gpsAlt, gpsSats, linkRSSI, linkLQ sql.NullInt64
		var fmMode sql.NullString
		if err := rows.Scan(&tsUs,
			&batV, &batA, &batPct, &batMAh,
			&gpsLat, &gpsLon, &gpsAlt, &gpsKmh, &gpsSats,
			&linkRSSI, &linkLQ,
			&fmMode); err != nil {
			return stats, nil, err
		}

		offset := time.Duration(tsUs) * time.Microsecond
		stats.SampleCount++

		// Battery voltage: track first/last/min/max.
		if batV.Valid {
			v := batV.Float64
			if stats.BatVoltsStart == nil {
				stats.BatVoltsStart = &v
			}
			vCopy := v
			stats.BatVoltsEnd = &vCopy
			if stats.BatVoltsMin == nil || v < *stats.BatVoltsMin {
				vCopy2 := v
				stats.BatVoltsMin = &vCopy2
			}
			if stats.BatVoltsMax == nil || v > *stats.BatVoltsMax {
				vCopy3 := v
				stats.BatVoltsMax = &vCopy3
			}
		}
		if batA.Valid {
			a := batA.Float64
			if stats.BatAmpsPeak == nil || a > *stats.BatAmpsPeak {
				stats.BatAmpsPeak = &a
			}
		}
		if batMAh.Valid {
			n := int(batMAh.Int64)
			stats.BatMAhEnd = &n
		}
		if batPct.Valid {
			n := int(batPct.Int64)
			stats.BatPctEnd = &n
		}

		// GPS: track max altitude/speed/sats; remember first and
		// last fix coordinates.
		if gpsLat.Valid && gpsLon.Valid {
			lat := gpsLat.Float64
			lon := gpsLon.Float64
			if stats.GpsLatStart == nil {
				stats.GpsLatStart = &lat
				stats.GpsLonStart = &lon
			}
			latCopy, lonCopy := lat, lon
			stats.GpsLatEnd = &latCopy
			stats.GpsLonEnd = &lonCopy
		}
		if gpsAlt.Valid {
			alt := int(gpsAlt.Int64)
			if stats.GpsAltMaxM == nil || alt > *stats.GpsAltMaxM {
				stats.GpsAltMaxM = &alt
			}
		}
		if gpsKmh.Valid {
			kmh := gpsKmh.Float64
			if stats.GpsKmhMax == nil || kmh > *stats.GpsKmhMax {
				stats.GpsKmhMax = &kmh
			}
		}
		if gpsSats.Valid {
			sats := int(gpsSats.Int64)
			if stats.GpsSatsMax == nil || sats > *stats.GpsSatsMax {
				stats.GpsSatsMax = &sats
			}
		}

		// Link: lowest LQ and best RSSI ever seen.
		if linkLQ.Valid {
			lq := int(linkLQ.Int64)
			if stats.LinkLQMin == nil || lq < *stats.LinkLQMin {
				stats.LinkLQMin = &lq
			}
		}
		if linkRSSI.Valid {
			rssi := int(linkRSSI.Int64)
			if stats.LinkRssiMax == nil || rssi > *stats.LinkRssiMax {
				stats.LinkRssiMax = &rssi
			}
		}

		// Mode tracking: detect transitions, accumulate intervals.
		if fmMode.Valid && fmMode.String != "" {
			if fmMode.String != curMode {
				if curMode != "" {
					modes = append(modes, ModeInterval{
						Mode:     curMode,
						Start:    curModeStart,
						Duration: offset - curModeStart,
					})
				}
				curMode = fmMode.String
				curModeStart = offset
			}
		}
		lastOffset = offset
	}
	if err := rows.Err(); err != nil {
		return stats, nil, err
	}

	// Close the final mode interval if any.
	if curMode != "" {
		modes = append(modes, ModeInterval{
			Mode:     curMode,
			Start:    curModeStart,
			Duration: lastOffset - curModeStart,
		})
	}

	return stats, modes, nil
}

// readEvents loads all event rows for the session in chronological
// order. Detail is JSON in the recorder; we unmarshal opaquely so the
// tool works regardless of which detail shapes the daemon emitted.
func readEvents(db *sql.DB, sessionID int64) ([]EventRow, error) {
	rows, err := db.Query(`
		SELECT ts_us, kind, name, level, detail
		FROM events
		WHERE session_id = ?
		ORDER BY ts_us ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EventRow
	for rows.Next() {
		var tsUs int64
		var kind, name, level, detail sql.NullString
		if err := rows.Scan(&tsUs, &kind, &name, &level, &detail); err != nil {
			return nil, err
		}
		ev := EventRow{
			Offset: time.Duration(tsUs) * time.Microsecond,
			Kind:   kind.String,
			Name:   name.String,
			Level:  level.String,
		}
		if detail.Valid && detail.String != "" {
			var parsed interface{}
			if err := json.Unmarshal([]byte(detail.String), &parsed); err == nil {
				ev.Detail = parsed
			} else {
				ev.Detail = detail.String
			}
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Sort defensively (events index is by ts_us already, but JSON
	// may have come in unordered if a future schema iteration
	// removes the index).
	sort.SliceStable(out, func(i, j int) bool { return out[i].Offset < out[j].Offset })
	return out, nil
}
