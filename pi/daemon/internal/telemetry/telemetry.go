// Package telemetry decodes CRSF telemetry frames forwarded by the
// RP2040 firmware and maintains a per-sensor latest-value snapshot
// for the GUI and API consumers.
//
// Design notes:
//
//   - The MCU does only minimal work (CRC validation, framing). All
//     parsing lives here in Go where it's easy to extend with new
//     sensor types and easy to test.
//
//   - Frames arrive as raw CRSF payloads via IPC (MsgTelemetry). The
//     daemon's IPC dispatcher calls Feed for each frame.
//
//   - Latest value per sensor type is kept with a timestamp. Sensors
//     that haven't been updated within their per-type stale window
//     are flagged Stale=true on the API output but still served (the
//     last-known value is often more useful than nothing).
//
//   - The daemon may run with no telemetry at all (operator chose to
//     fly without it, or the radio link doesn't carry it). The State
//     handles "never seen" by returning nil pointers; the GUI's
//     auto-fb checklist items fall back to manual confirmations.
//
// CRSF frame types parsed today:
//
//	0x02 GPS                 lat/lon, ground speed, heading, altitude, sats
//	0x08 Battery sensor      voltage, current, used capacity, percentage
//	0x14 Link statistics     up/downlink RSSI/LQ/SNR, TX power
//	0x21 Flight mode         FC-defined string ("ANGLE", "RTH", etc.)
//
// Future work (not in this round): vario, attitude, baro, GPS time.
// MSP-over-CRSF (frame 0x7A) would let us query MSP_BATTERY_STATE for
// real cell count. Deferred; for now we use a charged-LiPo heuristic
// for cell count.
package telemetry

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"time"
)

// CRSF telemetry frame types we care about. The CRSF spec defines
// many more; these are the ones we parse today.
const (
	frameGPS        byte = 0x02
	frameBattery    byte = 0x08
	frameLink       byte = 0x14
	frameAttitude   byte = 0x1E
	frameFlightMode byte = 0x21
)

// Stale-detection windows per frame type. Chosen to be ~5x the typical
// emission rate of the corresponding sensor in INAV defaults.
// Crossing the threshold means "we should distrust this data" rather
// than "discard it"; the State still serves the last value with
// Stale=true so the GUI can decide what to do.
var staleWindow = map[byte]time.Duration{
	frameGPS:        2 * time.Second,  // ~5 Hz typical
	frameBattery:    5 * time.Second,  // ~1 Hz typical
	frameLink:       1 * time.Second,  // ~10 Hz typical
	frameAttitude:   1 * time.Second,  // ~10 Hz typical
	frameFlightMode: 30 * time.Second, // emitted on change
}

// GPS holds parsed CRSF GPS telemetry.
//
// Coordinates are in CRSF native format (degrees * 1e7) converted to
// floating point degrees here so consumers don't all have to know about
// the wire format.
type GPS struct {
	LatDeg     float64 `json:"latDeg"`
	LonDeg     float64 `json:"lonDeg"`
	GroundKmh  float64 `json:"groundKmh"`
	HeadingDeg float64 `json:"headingDeg"`
	AltMeters  int32   `json:"altMeters"` // CRSF altitude is meters + 1000 offset
	Sats       uint8   `json:"sats"`
}

// Battery holds parsed CRSF battery telemetry.
//
// CellCount is derived heuristically from initial voltage (assumed near
// fully charged): ceil(initialVoltage / 4.2). Cached in the State once
// detected; not redetected on every frame. Wrong for partially
// discharged packs at first connection but works for the 95% case.
type Battery struct {
	Volts     float64 `json:"volts"`
	Amps      float64 `json:"amps"`
	UsedMAh   int32   `json:"usedMAh"`
	Percent   uint8   `json:"percent"`
	CellCount int     `json:"cellCount"` // 0 if not yet detected
	VoltsCell float64 `json:"voltsCell"` // Volts/CellCount when known
}

// Link holds parsed CRSF link statistics.
//
// CRSF reports RSSI as positive uint8 (which we negate to get dBm),
// LQ as a percentage, SNR as a signed dB value, and TX power as an
// indexed enum. We keep the raw values plus a denormalised dBm.
type Link struct {
	UplinkRSSIdBm   int8  `json:"uplinkRssiDbm"`
	UplinkLQ        uint8 `json:"uplinkLq"`
	UplinkSNR       int8  `json:"uplinkSnr"`
	DownlinkRSSIdBm int8  `json:"downlinkRssiDbm"`
	DownlinkLQ      uint8 `json:"downlinkLq"`
	DownlinkSNR     int8  `json:"downlinkSnr"`
	RFMode          uint8 `json:"rfMode"`
	TxPowerIdx      uint8 `json:"txPowerIdx"`
}

