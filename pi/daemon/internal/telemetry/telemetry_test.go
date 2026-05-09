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
	var lat int32 = 514321000 // 51.4321 * 1e7
	var lon int32 = -12345670 // -1.234567 * 1e7
	binary.BigEndian.PutUint32(body[0:4], uint32(lat))
	binary.BigEndian.PutUint32(body[4:8], uint32(lon))
	binary.BigEndian.PutUint16(body[8:10], 1234)   // 123.4 km/h
	binary.BigEndian.PutUint16(body[10:12], 17500) // 175.00 deg
	binary.BigEndian.PutUint16(body[12:14], 1450)  // alt = 450m (1450 - 1000)
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

func TestParseAttitude_RoundTrip(t *testing.T) {
	// Build a known-good attitude frame body. CRSF wire format is
	// pitch/roll/yaw in 1/10000 of a radian as int16 BE.
	body := make([]byte, 6)
	put := func(off int, v int16) {
		binary.BigEndian.PutUint16(body[off:off+2], uint16(v))
	}
	// pitch = 15 deg = 0.2618 rad → 2618 in 1/10000 rad
	put(0, 2618)
	// roll = -30 deg = -0.5236 rad → -5236
	put(2, -5236)
	// yaw = 90 deg = 1.5708 rad → 15708
	put(4, 15708)

	a, ok := parseAttitude(body)
	if !ok {
		t.Fatal("parseAttitude failed on well-formed input")
	}
	if a.PitchDeg < 14.9 || a.PitchDeg > 15.1 {
		t.Errorf("pitch: got %f, want ~15", a.PitchDeg)
	}
	if a.RollDeg < -30.1 || a.RollDeg > -29.9 {
		t.Errorf("roll: got %f, want ~-30", a.RollDeg)
	}
	if a.YawDeg < 89.9 || a.YawDeg > 90.1 {
		t.Errorf("yaw: got %f, want ~90", a.YawDeg)
	}
}

