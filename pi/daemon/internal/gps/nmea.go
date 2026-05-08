// Package gps reads NMEA from a serial GPS module and exposes the
// most recent fix as a thread-safe State snapshot.
//
// The parser handles GGA and RMC sentences only, which between them
// carry everything ZeroTX needs: fix presence, satellite count, HDOP,
// position, altitude, ground speed, true heading, and UTC date/time.
// Other talkers (GSA, GSV, VTG, GLL, ...) are accepted and silently
// ignored. Multi-constellation prefixes (GP, GN, GL, GA, GB) all parse
// the same way; the talker ID is recorded but otherwise unused.
//
// Read failures and parse failures are non-fatal. The reader logs
// each problem at most once per minute and keeps reading; the State
// just stops advancing until a clean sentence arrives. This keeps
// the daemon resilient to garbled bytes from a flaky cable, brown-out
// resets on the GPS module, and intermittent characters when the
// kernel UART buffer overflows.
package gps

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Sentence is a parsed NMEA line, post-checksum-verification.
type Sentence struct {
	Talker string   // "GP", "GN", "GL", ...
	MsgID  string   // "GGA", "RMC", ...
	Fields []string // comma-separated payload, no $, no checksum
}

// Common parser errors. Callers typically don't inspect these; the
// reader counts them and continues.
var (
	errBadFormat   = errors.New("nmea: malformed sentence")
	errBadChecksum = errors.New("nmea: checksum mismatch")
)

// ParseSentence parses one NMEA line. Leading and trailing whitespace
// is trimmed. The line must begin with '$' and contain '*' followed by
// two hex chars (the XOR checksum of everything between $ and *).
func ParseSentence(line string) (*Sentence, error) {
	line = strings.TrimSpace(line)
	if len(line) < 8 || line[0] != '$' {
		return nil, errBadFormat
	}
	star := strings.LastIndexByte(line, '*')
	if star < 1 || star+3 > len(line) {
		return nil, errBadFormat
	}
	body := line[1:star]
	want, err := strconv.ParseUint(line[star+1:star+3], 16, 8)
	if err != nil {
		return nil, errBadFormat
	}
	var got byte
	for i := 0; i < len(body); i++ {
		got ^= body[i]
	}
	if got != byte(want) {
		return nil, errBadChecksum
	}
	parts := strings.Split(body, ",")
	if len(parts) < 1 || len(parts[0]) < 5 {
		return nil, errBadFormat
	}
	head := parts[0]
	return &Sentence{
		Talker: head[:2],
		MsgID:  head[2:],
		Fields: parts[1:],
	}, nil
}

// applyToState updates s in place from the parsed Sentence and returns
// true if any state field changed. Unknown sentences are accepted but
// produce no change.
func applyToState(s *State, sent *Sentence, now time.Time) bool {
	switch sent.MsgID {
	case "GGA":
		return applyGGA(s, sent.Fields, now)
	case "RMC":
		return applyRMC(s, sent.Fields, now)
	}
	return false
}

// applyGGA updates fix quality, sats, HDOP, lat/lon, and altitude from
// a $..GGA sentence. Field layout:
//
//	0: UTC time (hhmmss.sss)
//	1: latitude (ddmm.mmmm)
//	2: N/S
//	3: longitude (dddmm.mmmm)
//	4: E/W
//	5: fix quality (0=none, 1=GPS, 2=DGPS, 4/5=RTK, ...)
//	6: satellites in use
//	7: HDOP
//	8: altitude (meters above mean sea level)
//	9: 'M'
//	10: geoid height
//	11: 'M'
//	12: DGPS age (seconds)
//	13: DGPS station ID
//
// Empty fields leave the corresponding state untouched.
func applyGGA(s *State, f []string, now time.Time) bool {
	if len(f) < 9 {
		return false
	}
	changed := false
	if q, ok := parseInt(f[5]); ok {
		newFix := FixNone
		if q > 0 {
			newFix = Fix3D // GGA can't tell 2D from 3D; sats heuristic below downgrades
		}
		if sats, ok := parseInt(f[6]); ok && sats >= 0 {
			s.Sats = sats
			if newFix != FixNone && sats < 4 {
				newFix = Fix2D
			}
			changed = true
		}
		if s.Fix != newFix {
			s.Fix = newFix
			changed = true
		}
	}
	if hdop, ok := parseFloat(f[7]); ok {
		s.HDOP = hdop
		changed = true
	}
	if lat, ok := parseLatLon(f[1], f[2], false); ok {
		s.LatDeg = lat
		changed = true
	}
	if lon, ok := parseLatLon(f[3], f[4], true); ok {
		s.LonDeg = lon
		changed = true
	}
	if alt, ok := parseFloat(f[8]); ok {
		s.AltMeters = alt
		changed = true
	}
	if changed {
		s.Updated = now
	}
	return changed
}

