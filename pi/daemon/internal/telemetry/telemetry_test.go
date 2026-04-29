package telemetry

import (
	"encoding/binary"
	"testing"
	"time"
)

// helpers to build CRSF telemetry frame payloads as the daemon receives
// them from the MCU: [crsf_addr:1][crsf_type:1][body:N].

func wrap(addr, frameType byte, body []byte) []byte {
	out := make([]byte, 2+len(body))
	out[0] = addr
	out[1] = frameType
	copy(out[2:], body)
	return out
}

func TestParseGPS_RoundTrip(t *testing.T) {
	// Build a known-good GPS frame body.
	body := make([]byte, 15)
	var lat int32 = 514321000   // 51.4321 * 1e7
	var lon int32 = -12345670   // -1.234567 * 1e7
	binary.BigEndian.PutUint32(body[0:4], uint32(lat))
	binary.BigEndian.PutUint32(body[4:8], uint32(lon))
	binary.BigEndian.PutUint16(body[8:10], 1234)                     // 123.4 km/h
	binary.BigEndian.PutUint16(body[10:12], 17500)                   // 175.00 deg
	binary.BigEndian.PutUint16(body[12:14], 1450)                    // alt = 450m (1450 - 1000)
	body[14] = 12

	g, ok := parseGPS(body)
	if !ok {
		t.Fatal("parseGPS failed on well-formed input")
	}
	if g.Sats != 12 {
		t.Errorf("sats: got %d, want 12", g.Sats)
	}
	if g.AltMeters != 450 {
		t.Errorf("alt: got %d, want 450", g.AltMeters)
	}
	if g.GroundKmh < 123.3 || g.GroundKmh > 123.5 {
		t.Errorf("groundKmh: got %f, want ~123.4", g.GroundKmh)
	}
	if g.HeadingDeg < 174.99 || g.HeadingDeg > 175.01 {
		t.Errorf("heading: got %f, want 175.0", g.HeadingDeg)
	}
	if g.LatDeg < 51.43 || g.LatDeg > 51.44 {
		t.Errorf("lat: got %f, want ~51.4321", g.LatDeg)
	}
	if g.LonDeg > -1.23 || g.LonDeg < -1.24 {
		t.Errorf("lon: got %f, want ~-1.234567", g.LonDeg)
	}
}

func TestParseGPS_ShortBuffer(t *testing.T) {
	if _, ok := parseGPS(make([]byte, 14)); ok {
		t.Errorf("parseGPS should reject 14-byte payload")
	}
}

func TestParseBattery_RoundTrip(t *testing.T) {
	body := make([]byte, 8)
	binary.BigEndian.PutUint16(body[0:2], 168) // 16.8V (4S full)
	binary.BigEndian.PutUint16(body[2:4], 25)  // 2.5A
	body[4] = 0x00
	body[5] = 0x05
	body[6] = 0xDC // 24-bit big-endian: 0x0005DC = 1500 mAh
	body[7] = 87   // 87%

	b, ok := parseBattery(body)
	if !ok {
		t.Fatal("parseBattery failed on well-formed input")
	}
	if b.Volts < 16.7 || b.Volts > 16.9 {
		t.Errorf("volts: got %f, want 16.8", b.Volts)
	}
	if b.Amps != 2.5 {
		t.Errorf("amps: got %f, want 2.5", b.Amps)
	}
	if b.UsedMAh != 1500 {
		t.Errorf("mAh: got %d, want 1500", b.UsedMAh)
	}
	if b.Percent != 87 {
		t.Errorf("percent: got %d, want 87", b.Percent)
	}
}

func TestParseLink_AntennaSelection(t *testing.T) {
	// active_antenna = 0 → use ant1 RSSI (b[0])
	body := []byte{
		70, 80, // up RSSI ant1, ant2
		95,        // up LQ
		8,         // up SNR
		0,         // active antenna 0
		1,         // RF mode
		2,         // tx power idx
		60, 90, 5, // down RSSI/LQ/SNR
	}
	l, ok := parseLink(body)
	if !ok {
		t.Fatal("parseLink failed")
	}
	if l.UplinkRSSIdBm != -70 {
		t.Errorf("uplink RSSI (ant 0): got %d, want -70", l.UplinkRSSIdBm)
	}

	// active_antenna = 1 → use ant2 RSSI (b[1])
	body[4] = 1
	l, _ = parseLink(body)
	if l.UplinkRSSIdBm != -80 {
		t.Errorf("uplink RSSI (ant 1): got %d, want -80", l.UplinkRSSIdBm)
	}

	if l.UplinkLQ != 95 || l.DownlinkLQ != 90 {
		t.Errorf("LQ values: got up=%d down=%d", l.UplinkLQ, l.DownlinkLQ)
	}
}

func TestParseFlightMode_NullTerminated(t *testing.T) {
	// "ANGL\0junk"
	body := []byte{'A', 'N', 'G', 'L', 0, 'X', 'Y'}
	m, ok := parseFlightMode(body)
	if !ok {
		t.Fatal("parseFlightMode failed")
	}
	if m.Mode != "ANGL" {
		t.Errorf("mode: got %q, want ANGL", m.Mode)
	}
}

func TestParseFlightMode_NoNull(t *testing.T) {
	// No null in the buffer; the whole thing is the mode string.
	body := []byte{'A', 'C', 'R', 'O'}
	m, ok := parseFlightMode(body)
	if !ok {
		t.Fatal("parseFlightMode failed")
	}
	if m.Mode != "ACRO" {
		t.Errorf("mode: got %q, want ACRO", m.Mode)
	}
}