func TestParseAttitude_ShortBuffer(t *testing.T) {
	if _, ok := parseAttitude(make([]byte, 5)); ok {
		t.Errorf("parseAttitude should reject 5-byte payload")
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
		{"3S LiHV nominal", 11.4, 3},      // 3.8V/cell
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

// === Home position tests ===

// gpsBody helper packs a GPS body matching parseGPS expectations.
func gpsBody(latE7, lonE7 int32, gsKmh10 uint16, hdg100 uint16, altPlus1000 int32, sats uint8) []byte {
	body := make([]byte, 15)
	binary.BigEndian.PutUint32(body[0:4], uint32(latE7))
	binary.BigEndian.PutUint32(body[4:8], uint32(lonE7))
	binary.BigEndian.PutUint16(body[8:10], gsKmh10)
	binary.BigEndian.PutUint16(body[10:12], hdg100)
	binary.BigEndian.PutUint16(body[12:14], uint16(altPlus1000))
	body[14] = sats
	return body
}

func TestSetHome_NoGPSReturnsFalse(t *testing.T) {
	s := New(nil)
	if s.SetHome(false) {
		t.Error("SetHome should fail when no GPS data has been received")
	}
	if s.HasHome() {
		t.Error("HasHome should be false after failed SetHome")
	}
}

func TestSetHome_RecordsCurrentGPS(t *testing.T) {
	s := New(nil)
	// Feed a GPS frame at a known location.
	s.Feed(wrap(0xEA, frameGPS, gpsBody(514321000, -12345670, 0, 0, 1100, 9)))
	if !s.SetHome(false) {
		t.Fatal("SetHome should succeed once GPS is present")
	}
	snap := s.Snapshot()
	if snap.Home == nil {
		t.Fatal("Snapshot.Home should be non-nil after SetHome")
	}
	if snap.Home.Data.LatDeg < 51.4320 || snap.Home.Data.LatDeg > 51.4322 {
		t.Errorf("home lat: got %v, want ~51.4321", snap.Home.Data.LatDeg)
	}
	if snap.Home.Data.DistanceM != 0 {
		t.Errorf("distance to self should be ~0, got %d", snap.Home.Data.DistanceM)
	}
}

func TestSetHome_IdempotentUnlessForced(t *testing.T) {
	s := New(nil)
	s.Feed(wrap(0xEA, frameGPS, gpsBody(514321000, -12345670, 0, 0, 1100, 9)))
	if !s.SetHome(false) {
		t.Fatal("first SetHome should succeed")
	}
	// Move GPS.
	s.Feed(wrap(0xEA, frameGPS, gpsBody(514400000, -12300000, 0, 0, 1100, 9)))
	// Without force, second call should be a no-op.
	if s.SetHome(false) {
		t.Error("second SetHome without force should return false")
	}
	snap := s.Snapshot()
	if snap.Home.Data.LatDeg < 51.4320 || snap.Home.Data.LatDeg > 51.4322 {
		t.Errorf("home should still be original location, got %v", snap.Home.Data.LatDeg)
	}
	// With force, second call should overwrite.
	if !s.SetHome(true) {
		t.Error("forced SetHome should succeed")
	}
	snap = s.Snapshot()
	if snap.Home.Data.LatDeg < 51.4399 || snap.Home.Data.LatDeg > 51.4401 {
		t.Errorf("home should have updated to new location, got %v", snap.Home.Data.LatDeg)
	}
}

// TestHomePosition_NoHomeReturnsNotOk confirms the typed accessor
// returns ok=false when no home has been set.
func TestHomePosition_NoHomeReturnsNotOk(t *testing.T) {
	s := New(nil)
	if _, _, ok := s.HomePosition(); ok {
		t.Error("HomePosition should return ok=false when no home set")
	}
}

// TestHomePosition_AfterSetHome confirms the typed accessor returns
// the same coordinates that SetHome recorded.
func TestHomePosition_AfterSetHome(t *testing.T) {
	s := New(nil)
	s.Feed(wrap(0xEA, frameGPS, gpsBody(514321000, -12345670, 0, 0, 1100, 9)))
	if !s.SetHome(false) {
		t.Fatal("SetHome should succeed")
	}
	lat, lon, ok := s.HomePosition()
	if !ok {
		t.Fatal("HomePosition should return ok=true after SetHome")
	}
	if lat < 51.4320 || lat > 51.4322 {
		t.Errorf("lat: got %v, want ~51.4321", lat)
	}
	// CRSF GPS frame uses 1e-7 deg scaling: -12345670 -> -1.234567.
	if lon < -1.2346 || lon > -1.2345 {
		t.Errorf("lon: got %v, want ~-1.234567", lon)
	}
}

// TestHomePosition_AfterClearHome confirms ClearHome makes the typed
// accessor return ok=false again.
func TestHomePosition_AfterClearHome(t *testing.T) {
	s := New(nil)
	s.Feed(wrap(0xEA, frameGPS, gpsBody(514321000, -12345670, 0, 0, 1100, 9)))
	s.SetHome(false)
	s.ClearHome()
	if _, _, ok := s.HomePosition(); ok {
		t.Error("HomePosition should return ok=false after ClearHome")
	}
}

func TestSnapshot_DistanceComputed(t *testing.T) {
	s := New(nil)
	// Set home.
	s.Feed(wrap(0xEA, frameGPS, gpsBody(514321000, -12345670, 0, 0, 1100, 9)))
	s.SetHome(false)
	// Move ~111 meters north (0.001 deg latitude is ~111m).
	s.Feed(wrap(0xEA, frameGPS, gpsBody(514331000, -12345670, 0, 0, 1100, 9)))
	snap := s.Snapshot()
	if snap.Home == nil {
		t.Fatal("Home should be present")
	}
	dist := snap.Home.Data.DistanceM
	if dist < 100 || dist > 130 {
		t.Errorf("expected ~111m, got %d", dist)
	}
}

func TestClearHome(t *testing.T) {
	s := New(nil)
	s.Feed(wrap(0xEA, frameGPS, gpsBody(514321000, -12345670, 0, 0, 1100, 9)))
	s.SetHome(false)
	if !s.HasHome() {
		t.Fatal("home should be set")
	}
	s.ClearHome()
	if s.HasHome() {
		t.Error("home should be cleared")
	}
	if snap := s.Snapshot(); snap.Home != nil {
		t.Error("Snapshot.Home should be nil after ClearHome")
	}
}

func TestReset_ClearsHome(t *testing.T) {
	s := New(nil)
	s.Feed(wrap(0xEA, frameGPS, gpsBody(514321000, -12345670, 0, 0, 1100, 9)))
	s.SetHome(false)
	s.Reset()
	if s.HasHome() {
		t.Error("Reset should clear home")
	}
}

func TestHaversine_KnownDistance(t *testing.T) {
	// SP <-> RJ (rough), about 360 km.
	d := haversineMeters(-23.5505, -46.6333, -22.9068, -43.1729)
	if d < 350000 || d > 370000 {
		t.Errorf("SP-RJ should be ~360km, got %.0fm", d)
	}
}

func TestHaversine_SamePoint(t *testing.T) {
	d := haversineMeters(51.4321, -1.234567, 51.4321, -1.234567)
	if d > 0.001 {
		t.Errorf("same point distance should be ~0, got %v", d)
	}
}

// silence unused import if time isn't used
var _ = time.Now
