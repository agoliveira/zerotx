// Package logic implements the EdgeTX logical-switch engine.
//
// The engine owns per-switch state (sticky latches, edge timers, delay/
// duration timers, prev-tick value cache for delta functions) and the
// previous-tick output bitmap. Each Tick() call evaluates every defined
// L# in the model: parse def, evaluate the function, apply the andsw
// modifier, then delay, then duration, store result.
//
// The resolver reads logic switch state via the engine's Logic() method
// during evaluation. Within a tick, all reads see the PREVIOUS tick's
// state, eliminating dependency cycles between L# definitions at the
// cost of one tick of latency on L→L chains. EdgeTX does the same.
//
// Time-sensitive functions (EDGE, STICKY, TIMER, DIFFEGREATER,
// ADIFFEGREATER) and time-based modifiers (delay, duration) read from
// the engine's Clock interface, which is mockable for tests.
package logic

import "time"

// Clock returns the current time. RealClock wraps time.Now; tests use
// FakeClock to drive the engine deterministically.
type Clock interface {
	Now() time.Time
}

// RealClock returns wall-clock time.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// FakeClock is a manually-advanced clock for tests.
type FakeClock struct {
	t time.Time
}

// NewFakeClock returns a FakeClock starting at a fixed reference time.
// Using a non-zero start avoids edge cases around the zero Time value.
func NewFakeClock() *FakeClock {
	return &FakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// Now implements Clock.
func (c *FakeClock) Now() time.Time { return c.t }

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// AdvanceTo sets the clock to t (must be >= current time).
func (c *FakeClock) AdvanceTo(t time.Time) { c.t = t }

// dsToDuration converts EdgeTX's 0.1-second integer units (the YAML
// encoding for delay/duration/EDGE-T1/EDGE-T2/TIMER on/off times) to
// time.Duration. dsToDuration(10) -> 1.0 second.
func dsToDuration(deciseconds int) time.Duration {
	return time.Duration(deciseconds) * 100 * time.Millisecond
}
