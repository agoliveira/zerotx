package main

// flight_events.go: detect significant in-flight events from
// telemetry snapshots and feed them to the recorder. Runs alongside
// the existing telemetry sampler in main.go and is driven by the
// same 5Hz tick.
//
// Event design:
//   - Edge-triggered: a state change produces one event, not a stream.
//   - Idempotent within a flight: peak-distance fires every time the
//     peak advances (so a flight that progressively goes farther logs
//     a series of peaks); GPS-lock fires once per acquire / lose pair.
//   - Pre-arm events are not relevant to the flight log; the detector
//     only runs while the recorder reports armed.
//
// The detector does NOT speak via TTS. Its job is to populate the
// recorder events table. The post-flight narrator reads those events
// and synthesizes the narration. Keeping detection and narration
// separate means the events are also useful for non-audio analysis
// (replay, debugging, summary stats) without needing TTS to be on.

import (
	"sync/atomic"

	"github.com/agoliveira/zerotx/pi/daemon/internal/recorder"
	"github.com/agoliveira/zerotx/pi/daemon/internal/recovery"
	"github.com/agoliveira/zerotx/pi/daemon/internal/telemetry"
)

// flightEventDetector is a small state machine that consumes
// telemetry snapshots and emits events to the recorder. Not safe
// for concurrent use; callers must serialize Tick() calls (which
// the telemetry sampler does naturally — single goroutine).
//
// When a recovery.Manager is registered (via SetRecoveryManager),
// failsafe transitions also auto-activate the lost-aircraft
// recovery view.
type flightEventDetector struct {
	rec recorder.Interface

	// recoveryMgr is set after construction (the manager itself
	// is built later in main, once gpsRdr and the recorder exist).
	// atomic.Pointer because the telemetry sampler goroutine reads
	// this while the main goroutine writes it -- without atomic,
	// the race detector trips even though the access is one word.
	recoveryMgr atomic.Pointer[recovery.Manager]

	// armed mirrors the recorder's session state. Events are only
	// emitted while armed; setArmed(true) resets per-flight state.
	armed bool

	// Per-flight state (reset on arm).
	lastMode         string
	lastSats         int  // -1 = no GPS observation yet
	gpsLocked        bool // sats >= 6
	homeSet          bool
	peakDistanceM    int32
	peakAltitudeM    int32
	batteryThreshold batteryThreshold
	linkQuality      linkQuality
	rthActive        bool
	failsafeActive   bool
}

// batteryThreshold is the cell-voltage band the battery is currently in.
// Edges between bands fire events; staying in a band does not.
type batteryThreshold int

const (
	batOK batteryThreshold = iota
	batLow
	batCritical
)

// linkQuality is the LQ band the link is in. Drops fire events;
// recoveries fire one event when LQ returns above the recovery line.
type linkQuality int

const (
	linkGood linkQuality = iota
	linkDegraded
	linkPoor
)

// Thresholds. These match the design discussion (per-cell voltages,
// LQ percent steps). Tunable — exposing them as config is a future
// step if operators want different bands.
const (
	cellLowVolts      = 3.6
	cellCriticalVolts = 3.4
	lqDegradedBelow   = 80
	lqPoorBelow       = 50
	lqRecoverAbove    = 90
	gpsLockMinSats    = 6
)

// newFlightEventDetector constructs a detector backed by the given
// recorder. The recorder may be a NoOpRecorder; in that case the
// detector's events go nowhere, which is fine.
func newFlightEventDetector(rec recorder.Interface) *flightEventDetector {
	d := &flightEventDetector{rec: rec}
	d.resetFlightState()
	return d
}

// SetRecoveryManager wires a recovery.Manager so failsafe transitions
// auto-activate the lost-aircraft recovery view. Optional: tests
// commonly skip this (auto-trigger isn't under test). Production
// calls this once at startup right after newFlightEventDetector.
func (d *flightEventDetector) SetRecoveryManager(m *recovery.Manager) {
	d.recoveryMgr.Store(m)
}

// resetFlightState wipes per-flight tracking. Called on arm so a new
// flight starts with a clean slate (peaks reset to zero, last-seen
// values cleared so the first observations re-fire as edges).
func (d *flightEventDetector) resetFlightState() {
	d.lastMode = ""
	d.lastSats = -1
	d.gpsLocked = false
	d.homeSet = false
	d.peakDistanceM = 0
	d.peakAltitudeM = 0
	d.batteryThreshold = batOK
	d.linkQuality = linkGood
	d.rthActive = false
	d.failsafeActive = false
}

