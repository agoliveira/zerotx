package main

import (
	"github.com/agoliveira/zerotx/pi/daemon/internal/gps"
	"github.com/agoliveira/zerotx/pi/daemon/internal/recorder"
	"github.com/agoliveira/zerotx/pi/daemon/internal/recovery"
)

// recoveryWiring is the bundle of objects the daemon constructs to
// stand up the lost-aircraft recovery view: the operator-position
// adapter (resolves where the operator is standing, both live and
// configured-at-boot) and the recovery state machine that consumes
// it. Bundling these in one struct removes a pair of forward-
// declared variables from main() and gives callers a single thing
// to pass through to the API provider layer.
//
// Adapter is exported so callers can inspect configuration state
// (ConfiguredSources) for the pre-flight UI without going through
// the manager. Manager is the recovery state machine itself; it
// holds a reference to the adapter internally for live-position
// queries.
//
// The struct intentionally does not own a context or any goroutines
// itself. The recovery manager is purely event-driven (Trigger,
// UpdateLastKnown, Dismiss); its lifecycle is the daemon's process
// lifecycle. No Close method is needed.
type recoveryWiring struct {
	Adapter *recoveryOperatorAdapter
	Manager *recovery.Manager
}

// newRecoveryWiring constructs both pieces from the daemon's
// runtime config. The recorder is passed through to the manager
// so recovery triggers (failsafe or manual) preserve the active
// recording via PreserveCurrentSession; the recorder may be a
// NoOpRecorder, in which case the preserve hook is a no-op.
//
// gpsRdr may be nil if the daemon was started without -gps-port;
// siteLat/siteLon are 0 if the flags were not passed. Both being
// absent is acceptable (the recovery view degrades to coords-only
// presentation); the pre-flight page warns about it via
// ConfiguredSources.
func newRecoveryWiring(gpsRdr *gps.Reader, siteLat, siteLon float64, rec recorder.Interface) *recoveryWiring {
	adapter := &recoveryOperatorAdapter{
		gps:     gpsRdr,
		siteLat: siteLat,
		siteLon: siteLon,
	}
	return &recoveryWiring{
		Adapter: adapter,
		Manager: recovery.New(adapter, rec),
	}
}
