// Package devhealth tracks per-device liveness for the ZeroTX
// ground station. The daemon owns a single Registry; each connected
// device (RP2040 CRSF link, HDMI kiosk displays, Mega IO subsystems,
// ESP32 HUB75 display, etc.) gets one entry. The Registry is read
// from the /api/v1/preflight endpoint and the status page.
//
// Two classes of device:
//
//   - Blocking: the daemon refuses to mark itself flight-ready while
//     any blocking device is not up. Today: the RP2040 CRSF link and
//     the two HDMI kiosk displays. Without these, the operator
//     cannot fly: no CRSF means no link to the FC, and no kiosks
//     means no HUD or map.
//
//   - Informational: tracked and shown to the operator but never
//     blocks flight. The Mega IO and all its subsystems (VFDs, GLCD,
//     buttons, encoder, LEDs, WS strip, LDR, relays) and the ESP32
//     HUB75 panel driver. These are nice-to-haves; an operator who
//     loses (say) one VFD can still fly perfectly well.
//
// Status semantics:
//
//   - "unknown": the device is registered but has never reported in.
//     This is the default state at boot for every device. The
//     daemon hasn't seen evidence either way.
//   - "up":      the device has reported in (Touch'd) within the
//     freshness window (TimeoutSec). Recompute on every Snapshot
//     call so a once-up device demotes to "down" as it goes stale.
//   - "down":    the device WAS up at some point (LastSeen is
//     non-zero) but no Touch within the freshness window. The
//     operator sees the last time it was alive so they can
//     correlate with the issue.
//
// Concurrency: Registry is safe for concurrent use. Register and
// Touch take a write lock; Snapshot takes a read lock and recomputes
// freshness inline (cheap; one time.Since per entry).
package devhealth

import (
	"sort"
	"sync"
	"time"
)

// Status is the device's current health.
type Status string

const (
	StatusUnknown Status = "unknown"
	StatusUp      Status = "up"
	StatusDown    Status = "down"
)

// Kind categorizes a device for UI grouping. The string values are
// used directly in the API and the status page; keep them stable.
type Kind string

const (
	KindRP2040       Kind = "rp2040"        // CRSF link to ELRS module
	KindHDMIDisplay  Kind = "hdmi-display"  // Pi HDMI kiosk display
	KindMega         Kind = "mega"          // Mega IO board (overall)
	KindMegaSubsys   Kind = "mega-subsys"   // Subsystem on the Mega (vfd.0, glcd, button.3, ...)
	KindESP32Display Kind = "esp32-display" // ESP32 HUB75 panel driver
)

// Device is one tracked piece of hardware. Zero value is not useful;
// construct via Registry.Register.
type Device struct {
	Name       string        // unique key, e.g. "rp2040", "hdmi.0", "vfd.0", "esp32-display"
	Kind       Kind          // for UI grouping
	Blocking   bool          // true => preflight Ready waits on this device being up
	Timeout    time.Duration // freshness window; LastSeen older than this demotes to "down"
	LastSeen   time.Time     // last successful Touch; zero means never
	FirstError string        // optional latest error string (set by Touch errs); cleared on next ok Touch
}

// status computes the current Status based on LastSeen and Timeout.
// Reads only; no locking needed.
func (d Device) status(now time.Time) Status {
	if d.LastSeen.IsZero() {
		return StatusUnknown
	}
	if now.Sub(d.LastSeen) <= d.Timeout {
		return StatusUp
	}
	return StatusDown
}

// Registry holds all tracked devices.
type Registry struct {
	mu      sync.RWMutex
	devices map[string]*Device
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{devices: make(map[string]*Device)}
}