// SetArmed updates the detector's armed flag. Transitions reset
// per-flight state and emit arm/disarm events. Callers should call
// this from the same goroutine that calls Tick.
func (d *flightEventDetector) SetArmed(armed bool) {
	if armed == d.armed {
		return
	}
	d.armed = armed
	if armed {
		d.resetFlightState()
		d.rec.LogEvent("flight", "armed", "info", nil)
	} else {
		d.rec.LogEvent("flight", "disarmed", "info", nil)
	}
}

// Tick consumes one telemetry snapshot and emits any events that
// follow from the change since last tick. Safe to call when not
// armed (returns immediately).
func (d *flightEventDetector) Tick(snap telemetry.Snapshot) {
	if !d.armed {
		return
	}
	d.checkGPS(snap)
	d.checkHome(snap)
	d.checkMode(snap)
	d.checkPosition(snap)
	d.checkBattery(snap)
	d.checkLink(snap)
}

func (d *flightEventDetector) checkGPS(snap telemetry.Snapshot) {
	if snap.GPS == nil || snap.GPS.Stale {
		return
	}
	g := snap.GPS.Data
	sats := int(g.Sats)

	// First observation of any sats, log it.
	if d.lastSats < 0 && sats > 0 {
		d.rec.LogEvent("flight", "first-sats", "info", map[string]interface{}{"sats": sats})
	}
	d.lastSats = sats

	locked := sats >= gpsLockMinSats
	if locked && !d.gpsLocked {
		d.rec.LogEvent("flight", "gps-lock-acquired", "info", map[string]interface{}{"sats": sats})
		d.gpsLocked = true
	} else if !locked && d.gpsLocked {
		d.rec.LogEvent("flight", "gps-lock-lost", "warning", map[string]interface{}{"sats": sats})
		d.gpsLocked = false
	}
}

func (d *flightEventDetector) checkHome(snap telemetry.Snapshot) {
	if d.homeSet || snap.Home == nil {
		return
	}
	d.rec.LogEvent("flight", "home-set", "info", map[string]interface{}{
		"lat": snap.Home.Data.LatDeg,
		"lon": snap.Home.Data.LonDeg,
	})
	d.homeSet = true
}

func (d *flightEventDetector) checkMode(snap telemetry.Snapshot) {
	if snap.FlightMode == nil {
		return
	}
	m := snap.FlightMode.Data.Mode
	if m == "" || m == d.lastMode {
		return
	}
	d.lastMode = m
	d.rec.LogEvent("flight", "mode-change", "info", map[string]interface{}{"mode": m})

	// RTH and failsafe are derived from the mode string. INAV uses
	// "RTH" (with possible trailing space) and "FS"/"!FS" variants.
	rth := m == "RTH" || m == "RTH "
	if rth && !d.rthActive {
		d.rec.LogEvent("flight", "rth-active", "warning", nil)
		d.rthActive = true
	} else if !rth && d.rthActive {
		d.rthActive = false
	}

	failsafe := m == "FS" || m == "!FS" || m == "!ERR"
	if failsafe && !d.failsafeActive {
		d.rec.LogEvent("flight", "failsafe", "critical", map[string]interface{}{"mode": m})
		d.failsafeActive = true
		// Auto-activate the lost-aircraft recovery view. The frozen
		// snapshot captures aircraft state at the moment failsafe
		// fired. GPS data is included only when fresh -- a stale
		// position at trigger time is misleading (says "aircraft
		// here" when it could have drifted minutes ago).
		if mgr := d.recoveryMgr.Load(); mgr != nil {
			mgr.Trigger(recovery.ReasonFailsafe, buildFrozenSnapshot(m, snap))
		}
	} else if !failsafe && d.failsafeActive {
		d.failsafeActive = false
	}
}

// buildFrozenSnapshot extracts the recovery.Snapshot fields from a
// telemetry snapshot at the moment of failsafe. The Mode string is
// passed in separately because the caller already has the canonical
// value (FS / !FS / !ERR) and we'd rather record the exact mode
// observed than whatever the FlightMode entry currently reads (they
// should agree, but the caller's value is the trigger truth).
func buildFrozenSnapshot(mode string, snap telemetry.Snapshot) recovery.Snapshot {
	out := recovery.Snapshot{Mode: mode}
	if snap.GPS != nil && !snap.GPS.Stale {
		g := snap.GPS.Data
		out.LatDeg = g.LatDeg
		out.LonDeg = g.LonDeg
		out.AltMeters = g.AltMeters
		out.GroundKmh = g.GroundKmh
		out.HeadingDeg = g.HeadingDeg
		// A real fix requires at least a 2D solution; the GPS
		// type's Sats field is a useful proxy. The lat/lon
		// non-zero check avoids the rare "0,0 with sats=6" wire
		// artifact.
		out.HasGPS = g.Sats >= 4 && (g.LatDeg != 0 || g.LonDeg != 0)
	}
	return out
}