func TestState_Feed_UnknownTypeReturnsFalse(t *testing.T) {
	s := New(nil)
	// Frame type 0xFE not in our known set.
	ok := s.Feed(wrap(0xEA, 0xFE, []byte{1, 2, 3}))
	if ok {
		t.Errorf("expected false for unknown frame type")
	}
}

func TestState_Feed_BatteryDetectsCellCount(t *testing.T) {
	s := New(nil)
	// 16.8V → 4S (16.8 / 4.2 = 4.0)
	body := make([]byte, 8)
	binary.BigEndian.PutUint16(body[0:2], 168)
	if !s.Feed(wrap(0xEA, frameBattery, body)) {
		t.Fatal("Feed battery failed")
	}
	snap := s.Snapshot()
	if snap.Battery == nil || snap.Battery.Data.CellCount != 4 {
		t.Errorf("expected 4-cell detection, got %+v", snap.Battery)
	}
	// VoltsCell should be ~4.2.
	vc := snap.Battery.Data.VoltsCell
	if vc < 4.19 || vc > 4.21 {
		t.Errorf("voltsCell: got %f, want ~4.2", vc)
	}
}

func TestState_Feed_BatteryCellCountSticky(t *testing.T) {
	// Cell count detected on first frame should NOT be re-detected as
	// the pack discharges. Initial 16.8V → 4 cells. Later 12.0V should
	// still report 4 cells (12/4 = 3.0V/cell, healthy mid-discharge).
	s := New(nil)
	body := make([]byte, 8)
	binary.BigEndian.PutUint16(body[0:2], 168)
	s.Feed(wrap(0xEA, frameBattery, body))

	binary.BigEndian.PutUint16(body[0:2], 120) // 12.0V mid-flight
	s.Feed(wrap(0xEA, frameBattery, body))

	snap := s.Snapshot()
	if snap.Battery.Data.CellCount != 4 {
		t.Errorf("cell count should remain 4, got %d", snap.Battery.Data.CellCount)
	}
}

func TestState_Feed_BatteryCellCountVariousChemistries(t *testing.T) {
	cases := []struct {
		name      string
		volts     float64
		wantCells int
	}{
		{"3S LiPo full", 12.6, 3},
		{"4S LiPo full", 16.8, 4},
		{"6S LiPo full", 25.2, 6},
		{"3S LiHV nominal", 11.4, 3},   // 3.8V/cell
		{"4S Li-ion mid-charge", 14.4, 4}, // 3.6V/cell
		{"6S Li-ion mid-charge", 21.6, 6}, // 3.6V/cell
		{"2S boundary (8.4V)", 8.4, 2},
		// Edge: heuristic over-estimates on partially discharged packs:
		{"3S at 3.5V/cell (10.5V) at first connect", 10.5, 3}, // ceil(10.5/4.2) = 3 ✓
		{"4S at 3.4V/cell (13.6V)", 13.6, 4},                  // ceil(13.6/4.2) = 4 ✓
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(nil)
			body := make([]byte, 8)
			binary.BigEndian.PutUint16(body[0:2], uint16(c.volts*10))
			s.Feed(wrap(0xEA, frameBattery, body))
			snap := s.Snapshot()
			if snap.Battery == nil {
				t.Fatal("no battery snapshot")
			}
			if snap.Battery.Data.CellCount != c.wantCells {
				t.Errorf("cells: got %d, want %d (volts %.1f)",
					snap.Battery.Data.CellCount, c.wantCells, c.volts)
			}
		})
	}
}

func TestState_Snapshot_HaveAny(t *testing.T) {
	s := New(nil)
	if s.Snapshot().HaveAny {
		t.Errorf("HaveAny should be false on empty state")
	}
	body := make([]byte, 15)
	body[14] = 8
	s.Feed(wrap(0xEA, frameGPS, body))
	if !s.Snapshot().HaveAny {
		t.Errorf("HaveAny should be true after first frame")
	}
}

func TestState_Reset_ClearsAll(t *testing.T) {
	s := New(nil)
	body := make([]byte, 15)
	body[14] = 8
	s.Feed(wrap(0xEA, frameGPS, body))
	s.Reset()
	snap := s.Snapshot()
	if snap.HaveAny || snap.GPS != nil {
		t.Errorf("Reset should wipe state, got %+v", snap)
	}
}

func TestState_StaleDetection(t *testing.T) {
	// Inject a Link frame with a fake old timestamp by manipulating
	// the State directly. (More realistic test would require a clock
	// abstraction; this is a lighter-weight check that the staleness
	// computation works.)
	s := New(nil)
	body := []byte{50, 60, 99, 5, 0, 1, 2, 40, 95, 3}
	s.Feed(wrap(0xEA, frameLink, body))
	// Force the timestamp into the past beyond the link stale window.
	s.linkAt = time.Now().Add(-2 * time.Second)
	snap := s.Snapshot()
	if !snap.Link.Stale {
		t.Errorf("expected Link to be stale after 2s (window is 1s)")
	}
}

func TestState_FeedShortPayloadIgnored(t *testing.T) {
	s := New(nil)
	if s.Feed([]byte{0xEA}) {
		t.Errorf("Feed should reject 1-byte payload")
	}
	if s.Feed(nil) {
		t.Errorf("Feed should reject nil payload")
	}
}
