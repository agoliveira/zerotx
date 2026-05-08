// Package heartbeat drives a single GPIO line as a daemon liveness
// indicator. The daemon's main loop calls Tick() at its native rate
// (typically 50Hz). An internal goroutine wakes at the toggle rate
// (default 1Hz) and:
//
//   - if the most recent Tick() is within the freshness window,
//     flips the line state. Result: visible 1Hz blink.
//   - if no Tick has arrived within the freshness window, forces
//     the line low and leaves it there.
//
// The three observable LED states are off (daemon dead, or main loop
// hung past the freshness window), blinking 1Hz (healthy), and (very
// rarely) solid: only possible if the toggle goroutine itself dies
// while the line happens to be high. The package is small enough
// that this last case is mostly theoretical.
//
// Two driver implementations:
//
//   - real: wraps a gpiocdev line (Linux GPIO character device).
//     Use NewReal for production.
//   - null: no-op. Use NewNull when no breakout is wired or when
//     -heartbeat-gpio is left at the disabled default.
//
// Both expose the same surface; the daemon owns one Heartbeat which
// it Tick()s and Close()s without caring which driver is underneath.
package heartbeat

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Driver is the interface to whatever physically drives the LED.
type Driver interface {
	// SetValue writes 0 (low) or 1 (high) to the line. Anything
	// outside that range is treated as 1.
	SetValue(v int) error
	// Close releases the underlying resource.
	Close() error
}

// Config configures a Heartbeat. Zero values pick reasonable defaults
// (1Hz toggle, 1.5s freshness window, time.Now as the clock).
type Config struct {
	// ToggleInterval is the half-period of the blink. The LED
	// changes state every ToggleInterval. A 1Hz blink (visible
	// "alive" indicator) is ToggleInterval = 500ms. Defaults to
	// 500ms when zero.
	ToggleInterval time.Duration

	// Freshness is the maximum age of the last Tick() before the
	// LED is forced low. Pick a value comfortably larger than the
	// expected main-loop period to avoid false positives. Defaults
	// to 1.5s when zero.
	Freshness time.Duration

	// Now returns the current time. Defaults to time.Now. Override
	// in tests.
	Now func() time.Time
}

// Heartbeat is a single-line liveness indicator.
type Heartbeat struct {
	cfg    Config
	drv    Driver
	last   atomic.Int64 // last Tick() time as UnixNano
	stop   chan struct{}
	wg     sync.WaitGroup
	closed atomic.Bool
}

// New constructs a Heartbeat with the given driver and config. The
// caller must call Start to launch the toggle goroutine, then Tick()
// periodically to keep it alive, and Close when shutting down.
func New(drv Driver, cfg Config) *Heartbeat {
	if cfg.ToggleInterval <= 0 {
		cfg.ToggleInterval = 500 * time.Millisecond
	}
	if cfg.Freshness <= 0 {
		cfg.Freshness = 1500 * time.Millisecond
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	h := &Heartbeat{
		cfg:  cfg,
		drv:  drv,
		stop: make(chan struct{}),
	}
	// Seed last so Tick() doesn't need a "first time" branch and
	// the goroutine can run from t=0 with a known reference.
	h.last.Store(cfg.Now().UnixNano())
	return h
}

// Start launches the toggle goroutine. Returns nil unconditionally;
// Driver errors during SetValue are logged via the driver's own
// error path (or swallowed by the null driver).
func (h *Heartbeat) Start() error {
	if h.closed.Load() {
		return errors.New("heartbeat: already closed")
	}
	h.wg.Add(1)
	go h.run()
	return nil
}

// Tick records that the main loop is alive. Cheap (one atomic store);
// safe to call from the hot path. Goroutines other than the main loop
// can also call Tick() without coordination.
func (h *Heartbeat) Tick() {
	h.last.Store(h.cfg.Now().UnixNano())
}

// Close stops the toggle goroutine, forces the line low, and releases
// the underlying driver. Idempotent.
func (h *Heartbeat) Close() error {
	if !h.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(h.stop)
	h.wg.Wait()
	// Ensure we leave the LED in a known-off state on shutdown.
	_ = h.drv.SetValue(0)
	return h.drv.Close()
}

// run is the toggle goroutine. Wakes every ToggleInterval. Decides
// the next line state based on freshness of the last Tick.
func (h *Heartbeat) run() {
	defer h.wg.Done()

	ticker := time.NewTicker(h.cfg.ToggleInterval)
	defer ticker.Stop()

	state := 0 // start low; first wake bumps to 1 if fresh

	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
			lastNS := h.last.Load()
			age := h.cfg.Now().Sub(time.Unix(0, lastNS))
			if age > h.cfg.Freshness {
				// Stale. Force low and stay there until ticks resume.
				if state != 0 {
					state = 0
				}
				_ = h.drv.SetValue(0)
				continue
			}
			// Fresh. Toggle.
			if state == 0 {
				state = 1
			} else {
				state = 0
			}
			_ = h.drv.SetValue(state)
		}
	}
}