// Register adds or replaces a device entry. The new entry starts in
// the unknown state (LastSeen=zero). Safe to call after the registry
// has been read by other goroutines.
//
// If a device with the same Name already exists, it is replaced
// (LastSeen is reset to zero). Register is idempotent in the
// configuration sense — call it at startup for each blocking
// device, and let Touch fill in liveness later.
//
// For auto-discovery use cases where Register would clobber a
// previously-touched liveness state, use EnsureDevice instead.
func (r *Registry) Register(name string, kind Kind, blocking bool, timeout time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.devices[name] = &Device{
		Name:     name,
		Kind:     kind,
		Blocking: blocking,
		Timeout:  timeout,
	}
}

// EnsureDevice registers a device only if one with the given name
// is not already present. Returns true if a new entry was created,
// false if the existing entry was kept.
//
// Used by auto-discovery flows: e.g. the Mega IO board emits
// EVENT lines that name subsystems the daemon hasn't been told
// about explicitly. The first event creates the entry; subsequent
// events Touch it without clobbering its accumulated state. Unlike
// Register, calling EnsureDevice on every event is safe and cheap.
func (r *Registry) EnsureDevice(name string, kind Kind, blocking bool, timeout time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.devices[name]; ok {
		return false
	}
	r.devices[name] = &Device{
		Name:     name,
		Kind:     kind,
		Blocking: blocking,
		Timeout:  timeout,
	}
	return true
}

// Touch updates LastSeen for the named device to now. If the device
// hasn't been Register'd, Touch is a no-op (returns false). This
// makes per-subsystem auto-discovery a one-line: call Register on
// first event, Touch on subsequent events.
//
// Convenience: callers usually Touch() with err==nil (device alive).
// Pass err != nil to record an issue WITHOUT updating LastSeen --
// useful for things like "Send failed, port not open" so the
// operator sees the error reason on the status page.
func (r *Registry) Touch(name string, err error) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[name]
	if !ok {
		return false
	}
	if err == nil {
		d.LastSeen = time.Now()
		d.FirstError = ""
	} else if d.FirstError == "" {
		// Record the FIRST error string until a successful Touch
		// clears it. This avoids the FirstError flapping under a
		// flood of identical errors.
		d.FirstError = err.Error()
	}
	return true
}

// Snapshot is the wire-shape of one device at a moment in time.
// Status is computed from LastSeen + Timeout at snapshot time.
type Snapshot struct {
	Name       string    `json:"name"`
	Kind       Kind      `json:"kind"`
	Blocking   bool      `json:"blocking"`
	Status     Status    `json:"status"`
	LastSeen   time.Time `json:"lastSeen,omitempty"`
	FirstError string    `json:"firstError,omitempty"`
}

// SnapshotAll returns the current state of all registered devices
// at the moment of the call, sorted by Name for deterministic UI.
func (r *Registry) SnapshotAll() []Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	out := make([]Snapshot, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, Snapshot{
			Name:       d.Name,
			Kind:       d.Kind,
			Blocking:   d.Blocking,
			Status:     d.status(now),
			LastSeen:   d.LastSeen,
			FirstError: d.FirstError,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AllBlockingUp reports whether every device with Blocking=true is
// currently Status=up. Returns true if there are no blocking devices
// registered (which is the boot state before commits 2 and 3 wire in
// the RP2040 and HDMI tracking). The /api/v1/preflight aggregator
// uses this to compute its Ready field.
func (r *Registry) AllBlockingUp() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	for _, d := range r.devices {
		if !d.Blocking {
			continue
		}
		if d.status(now) != StatusUp {
			return false
		}
	}
	return true
}

// BlockingDown returns the names of blocking devices currently NOT
// up. Used by the status page to render which device is gating the
// "Proceed to flight" button. Returns an empty slice (non-nil) when
// everything is up. Sorted for deterministic display.
func (r *Registry) BlockingDown() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	out := []string{}
	for _, d := range r.devices {
		if !d.Blocking {
			continue
		}
		if d.status(now) != StatusUp {
			out = append(out, d.Name)
		}
	}
	sort.Strings(out)
	return out
}