// Attitude holds parsed CRSF attitude telemetry. Roll, pitch, yaw
// in degrees. CRSF wire format is 1/10000 of a radian as int16
// big-endian; we convert to degrees here so consumers don't need
// to know the units. Pitch positive = nose up; roll positive =
// right wing down (standard aviation convention).
type Attitude struct {
	RollDeg  float64 `json:"rollDeg"`
	PitchDeg float64 `json:"pitchDeg"`
	YawDeg   float64 `json:"yawDeg"`
}

// FlightMode holds the FC-reported flight mode string. INAV uses
// "ANGL", "ACRO", "MANU", "RTH ", "WP  ", etc. The daemon doesn't
// interpret; it just exposes the string.
type FlightMode struct {
	Mode string `json:"mode"`
}

// Snapshot is the API surface: a typed snapshot of all known telemetry
// sensors with per-sensor staleness and freshness timestamps. Pointers
// are nil when no data has ever been received for that sensor.
type Snapshot struct {
	GPS        *GPSEntry        `json:"gps,omitempty"`
	Battery    *BatteryEntry    `json:"battery,omitempty"`
	Link       *LinkEntry       `json:"link,omitempty"`
	Attitude   *AttitudeEntry   `json:"attitude,omitempty"`
	FlightMode *FlightModeEntry `json:"flightMode,omitempty"`
	Home       *HomeEntry       `json:"home,omitempty"`

	// HaveAny is true if at least one sensor has ever produced data.
	// The GUI uses this to distinguish "telemetry not flowing yet"
	// from "telemetry not configured at all" — the latter prompts
	// fallback to manual confirmations.
	HaveAny bool `json:"haveAny"`
}

type GPSEntry struct {
	Data    GPS    `json:"data"`
	Updated string `json:"updated"`
	Stale   bool   `json:"stale"`
}
type BatteryEntry struct {
	Data    Battery `json:"data"`
	Updated string  `json:"updated"`
	Stale   bool    `json:"stale"`
}
type LinkEntry struct {
	Data    Link   `json:"data"`
	Updated string `json:"updated"`
	Stale   bool   `json:"stale"`
}
type AttitudeEntry struct {
	Data    Attitude `json:"data"`
	Updated string   `json:"updated"`
	Stale   bool     `json:"stale"`
}
type FlightModeEntry struct {
	Data    FlightMode `json:"data"`
	Updated string     `json:"updated"`
	Stale   bool       `json:"stale"`
}

// Home is the home position recorded on arm (or by explicit
// operator command), plus the current great-circle distance from
// the latest GPS sample. DistanceM is 0 when no current GPS fix
// is available even if Home is set.
type Home struct {
	LatDeg    float64 `json:"latDeg"`
	LonDeg    float64 `json:"lonDeg"`
	DistanceM int32   `json:"distanceM"`
}
type HomeEntry struct {
	Data    Home   `json:"data"`
	Updated string `json:"updated"` // when home was set
}

// State holds the latest decoded value per sensor plus an update
// timestamp. Safe for concurrent use.
type State struct {
	mu sync.RWMutex

	gps       *GPS
	gpsAt     time.Time
	battery   *Battery
	batteryAt time.Time
	link      *Link
	linkAt    time.Time
	attitude   *Attitude
	attitudeAt time.Time
	mode      *FlightMode
	modeAt    time.Time

	// cellCount is detected on first battery telemetry frame and cached
	// for the lifetime of the State. Setting it back to 0 (e.g. on
	// model unload) would force re-detection on next first frame.
	cellCount int

	// Home position, set by SetHome (typically called on arm with a
	// valid GPS lock). Distance-from-home in Snapshot is computed
	// from this against the current GPS sample. Zero (homeSet=false)
	// means no home, and Snapshot reports HaveHome=false.
	homeLat float64
	homeLon float64
	homeSet bool
	homeAt  time.Time

	// log of frame types we couldn't parse (for warn-once logging).
	unknownTypes map[byte]bool

	// optional logger; nil means no logging.
	logf func(format string, args ...interface{})
}

