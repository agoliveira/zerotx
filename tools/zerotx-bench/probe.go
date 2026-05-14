// Package main contains the zerotx-bench diagnostic tool. See README.md.
package main

import (
	"context"
	"sync"
	"time"
)

// Status is the outcome of a probe run.
type Status string

const (
	// StatusUnknown: probe has never been run (or registry holds no
	// result yet). The UI shows this as a gray dot.
	StatusUnknown Status = "unknown"
	// StatusPass: device responded as expected. Green dot.
	StatusPass Status = "pass"
	// StatusFail: device should be present but isn't, or responded
	// in an unexpected way. Red dot. The Notes/Error fields carry
	// the explanation.
	StatusFail Status = "fail"
	// StatusSkipped: probe was manually skipped via the UI. Yellow
	// dot. Skip persists for the bench-tool session only; restart
	// resets all probes to non-skipped.
	StatusSkipped Status = "skipped"
)

// Result is what a single probe run returns. Details is a free-form
// key/value map intended for display: the UI dumps it in the right-
// hand pane as-is, so probes should pick keys that read naturally to
// a human at the bench.
type Result struct {
	Status  Status            `json:"status"`
	Details map[string]string `json:"details,omitempty"`
	Notes   string            `json:"notes,omitempty"`
	Error   string            `json:"error,omitempty"`
	RanAt   time.Time         `json:"ranAt"`
	Took    time.Duration     `json:"took"`
}

// TestAction is an interactive test the bench operator can fire from
// the UI for a given probe. Buttons in the right-hand pane invoke
// these. Examples: "Blink LED 0", "Beep buzzer 500ms", "Read RTC time".
//
// Run() may take seconds for some tests (audio tone, GPS NMEA capture)
// but should always honor ctx for cancellation. The string return is
// the result text to show in the UI; err signals "the test couldn't
// run cleanly" (separate from "the test ran and found something
// unexpected", which goes in the string).
type TestAction struct {
	ID          string                                                `json:"id"`
	Label       string                                                `json:"label"`
	Description string                                                `json:"description,omitempty"`
	Run         func(ctx context.Context) (output string, err error) `json:"-"`
}

// Prober is the contract every device probe satisfies. Probes are
// registered with the global registry at startup; the web UI sees
// each registered probe in the device list on the left pane.
type Prober interface {
	// ID is a stable lowercase identifier used in URLs. Must be
	// unique across all registered probes. E.g. "mega", "rp2040",
	// "rtc-ds3231", "gps-ublox".
	ID() string

	// Name is the human-readable label shown in the UI. E.g.
	// "Mega 2560 IO board", "DS3231 RTC".
	Name() string

	// Category groups probes in the UI (MCU, USB, I2C, GPIO, RF).
	// Free-form string; the UI orders by category then by name.
	Category() string

	// WiringRef is an optional anchor into docs/hardware-pinout.md
	// (or empty for probes with no wiring like network/storage).
	// Right pane links here as "See wiring".
	WiringRef() string

	// Probe runs the presence/responsiveness check. Returns a
	// Result with Status set. Idempotent: safe to call repeatedly.
	// Should not block longer than 5s; long-running checks belong
	// in TestActions.
	Probe(ctx context.Context) Result

	// Tests returns the interactive test actions for this probe.
	// Empty slice is fine (the joystick has no button-driven test,
	// for instance -- its "test" is moving the sticks and watching
	// the live axis values via Probe).
	Tests() []TestAction
}

// Registry holds all registered probes plus per-session skip state.
// Single instance lives in main(); the HTTP handlers close over it.
// Safe for concurrent use: the web UI fires probes from goroutine-
// per-request, but each probe protects its own internal state.
type Registry struct {
	mu      sync.RWMutex
	probes  []Prober
	results map[string]Result // keyed by Prober.ID()
	skipped map[string]bool   // keyed by Prober.ID()
}

func NewRegistry() *Registry {
	return &Registry{
		results: make(map[string]Result),
		skipped: make(map[string]bool),
	}
}

// Register adds a Prober. Call at startup before serving HTTP. Panics
// on duplicate IDs (programmer error; not a runtime condition).
func (r *Registry) Register(p Prober) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.probes {
		if existing.ID() == p.ID() {
			panic("zerotx-bench: duplicate prober ID " + p.ID())
		}
	}
	r.probes = append(r.probes, p)
}

// List returns a snapshot of registered probes in registration order.
func (r *Registry) List() []Prober {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Prober, len(r.probes))
	copy(out, r.probes)
	return out
}

// Get returns the prober with the given ID, or nil.
func (r *Registry) Get(id string) Prober {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.probes {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

// IsSkipped reports whether a probe is currently marked skipped.
func (r *Registry) IsSkipped(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.skipped[id]
}

// SetSkipped toggles or sets the skip state for a probe.
func (r *Registry) SetSkipped(id string, skip bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skipped[id] = skip
}

// LastResult returns the most recent probe result, or zero-value
// Result if the probe has never been run in this session.
func (r *Registry) LastResult(id string) Result {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.results[id]
}

// RunProbe executes the probe, stores the result, and returns it.
// If the probe is currently marked skipped, returns a StatusSkipped
// result without invoking Probe().
func (r *Registry) RunProbe(ctx context.Context, id string) Result {
	r.mu.RLock()
	skipped := r.skipped[id]
	var prober Prober
	for _, p := range r.probes {
		if p.ID() == id {
			prober = p
			break
		}
	}
	r.mu.RUnlock()

	if prober == nil {
		return Result{Status: StatusFail, Error: "no such probe: " + id, RanAt: time.Now()}
	}
	if skipped {
		res := Result{Status: StatusSkipped, RanAt: time.Now()}
		r.mu.Lock()
		r.results[id] = res
		r.mu.Unlock()
		return res
	}

	start := time.Now()
	res := prober.Probe(ctx)
	res.RanAt = start
	res.Took = time.Since(start)

	r.mu.Lock()
	r.results[id] = res
	r.mu.Unlock()
	return res
}
