package display

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Manager wraps a Driver with reconnect logic. It owns the lifecycle:
// opens the transport, runs a Driver, reopens on failure, exposes the
// same caller-facing surface as Driver but tolerates "no driver right
// now" by silently dropping calls.
//
// Use this from main.go instead of constructing a Driver directly.
// Manager is what survives across disconnects (cable unplug, ESP32
// reset, etc.); the Driver inside it gets recreated each time.
type Manager struct {
	open       func() (Transport, error)
	driverCfg  Config
	retryDelay time.Duration

	mu      sync.RWMutex
	current *Driver
	mode    Mode
	state   State
	thresh  *Thresholds
	bright  int // -1 = unset
	closed  bool
	onEvent func(Event)
}

// Transport is what the Manager opens to talk to the device. Typically
// a serial port; tests can substitute anything implementing the
// io.ReadWriteCloser surface.
type Transport interface {
	Read(p []byte) (n int, err error)
	Write(p []byte) (n int, err error)
	Close() error
}

// ManagerConfig configures a Manager.
type ManagerConfig struct {
	// Open returns a fresh Transport. Called once per (re)connection.
	// Returning an error causes the Manager to wait RetryDelay and try
	// again. The returned Transport is owned by the Manager: it will
	// be closed when the Driver exits.
	Open func() (Transport, error)

	// DriverConfig is passed verbatim to each Driver the Manager
	// creates.
	DriverConfig Config

	// RetryDelay is how long to wait between reconnect attempts. Zero
	// defaults to 5 seconds.
	RetryDelay time.Duration

	// OnEvent is forwarded to each Driver. Optional.
	OnEvent func(Event)
}

// NewManager constructs a Manager. Run() must be called to start it.
func NewManager(cfg ManagerConfig) *Manager {
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 5 * time.Second
	}
	return &Manager{
		open:       cfg.Open,
		driverCfg:  cfg.DriverConfig,
		retryDelay: cfg.RetryDelay,
		onEvent:    cfg.OnEvent,
		bright:     -1,
	}
}

// Run blocks, owning the reconnect loop until ctx is cancelled.
// Returns nil on graceful shutdown.
func (m *Manager) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		t, err := m.open()
		if err != nil {
			log.Printf("display: open: %v (retry in %s)", err, m.retryDelay)
			if !m.sleep(ctx, m.retryDelay) {
				return nil
			}
			continue
		}
		log.Printf("display: connected")
		d := New(t, m.driverCfg)
		if m.onEvent != nil {
			d.SetEventHandler(m.onEvent)
		}

		// Replay our cached state to the new Driver so it picks up
		// where we left off without callers having to re-issue.
		m.attach(d)

		runErr := d.Run(ctx)
		m.detach()
		if runErr != nil {
			log.Printf("display: driver exited: %v", runErr)
		} else {
			log.Printf("display: driver exited")
		}
		// Don't retry if shutting down.
		if ctx.Err() != nil {
			return nil
		}
		if !m.sleep(ctx, m.retryDelay) {
			return nil
		}
	}
}

// Close marks the Manager closed. The current Driver (if any) is
// closed too; Run unblocks shortly after via ctx cancellation.
// Idempotent.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	d := m.current
	m.mu.Unlock()
	if d != nil {
		return d.Close()
	}
	return nil
}

// === Caller-facing methods (mirror Driver) ===
//
// Each method updates the cached state and forwards to the live
// Driver if there is one. With no driver, calls succeed silently;
// the cache is replayed when a driver next attaches.

func (m *Manager) SetMode(mode Mode) {
	m.mu.Lock()
	m.mode = mode
	d := m.current
	m.mu.Unlock()
	if d != nil {
		d.SetMode(mode)
	}
}

func (m *Manager) SetState(s State) {
	m.mu.Lock()
	mergeState(&m.state, s)
	d := m.current
	m.mu.Unlock()
	if d != nil {
		d.SetState(s)
	}
}

func (m *Manager) SetThresholds(t *Thresholds) {
	m.mu.Lock()
	m.thresh = t
	d := m.current
	m.mu.Unlock()
	if d != nil {
		d.SetThresholds(t)
	}
}

func (m *Manager) SetBrightness(pct int) {
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	m.mu.Lock()
	m.bright = pct
	d := m.current
	m.mu.Unlock()
	if d != nil {
		d.SetBrightness(pct)
	}
}

func (m *Manager) FireAlarm(level AlarmLevel, text string) {
	m.mu.RLock()
	d := m.current
	m.mu.RUnlock()
	if d != nil {
		d.FireAlarm(level, text)
	}
}

func (m *Manager) ClearAlarm() {
	m.mu.RLock()
	d := m.current
	m.mu.RUnlock()
	if d != nil {
		d.ClearAlarm()
	}
}

func (m *Manager) ShowMessage(text string) {
	m.mu.RLock()
	d := m.current
	m.mu.RUnlock()
	if d != nil {
		d.ShowMessage(text)
	}
}

func (m *Manager) Ping() {
	m.mu.RLock()
	d := m.current
	m.mu.RUnlock()
	if d != nil {
		d.Ping()
	}
}

// === Internal ===

// attach wires a freshly-constructed Driver and replays cached state
// so the device is brought up to current truth right after connect.
func (m *Manager) attach(d *Driver) {
	m.mu.Lock()
	m.current = d
	mode := m.mode
	state := m.state
	thresh := m.thresh
	bright := m.bright
	m.mu.Unlock()

	if mode != "" {
		d.SetMode(mode)
	}
	if bright >= 0 {
		d.SetBrightness(bright)
	}
	if thresh != nil {
		d.SetThresholds(thresh)
	}
	d.SetState(state)
}

func (m *Manager) detach() {
	m.mu.Lock()
	m.current = nil
	m.mu.Unlock()
}

// Connected reports whether a Driver is currently attached to the
// Manager. True between attach() (after a successful Open) and the
// next detach() (after Driver.Run returns). Used by devhealth to
// track ESP32 HUB75 panel liveness without exposing the internal
// reconnect-loop state.
//
// This is a sampling check, not a stream: callers poll. The
// underlying lock is held only briefly.
func (m *Manager) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current != nil
}

// sleep blocks for d or until ctx is cancelled. Returns true if the
// sleep completed normally, false if ctx fired (Manager should exit).
func (m *Manager) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// OpenError wraps Open failures; just for type-friendly handling in
// callers that want to distinguish between "couldn't open" vs other
// driver errors.
type OpenError struct{ Err error }

func (e *OpenError) Error() string { return fmt.Sprintf("display: open: %v", e.Err) }
func (e *OpenError) Unwrap() error { return e.Err }