// applyRMC updates time, lat/lon, ground speed, heading, and validity
// flag from a $..RMC sentence. Field layout:
//
//	0: UTC time (hhmmss.sss)
//	1: status (A=active, V=void)
//	2: latitude (ddmm.mmmm)
//	3: N/S
//	4: longitude (dddmm.mmmm)
//	5: E/W
//	6: speed over ground (knots)
//	7: course over ground (degrees true)
//	8: date (ddmmyy)
//	9: magnetic variation
//	10: variation E/W
//	11: mode indicator (NMEA 2.3+)
//
// Empty fields leave the corresponding state untouched. Date+time
// together produce s.Time only when both are present.
func applyRMC(s *State, f []string, now time.Time) bool {
	if len(f) < 9 {
		return false
	}
	changed := false
	if lat, ok := parseLatLon(f[2], f[3], false); ok {
		s.LatDeg = lat
		changed = true
	}
	if lon, ok := parseLatLon(f[4], f[5], true); ok {
		s.LonDeg = lon
		changed = true
	}
	if knots, ok := parseFloat(f[6]); ok {
		s.SpeedKmh = knots * 1.852
		changed = true
	}
	if hdg, ok := parseFloat(f[7]); ok {
		s.HeadingDeg = hdg
		changed = true
	}
	if t, ok := parseRMCTime(f[8], f[0]); ok {
		s.Time = t
		changed = true
	}
	if changed {
		s.Updated = now
	}
	return changed
}

// parseLatLon converts ddmm.mmmm + N/S/E/W (or dddmm.mmmm for lon)
// into signed decimal degrees. Empty inputs return ok=false.
func parseLatLon(coord, hemi string, isLon bool) (float64, bool) {
	if coord == "" || hemi == "" {
		return 0, false
	}
	dot := strings.IndexByte(coord, '.')
	if dot < 0 {
		dot = len(coord)
	}
	// Latitude: 2 degree digits; longitude: 3.
	degDigits := 2
	if isLon {
		degDigits = 3
	}
	if dot < degDigits+2 {
		return 0, false
	}
	deg, err := strconv.ParseFloat(coord[:degDigits], 64)
	if err != nil {
		return 0, false
	}
	min, err := strconv.ParseFloat(coord[degDigits:], 64)
	if err != nil {
		return 0, false
	}
	val := deg + min/60.0
	switch hemi {
	case "S", "W":
		val = -val
	case "N", "E":
		// keep sign
	default:
		return 0, false
	}
	return val, true
}

// parseRMCTime combines RMC's date (ddmmyy) and time (hhmmss[.sss])
// fields into a UTC time.Time. Returns ok=false if either is empty.
func parseRMCTime(date, t string) (time.Time, bool) {
	if len(date) < 6 || len(t) < 6 {
		return time.Time{}, false
	}
	day, err1 := strconv.Atoi(date[0:2])
	mon, err2 := strconv.Atoi(date[2:4])
	yr, err3 := strconv.Atoi(date[4:6])
	hr, err4 := strconv.Atoi(t[0:2])
	mi, err5 := strconv.Atoi(t[2:4])
	sec, err6 := strconv.Atoi(t[4:6])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || err6 != nil {
		return time.Time{}, false
	}
	// NMEA two-digit year: 00..79 -> 2000..2079, 80..99 -> 1980..1999
	// (matches the convention used by gpsd and most consumer GPS modules).
	full := 2000 + yr
	if yr >= 80 {
		full = 1900 + yr
	}
	// Sub-second component, if present after the dot.
	nsec := 0
	if dot := strings.IndexByte(t, '.'); dot >= 0 && len(t) > dot+1 {
		frac := t[dot+1:]
		if len(frac) > 9 {
			frac = frac[:9]
		}
		// Pad to nanoseconds (frac is e.g. "5" -> 0.5s -> 500000000ns).
		fracVal, err := strconv.Atoi(frac)
		if err == nil {
			pad := 9 - len(frac)
			for i := 0; i < pad; i++ {
				fracVal *= 10
			}
			nsec = fracVal
		}
	}
	return time.Date(full, time.Month(mon), day, hr, mi, sec, nsec, time.UTC), true
}

// parseFloat returns 0,false on empty input. Avoids reporting empty
// fields as 0 which would clobber prior valid state.
func parseFloat(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseInt returns 0,false on empty input.
func parseInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return v, true
}

// dump renders a State as a single log-friendly line.
func (s State) dump() string {
	if s.Fix == FixNone {
		return "no fix"
	}
	d := "3D"
	if s.Fix == Fix2D {
		d = "2D"
	}
	return fmt.Sprintf("%s fix, sats=%d HDOP=%.1f lat=%.6f lon=%.6f alt=%.1fm",
		d, s.Sats, s.HDOP, s.LatDeg, s.LonDeg, s.AltMeters)
}
