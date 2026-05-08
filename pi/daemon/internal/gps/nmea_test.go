package gps

import (
	"math"
	"testing"
	"time"
)

// floatNear returns true if a and b are within tol of each other.
func floatNear(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

// TestParseSentence_Valid covers a clean GGA from a u-blox module.
// Real-world capture; the checksum is correct.
func TestParseSentence_Valid(t *testing.T) {
	line := "$GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,*47"
	s, err := ParseSentence(line)
	if err != nil {
		t.Fatalf("ParseSentence: %v", err)
	}
	if s.Talker != "GP" || s.MsgID != "GGA" {
		t.Errorf("talker=%q msg=%q, want GP/GGA", s.Talker, s.MsgID)
	}
	if len(s.Fields) != 14 {
		t.Errorf("fields=%d, want 14: %v", len(s.Fields), s.Fields)
	}
}

// TestParseSentence_GNTalker confirms multi-constellation prefixes
// parse the same as GP.
func TestParseSentence_GNTalker(t *testing.T) {
	line := withChecksum("GNRMC,113132.00,A,2255.0000,S,04706.0000,W,0.5,123.4,080525,,,A")
	s, err := ParseSentence(line)
	if err != nil {
		t.Fatalf("ParseSentence: %v", err)
	}
	if s.Talker != "GN" || s.MsgID != "RMC" {
		t.Errorf("talker=%q msg=%q, want GN/RMC", s.Talker, s.MsgID)
	}
}

// TestParseSentence_BadChecksum detects altered payload.
func TestParseSentence_BadChecksum(t *testing.T) {
	// Same as the valid GGA but with one digit changed in the lat,
	// which means the original checksum no longer matches.
	line := "$GPGGA,123519,4807.039,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,*47"
	if _, err := ParseSentence(line); err == nil {
		t.Error("expected checksum error")
	}
}

// TestParseSentence_Malformed catches obvious garbage.
func TestParseSentence_Malformed(t *testing.T) {
	cases := []string{
		"",
		"GPGGA,1,2*47", // missing $
		"$short",       // too short
		"$GPGGA,1,2",   // no checksum
		"$GPGGA,1,2*Z9",
	}
	for _, in := range cases {
		if _, err := ParseSentence(in); err == nil {
			t.Errorf("ParseSentence(%q) succeeded, expected error", in)
		}
	}
}

// TestApplyGGA_Basic confirms field extraction matches a known fix.
// The location is the example from the NMEA Wikipedia article (a hill
// near Munich); the fields are real, only the timestamp is arbitrary.
func TestApplyGGA_Basic(t *testing.T) {
	sent, err := ParseSentence("$GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,*47")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2025, 5, 8, 12, 0, 0, 0, time.UTC)
	var s State
	changed := applyToState(&s, sent, now)
	if !changed {
		t.Fatal("applyGGA did not mark state changed")
	}
	if s.Fix != Fix3D {
		t.Errorf("Fix=%v, want Fix3D", s.Fix)
	}
	if s.Sats != 8 {
		t.Errorf("Sats=%d, want 8", s.Sats)
	}
	if !floatNear(s.HDOP, 0.9, 0.001) {
		t.Errorf("HDOP=%f, want 0.9", s.HDOP)
	}
	// 4807.038 N -> 48 + 7.038/60 = 48.1173
	if !floatNear(s.LatDeg, 48.1173, 0.0001) {
		t.Errorf("LatDeg=%f, want ~48.1173", s.LatDeg)
	}
	// 01131.000 E -> 11 + 31.000/60 = 11.51667
	if !floatNear(s.LonDeg, 11.51667, 0.0001) {
		t.Errorf("LonDeg=%f, want ~11.51667", s.LonDeg)
	}
	if !floatNear(s.AltMeters, 545.4, 0.001) {
		t.Errorf("AltMeters=%f, want 545.4", s.AltMeters)
	}
	if !s.Updated.Equal(now) {
		t.Errorf("Updated=%v, want %v", s.Updated, now)
	}
}

// TestApplyGGA_NoFix records a 0 fix-quality field.
func TestApplyGGA_NoFix(t *testing.T) {
	sent, err := ParseSentence(withChecksum("GPGGA,123519,,,,,0,00,99.9,,M,,M,,"))
	if err != nil {
		t.Fatal(err)
	}
	var s State
	applyToState(&s, sent, time.Now())
	if s.Fix != FixNone {
		t.Errorf("Fix=%v, want FixNone", s.Fix)
	}
	if s.Sats != 0 {
		t.Errorf("Sats=%d, want 0", s.Sats)
	}
}