// New constructs an empty State. logf is optional; if non-nil, the
// State logs first-time observations of unknown CRSF frame types
// (helpful when adding support for new sensors).
func New(logf func(format string, args ...interface{})) *State {
	return &State{
		unknownTypes: make(map[byte]bool),
		logf:         logf,
	}
}

// Feed accepts a raw CRSF telemetry payload as forwarded by the MCU
// over IPC: [addr:1][type:1][payload:N]. Returns true if the frame was
// recognised and parsed; false for unknown types or malformed payloads.
//
// Errors are not propagated: they're logged once per type and the
// frame is dropped. The daemon's tick loop is the time-critical path;
// telemetry parsing must never block or fail it.
func (s *State) Feed(payload []byte) bool {
	if len(payload) < 2 {
		return false
	}
	// payload[0] is the CRSF address byte; we ignore it. payload[1] is
	// the frame type. The rest is the per-type payload.
	frameType := payload[1]
	body := payload[2:]
	now := time.Now()

	switch frameType {
	case frameGPS:
		g, ok := parseGPS(body)
		if !ok {
			return false
		}
		s.mu.Lock()
		s.gps = &g
		s.gpsAt = now
		s.mu.Unlock()
		return true

	case frameBattery:
		b, ok := parseBattery(body)
		if !ok {
			return false
		}
		s.mu.Lock()
		// Detect cell count on first ever observation (or after explicit
		// reset). Heuristic: assume initial reading is near full charge,
		// so cells = ceil(volts / 4.2). Capped at 12 to avoid junk
		// readings producing absurd counts.
		if s.cellCount == 0 && b.Volts > 0 {
			cells := int(math.Ceil(b.Volts / 4.2))
			if cells > 12 {
				cells = 12
			}
			if cells > 0 {
				s.cellCount = cells
				if s.logf != nil {
					s.logf("telemetry: detected %d-cell battery (initial %.2fV)",
						cells, b.Volts)
				}
			}
		}
		b.CellCount = s.cellCount
		if s.cellCount > 0 {
			b.VoltsCell = b.Volts / float64(s.cellCount)
		}
		s.battery = &b
		s.batteryAt = now
		s.mu.Unlock()
		return true

	case frameLink:
		l, ok := parseLink(body)
		if !ok {
			return false
		}
		s.mu.Lock()
		s.link = &l
		s.linkAt = now
		s.mu.Unlock()
		return true

	case frameAttitude:
		a, ok := parseAttitude(body)
		if !ok {
			return false
		}
		s.mu.Lock()
		s.attitude = &a
		s.attitudeAt = now
		s.mu.Unlock()
		return true

	case frameFlightMode:
		m, ok := parseFlightMode(body)
		if !ok {
			return false
		}
		s.mu.Lock()
		s.mode = &m
		s.modeAt = now
		s.mu.Unlock()
		return true
	}

	// Unknown frame type. Log once.
	s.mu.Lock()
	first := !s.unknownTypes[frameType]
	s.unknownTypes[frameType] = true
	s.mu.Unlock()
	if first && s.logf != nil {
		s.logf("telemetry: ignoring unknown CRSF frame type 0x%02X (will not log again)", frameType)
	}
	return false
}

// Snapshot returns the current decoded state of all sensors. Safe to
// call from any goroutine.
func (s *State) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	out := Snapshot{}
	if s.gps != nil {
		out.GPS = &GPSEntry{
			Data:    *s.gps,
			Updated: s.gpsAt.UTC().Format(time.RFC3339Nano),
			Stale:   isStale(s.gpsAt, now, frameGPS),
		}
		out.HaveAny = true
	}
	if s.battery != nil {
		out.Battery = &BatteryEntry{
			Data:    *s.battery,
			Updated: s.batteryAt.UTC().Format(time.RFC3339Nano),
			Stale:   isStale(s.batteryAt, now, frameBattery),
		}
		out.HaveAny = true
	}
	if s.link != nil {
		out.Link = &LinkEntry{
			Data:    *s.link,
			Updated: s.linkAt.UTC().Format(time.RFC3339Nano),
			Stale:   isStale(s.linkAt, now, frameLink),
		}
		out.HaveAny = true
	}
	if s.attitude != nil {
		out.Attitude = &AttitudeEntry{
			Data:    *s.attitude,
			Updated: s.attitudeAt.UTC().Format(time.RFC3339Nano),
			Stale:   isStale(s.attitudeAt, now, frameAttitude),
		}
		out.HaveAny = true
	}
	if s.mode != nil {
		out.FlightMode = &FlightModeEntry{
			Data:    *s.mode,
			Updated: s.modeAt.UTC().Format(time.RFC3339Nano),
			Stale:   isStale(s.modeAt, now, frameFlightMode),
		}
		out.HaveAny = true
	}
	if s.homeSet {
		var dist int32
		if s.gps != nil {
			d := haversineMeters(s.homeLat, s.homeLon, s.gps.LatDeg, s.gps.LonDeg)
			dist = int32(d + 0.5)
		}
		out.Home = &HomeEntry{
			Data: Home{
				LatDeg:    s.homeLat,
				LonDeg:    s.homeLon,
				DistanceM: dist,
			},
			Updated: s.homeAt.UTC().Format(time.RFC3339Nano),
		}
	}
	return out
}

