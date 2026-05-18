package main

import (
	"github.com/agoliveira/zerotx/pi/daemon/internal/gps"
	"github.com/agoliveira/zerotx/pi/daemon/internal/recovery"
)

// recoveryOperatorAdapter resolves the operator's position for the
// recovery view. Tried in order:
//
//  1. Pi-side GPS (gpsRdr): if a fix is present, use it.
//  2. -site-lat / -site-lon flags: if both are non-zero, use them.
//  3. Source "none": nothing to display; the GUI falls back to
//     coords-only presentation (the operator types lat/lon into a
//     phone).
//
// Called fresh on every recovery.Manager.State() read, so the
// operator marker on the map tracks them as they walk.
//
// Source values are stable strings the GUI matches on:
//
//   - "gps":  came from the Pi GPS (HDOP-clean, current).
//   - "site": came from the static -site-lat/-site-lon flags
//             (acceptable if the operator hasn't moved since
//             arriving; the kiosk should display a "no GPS fix,
//             using configured site" notice).
//   - "none": neither available; bearing/distance unavailable.
type recoveryOperatorAdapter struct {
	gps     *gps.Reader
	siteLat float64
	siteLon float64
}

func (a *recoveryOperatorAdapter) OperatorPosition() recovery.OperatorPosition {
	if a.gps != nil {
		s := a.gps.Get()
		if s.Fix != gps.FixNone && (s.LatDeg != 0 || s.LonDeg != 0) {
			return recovery.OperatorPosition{
				LatDeg: s.LatDeg,
				LonDeg: s.LonDeg,
				Source: "gps",
			}
		}
	}
	if a.siteLat != 0 || a.siteLon != 0 {
		return recovery.OperatorPosition{
			LatDeg: a.siteLat,
			LonDeg: a.siteLon,
			Source: "site",
		}
	}
	return recovery.OperatorPosition{Source: "none"}
}