// TestApplyGGA_2DFix triggers the sats<4 downgrade path.
func TestApplyGGA_2DFix(t *testing.T) {
	sent, err := ParseSentence(withChecksum("GPGGA,123519,4807.038,N,01131.000,E,1,03,2.5,100.0,M,46.9,M,,"))
	if err != nil {
		t.Fatalf("ParseSentence: %v", err)
	}
	var s State
	applyToState(&s, sent, time.Now())
	if s.Fix != Fix2D {
		t.Errorf("Fix=%v, want Fix2D (sats=3)", s.Fix)
	}
	if s.Sats != 3 {
		t.Errorf("Sats=%d, want 3", s.Sats)
	}
}

// TestApplyRMC_Basic confirms RMC parsing including knots->kmh
// conversion and date+time merging.
func TestApplyRMC_Basic(t *testing.T) {
	// 100 knots = 185.2 km/h. Heading 084.4 degrees true.
	sent, err := ParseSentence(withChecksum("GPRMC,225446,A,4916.45,N,12311.12,W,100.0,084.4,191194,003.1,W"))
	if err != nil {
		t.Fatalf("ParseSentence: %v", err)
	}
	var s State
	applyToState(&s, sent, time.Now())
	if !floatNear(s.SpeedKmh, 185.2, 0.001) {
		t.Errorf("SpeedKmh=%f, want 185.2", s.SpeedKmh)
	}
	if !floatNear(s.HeadingDeg, 84.4, 0.01) {
		t.Errorf("HeadingDeg=%f, want 84.4", s.HeadingDeg)
	}
	// 4916.45 N -> 49.27417
	if !floatNear(s.LatDeg, 49.27417, 0.0001) {
		t.Errorf("LatDeg=%f, want ~49.27417", s.LatDeg)
	}
	// 12311.12 W -> -123.18533
	if !floatNear(s.LonDeg, -123.18533, 0.0001) {
		t.Errorf("LonDeg=%f, want ~-123.18533", s.LonDeg)
	}
	want := time.Date(1994, 11, 19, 22, 54, 46, 0, time.UTC)
	if !s.Time.Equal(want) {
		t.Errorf("Time=%v, want %v", s.Time, want)
	}
}

// TestApplyRMC_TwoDigitYear2000s confirms 00..79 maps into the 21st century.
func TestApplyRMC_TwoDigitYear2000s(t *testing.T) {
	sent, err := ParseSentence(withChecksum("GPRMC,123000,A,4916.45,N,12311.12,W,000.0,000.0,080525,,,A"))
	if err != nil {
		t.Fatalf("ParseSentence: %v", err)
	}
	var s State
	applyToState(&s, sent, time.Now())
	if s.Time.Year() != 2025 {
		t.Errorf("Year=%d, want 2025", s.Time.Year())
	}
}

// TestParseLatLon_EmptyFieldsIgnored confirms missing fields don't
// silently overwrite prior valid state with zeros.
func TestParseLatLon_EmptyFieldsIgnored(t *testing.T) {
	if _, ok := parseLatLon("", "N", false); ok {
		t.Error("empty coord parsed as ok")
	}
	if _, ok := parseLatLon("4807.038", "", false); ok {
		t.Error("empty hemi parsed as ok")
	}
}

// TestUnknownSentenceIgnored covers sentences we don't parse but
// don't reject either (GSA, GSV, VTG, ...).
func TestUnknownSentenceIgnored(t *testing.T) {
	sent, err := ParseSentence(withChecksum("GPGSA,A,3,29,07,16,30,26,21,05,18,,,,,1.7,0.9,1.5"))
	if err != nil {
		t.Fatalf("ParseSentence: %v", err)
	}
	var s State
	if applyToState(&s, sent, time.Now()) {
		t.Error("unknown sentence reported as state change")
	}
}

// withChecksum wraps a sentence body (everything between the leading
// '$' and the trailing '*XX') with the correct NMEA XOR checksum.
// All test sentences go through this so the tests are correct by
// construction; hand-typed checksums are too easy to get wrong.
func withChecksum(body string) string {
	var ck byte
	for i := 0; i < len(body); i++ {
		ck ^= body[i]
	}
	return "$" + body + "*" + hexByte(ck)
}

// hexByte renders b as two uppercase hex digits.
func hexByte(b byte) string {
	const hex = "0123456789ABCDEF"
	return string([]byte{hex[b>>4], hex[b&0xf]})
}