// Reset wipes all state. Useful when the model is unloaded or the
// link is re-established (forces cell-count re-detection).
func (s *State) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gps = nil
	s.battery = nil
	s.link = nil
	s.mode = nil
	s.cellCount = 0
	s.homeSet = false
}

// === Frame decoders ===
//
// CRSF telemetry uses big-endian for multi-byte fields. Reference:
// https://github.com/crsf-wg/crsf/wiki/Telemetry

func parseGPS(b []byte) (GPS, bool) {
	// Layout: lat:int32 BE, lon:int32 BE, gs:uint16 BE (km/h * 10),
	//         hdg:uint16 BE (deg * 100), alt:uint16 BE (m + 1000),
	//         sats:uint8.  Total 15 bytes.
	if len(b) < 15 {
		return GPS{}, false
	}
	lat := int32(binary.BigEndian.Uint32(b[0:4]))
	lon := int32(binary.BigEndian.Uint32(b[4:8]))
	gs := binary.BigEndian.Uint16(b[8:10])
	hd := binary.BigEndian.Uint16(b[10:12])
	al := int32(binary.BigEndian.Uint16(b[12:14])) - 1000
	st := b[14]
	return GPS{
		LatDeg:     float64(lat) / 1e7,
		LonDeg:     float64(lon) / 1e7,
		GroundKmh:  float64(gs) / 10.0,
		HeadingDeg: float64(hd) / 100.0,
		AltMeters:  al,
		Sats:       st,
	}, true
}

func parseBattery(b []byte) (Battery, bool) {
	// Layout: voltage:uint16 BE (0.1V), current:uint16 BE (0.1A),
	//         capacity:24-bit BE (mAh), percent:uint8.  Total 8 bytes.
	if len(b) < 8 {
		return Battery{}, false
	}
	v := binary.BigEndian.Uint16(b[0:2])
	a := binary.BigEndian.Uint16(b[2:4])
	cap24 := uint32(b[4])<<16 | uint32(b[5])<<8 | uint32(b[6])
	pct := b[7]
	return Battery{
		Volts:   float64(v) / 10.0,
		Amps:    float64(a) / 10.0,
		UsedMAh: int32(cap24),
		Percent: pct,
	}, true
}

func parseLink(b []byte) (Link, bool) {
	// Layout (10 bytes):
	//   uplink_rssi_ant1:uint8 (positive, negate for dBm)
	//   uplink_rssi_ant2:uint8
	//   uplink_lq:uint8
	//   uplink_snr:int8
	//   active_antenna:uint8
	//   rf_mode:uint8
	//   uplink_tx_power:uint8 (enum index)
	//   downlink_rssi:uint8
	//   downlink_lq:uint8
	//   downlink_snr:int8
	if len(b) < 10 {
		return Link{}, false
	}
	// We pick the better of the two uplink antennas based on active.
	upRSSI := b[0]
	if b[4] == 1 {
		upRSSI = b[1]
	}
	return Link{
		UplinkRSSIdBm:   -int8(upRSSI),
		UplinkLQ:        b[2],
		UplinkSNR:       int8(b[3]),
		RFMode:          b[5],
		TxPowerIdx:      b[6],
		DownlinkRSSIdBm: -int8(b[7]),
		DownlinkLQ:      b[8],
		DownlinkSNR:     int8(b[9]),
	}, true
}