func (d *flightEventDetector) checkPosition(snap telemetry.Snapshot) {
	// Aircraft position at the moment we observed this peak. Used
	// to enrich post-flight narration ("peak altitude near $place").
	// Only attached when GPS is fresh; stale readings would point
	// at a place the aircraft already left.
	var lat, lon float64
	var havePos bool
	if snap.GPS != nil && !snap.GPS.Stale {
		lat = snap.GPS.Data.LatDeg
		lon = snap.GPS.Data.LonDeg
		havePos = true
	}

	if snap.Home != nil {
		dist := snap.Home.Data.DistanceM
		if dist > d.peakDistanceM {
			// Only emit when the peak crosses a new 50m boundary
			// (50, 100, 150...). Otherwise every meter of advance
			// would log. Edge: 0 -> 47 logs nothing; 0 -> 53 logs 50.
			oldBucket := d.peakDistanceM / 50
			newBucket := dist / 50
			d.peakDistanceM = dist
			if newBucket > oldBucket {
				detail := map[string]interface{}{"meters": dist}
				if havePos {
					detail["lat"] = lat
					detail["lon"] = lon
				}
				d.rec.LogEvent("flight", "peak-distance", "info", detail)
			}
		}
	}
	if snap.GPS != nil && !snap.GPS.Stale {
		alt := snap.GPS.Data.AltMeters
		if alt > d.peakAltitudeM {
			oldBucket := d.peakAltitudeM / 25
			newBucket := alt / 25
			d.peakAltitudeM = alt
			if newBucket > oldBucket {
				d.rec.LogEvent("flight", "peak-altitude", "info", map[string]interface{}{
					"meters": alt,
					"lat":    lat,
					"lon":    lon,
				})
			}
		}
	}
}

func (d *flightEventDetector) checkBattery(snap telemetry.Snapshot) {
	if snap.Battery == nil || snap.Battery.Stale {
		return
	}
	b := snap.Battery.Data
	if b.CellCount == 0 || b.VoltsCell == 0 {
		return
	}
	cv := b.VoltsCell

	var th batteryThreshold
	switch {
	case cv < cellCriticalVolts:
		th = batCritical
	case cv < cellLowVolts:
		th = batLow
	default:
		th = batOK
	}
	if th == d.batteryThreshold {
		return
	}
	// Only emit on transitions to a worse state. Recoveries (battery
	// going up) are not realistic in a flight; if we see one, suppress
	// the event but update the band so a re-drop fires correctly.
	if th > d.batteryThreshold {
		switch th {
		case batLow:
			d.rec.LogEvent("flight", "battery-low", "warning", map[string]interface{}{
				"cellVolts": cv, "volts": b.Volts, "percent": b.Percent,
			})
		case batCritical:
			d.rec.LogEvent("flight", "battery-critical", "critical", map[string]interface{}{
				"cellVolts": cv, "volts": b.Volts, "percent": b.Percent,
			})
		}
	}
	d.batteryThreshold = th
}

func (d *flightEventDetector) checkLink(snap telemetry.Snapshot) {
	if snap.Link == nil || snap.Link.Stale {
		return
	}
	lq := int(snap.Link.Data.UplinkLQ)

	var q linkQuality
	switch {
	case lq < lqPoorBelow:
		q = linkPoor
	case lq < lqDegradedBelow:
		q = linkDegraded
	default:
		q = linkGood
	}

	switch {
	case q > d.linkQuality:
		// Degradation: log the new (worse) state.
		switch q {
		case linkDegraded:
			d.rec.LogEvent("flight", "link-degraded", "warning", map[string]interface{}{"lq": lq})
		case linkPoor:
			d.rec.LogEvent("flight", "link-poor", "critical", map[string]interface{}{"lq": lq})
		}
		d.linkQuality = q
	case q < d.linkQuality && lq >= lqRecoverAbove:
		// Recovery: only log when we cross the recovery line, not
		// every dip back to the previous band.
		d.rec.LogEvent("flight", "link-recovered", "info", map[string]interface{}{"lq": lq})
		d.linkQuality = q
	}
}
