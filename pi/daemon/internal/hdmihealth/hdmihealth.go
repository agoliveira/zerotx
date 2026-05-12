// Package hdmihealth detects the connection state of HDMI displays
// attached to the Pi by reading the Linux DRM subsystem's sysfs
// entries. Used by the devhealth registry to gate flight on both
// kiosk displays being attached.
//
// On the Pi 400, the two micro-HDMI outputs appear as
// /sys/class/drm/card1-HDMI-A-1 and /sys/class/drm/card1-HDMI-A-2.
// On other Pi variants the card number may differ; the glob
// /sys/class/drm/card*-HDMI-* matches whichever DRM card the kernel
// chose. Each connector directory has a 'status' file whose content
// is "connected\n" or "disconnected\n" -- the canonical signal we
// trust for "an EDID-emitting sink is on the other end of the cable".
package hdmihealth

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultPattern matches every HDMI connector exposed by every DRM
// card on the Pi. Use a different pattern in tests pointing at a
// fixture directory.
const DefaultPattern = "/sys/class/drm/card*-HDMI-*"

// Result is the outcome of one scan call.
type Result struct {
	// Connected is the number of HDMI connectors whose status file
	// reports "connected" at the moment of the call.
	Connected int
	// Total is the number of HDMI connectors found by the glob,
	// regardless of their status.
	Total int
	// Detail is one short line per connector: "card1-HDMI-A-1:
	// connected" or "card1-HDMI-A-2: disconnected". Sorted by name.
	// Useful for the devhealth FirstError field and for logs.
	Detail []string
}

// Scan walks every path matching the glob pattern and reads its
// 'status' file. Returns the count of connected vs total connectors
// plus a per-connector detail list.
//
// Error semantics:
//   - A glob error (malformed pattern) returns a non-nil error and
//     a zero-value Result.
//   - Per-connector read failures are not propagated as errors --
//     the connector counts toward Total but not toward Connected,
//     and Detail records it as "<name>: <read-error>". This
//     matches how an operator would interpret a broken sysfs entry:
//     "it's there, but I can't tell its state, so assume not up".
//   - No matches found returns a zero Result and nil error. This
//     is normal on a desktop dev machine without HDMI; the daemon
//     gracefully reports zero connected and the device stays down.
func Scan(pattern string) (Result, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return Result{}, err
	}
	out := Result{Total: len(matches)}
	for _, dir := range matches {
		name := filepath.Base(dir)
		statusPath := filepath.Join(dir, "status")
		b, rerr := os.ReadFile(statusPath)
		if rerr != nil {
			out.Detail = append(out.Detail, name+": read error: "+rerr.Error())
			continue
		}
		s := strings.TrimSpace(string(b))
		if s == "connected" {
			out.Connected++
		}
		out.Detail = append(out.Detail, name+": "+s)
	}
	return out, nil
}