// parseAttitude decodes CRSF frame 0x1E. Layout: 6 bytes, three
// int16 big-endian values for pitch, roll, yaw, each in 1/10000 of
// a radian. Pitch comes first on the wire (despite "attitude"
// usually being read as roll-pitch-yaw); CRSF spec order is
// pitch/roll/yaw.
func parseAttitude(b []byte) (Attitude, bool) {
	if len(b) < 6 {
		return Attitude{}, false
	}
	pitch := int16(binary.BigEndian.Uint16(b[0:2]))
	roll := int16(binary.BigEndian.Uint16(b[2:4]))
	yaw := int16(binary.BigEndian.Uint16(b[4:6]))
	const radToDeg = 180.0 / math.Pi
	const scale = radToDeg / 10000.0
	return Attitude{
		PitchDeg: float64(pitch) * scale,
		RollDeg:  float64(roll) * scale,
		YawDeg:   float64(yaw) * scale,
	}, true
}

func parseFlightMode(b []byte) (FlightMode, bool) {
	// Layout: null-terminated ASCII string.
	if len(b) == 0 {
		return FlightMode{}, false
	}
	end := len(b)
	for i, c := range b {
		if c == 0 {
			end = i
			break
		}
	}
	return FlightMode{Mode: string(b[:end])}, true
}

// isStale reports whether at-time is older than the per-type window.
// Defaults to 30s if no window is registered for the type.
func isStale(at, now time.Time, frameType byte) bool {
	if at.IsZero() {
		return true
	}
	w, ok := staleWindow[frameType]
	if !ok {
		w = 30 * time.Second
	}
	return now.Sub(at) > w
}

// String is a human-friendly summary of the snapshot, useful in logs.
func (s Snapshot) String() string {
	parts := []string{}
	if s.Battery != nil {
		parts = append(parts, fmt.Sprintf("bat=%.2fV/%.1fA/%d%%",
			s.Battery.Data.Volts, s.Battery.Data.Amps, s.Battery.Data.Percent))
	}
	if s.GPS != nil {
		parts = append(parts, fmt.Sprintf("gps=%dsats", s.GPS.Data.Sats))
	}
	if s.Link != nil {
		parts = append(parts, fmt.Sprintf("link=%ddBm/%d%%",
			s.Link.Data.UplinkRSSIdBm, s.Link.Data.UplinkLQ))
	}
	if s.FlightMode != nil {
		parts = append(parts, fmt.Sprintf("mode=%s", s.FlightMode.Data.Mode))
	}
	if len(parts) == 0 {
		return "<no telemetry>"
	}
	return joinNonEmpty(parts, " ")
}

func joinNonEmpty(s []string, sep string) string {
	out := ""
	for _, p := range s {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}

// === Home position ===

// SetHome records the current GPS position as home. Returns true if
// home was recorded, false if there is no current GPS fix to use.
// The caller (typically main.go on arm) decides when to call this.
//
// If force is false and home is already set, returns false without
// changing anything (idempotent on repeated arms within a flight).
// Pass force=true to override (e.g. operator explicitly resets home).
func (s *State) SetHome(force bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gps == nil {
		return false
	}
	if s.homeSet && !force {
		return false
	}
	s.homeLat = s.gps.LatDeg
	s.homeLon = s.gps.LonDeg
	s.homeSet = true
	s.homeAt = time.Now()
	if s.logf != nil {
		s.logf("telemetry: home set to %.6f,%.6f", s.homeLat, s.homeLon)
	}
	return true
}

// ClearHome wipes the recorded home. After this, Snapshot.Home is nil
// until SetHome is called again. Used on disarm-followed-by-relocate
// or on model unload.
func (s *State) ClearHome() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.homeSet = false
}

// HomePosition returns the recorded home lat/lon. ok is false when
// no home has been set in this session. This is the typed accessor
// for callers that just want the position without building a full
// JSON snapshot (the resolver chain in main.go, for example).
func (s *State) HomePosition() (lat, lon float64, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.homeSet {
		return 0, 0, false
	}
	return s.homeLat, s.homeLon, true
}

// HasHome reports whether a home position is currently set.
func (s *State) HasHome() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.homeSet
}

// haversineMeters returns the great-circle distance in meters between
// two lat/lon pairs in decimal degrees. Mean Earth radius is used;
// accuracy is more than sufficient for sub-km RC ranges where the
// Earth-as-sphere assumption introduces well under 0.5% error.
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusM = 6371000.0
	rad := math.Pi / 180.0
	phi1 := lat1 * rad
	phi2 := lat2 * rad
	dphi := (lat2 - lat1) * rad
	dlambda := (lon2 - lon1) * rad
	a := math.Sin(dphi/2)*math.Sin(dphi/2) +
		math.Cos(phi1)*math.Cos(phi2)*math.Sin(dlambda/2)*math.Sin(dlambda/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusM * c
}
