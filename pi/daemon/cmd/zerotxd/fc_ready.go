package main

import "strings"

// fcReadyFromMode returns true when the INAV CRSF flight-mode
// string indicates the FC is in a state that permits arming.
//
// CRSF telemetry frame type 0x21 (FLIGHT_MODE) carries a short
// (max 16-byte, null-terminated) ASCII string. INAV uses this
// field both for active flight modes ("ANGL", "ACRO", "NAV",
// "RTH", ...) and for pre-arm status decorations.
//
// Known patterns (collected from INAV source and telemetry
// captures; field testing will refine):
//
//   - Empty / stale: not ready.
//   - Leading '!': pre-arm error or active warning. Examples:
//     "!ERR" (generic), "!FS!" (failsafe), "!HWFAIL", "!RX",
//     "!STR" (stuck stick), "!ACC" (accel cal), etc.
//   - "WAIT" / "WAITING": booting, GPS lock acquiring, etc.
//   - "OK" / "OK*" / "OK!": pre-arm OK, ready to arm.
//   - Active mode names without leading '!': either already
//     armed and flying, or pre-arm OK and showing the mode that
//     would activate on arm. Treated as ready (the arm state
//     machine consults other gates separately).
//
// Conservative posture: any unrecognised string without an
// explicit '!' or "WAIT" prefix is treated as ready, to avoid
// false-blocking arming on a string we haven't catalogued yet.
//
// This is a pure function for testability. Field captures will
// extend the case list as patterns surface.
func fcReadyFromMode(mode string) bool {
	m := strings.TrimSpace(mode)
	if m == "" {
		return false
	}
	if strings.HasPrefix(m, "!") {
		return false
	}
	if strings.HasPrefix(m, "WAIT") {
		return false
	}
	return true
}
