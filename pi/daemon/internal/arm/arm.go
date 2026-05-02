// Package arm implements the GCS-side arming state machine.
//
// The state machine has three states (DISARMED, ARMING_REQUESTED,
// ARMED) and translates physical inputs (arm key, confirm key,
// throttle channel value, FC readiness telemetry) into transitions
// plus events that the daemon consumes for CRSF AUX channel control,
// audio narration, and GUI updates.
//
// Design notes:
//
//   - The state machine is purely synchronous. Transitions happen
//     only inside the input methods (KeyChanged, Confirm, etc).
//     There are no goroutines; the 60s arming-request timeout is
//     driven by Tick() calls from outside.
//
//   - The state machine never "knows" about CRSF, audio, or GPIO.
//     It emits events; the caller maps events to side effects.
//
//   - At construction the state is DISARMED regardless of physical
//     key position. The caller calls Init() once with the actual
//     physical state at boot. If the key is UP, a boot-warning
//     event fires.
//
//   - In ARMING_REQUESTED, telemetry flapping (FC ready-to-arm
//     toggling) does NOT auto-cancel. The operator can confirm only
//     when ready is currently true; a confirm with ready=false is
//     denied. Same for throttle: only blocks the confirm transition,
//     does not change state by itself.
package arm

import (
	"sync"
	"time"
)

// State is the current state of the arming machine.
type State int

const (
	StateDisarmed State = iota
	StateArmingRequested
	StateArmed
)

func (s State) String() string {
	switch s {
	case StateDisarmed:
		return "DISARMED"
	case StateArmingRequested:
		return "ARMING_REQUESTED"
	case StateArmed:
		return "ARMED"
	}
	return "UNKNOWN"
}

// MarshalJSON serializes State as its human string ("DISARMED",
// "ARMING_REQUESTED", "ARMED") so JSON consumers don't have to keep
// in sync with the iota numbering.
func (s State) MarshalJSON() ([]byte, error) {
	return []byte(`"` + s.String() + `"`), nil
}

// Event is something the state machine wants the outside world to
// know about. Events fire as a result of input methods (or Tick).
// The caller drains events from the channel returned by Events().
type Event int

const (
	// EventBootKeyUp fires once if Init is called with keyUp=true.
	// Tells the operator to flip the key down to clear stale state.
	EventBootKeyUp Event = iota

	// EventArmingRequested: DISARMED -> ARMING_REQUESTED.
	EventArmingRequested

	// EventArmingCancelled: ARMING_REQUESTED -> DISARMED via key
	// flip down (operator changed mind).
	EventArmingCancelled

	// EventArmingTimeout: ARMING_REQUESTED -> DISARMED via 60s
	// timeout (operator forgot to confirm).
	EventArmingTimeout

	// EventArmed: ARMING_REQUESTED -> ARMED. Confirm pressed,
	// throttle was zero, FC was ready.
	EventArmed

	// EventArmDeniedThrottle: confirm pressed but throttle != 0.
	// State unchanged (still ARMING_REQUESTED).
	EventArmDeniedThrottle

	// EventArmDeniedNotReady: confirm pressed but FC not ready.
	// State unchanged.
	EventArmDeniedNotReady

	// EventDisarmed: ARMED -> DISARMED via key flip down with
	// throttle zero.
	EventDisarmed

	// EventDisarmDeniedInFlight: ARMED + key flip down + throttle
	// non-zero. State unchanged. Operator must use mushroom or
	// FC failsafes for in-flight aborts.
	EventDisarmDeniedInFlight
)

func (e Event) String() string {
	switch e {
	case EventBootKeyUp:
		return "boot-key-up"
	case EventArmingRequested:
		return "arming-requested"
	case EventArmingCancelled:
		return "arming-cancelled"
	case EventArmingTimeout:
		return "arming-timeout"
	case EventArmed:
		return "armed"
	case EventArmDeniedThrottle:
		return "arm-denied-throttle"
	case EventArmDeniedNotReady:
		return "arm-denied-not-ready"
	case EventDisarmed:
		return "disarmed"
	case EventDisarmDeniedInFlight:
		return "disarm-denied-in-flight"
	}
	return "unknown"
}

// ArmingTimeout is how long ARMING_REQUESTED stays valid before the
// state machine reverts to DISARMED. Configurable per Machine via
// the WithTimeout option.
const DefaultArmingTimeout = 60 * time.Second

// Machine is the arming state machine. Safe for concurrent use; all
// public methods take the internal lock. Events are emitted to a
// buffered channel; if the consumer falls behind, oldest events are
// dropped (with a counter) rather than blocking transitions.
type Machine struct {
	mu sync.Mutex

	state State

	// Cached input snapshots. The state machine doesn't poll for
	// these; callers update them via the input methods.
	keyUp        bool
	throttleZero bool
	fcReady      bool

	// armRequestedAt is the time when state became
	// ARMING_REQUESTED. Used to compute timeout.
	armRequestedAt time.Time

	// Configured.
	timeout time.Duration

	// Output channel. Buffered, drops on overflow.
	events     chan Event
	dropped    int // count of dropped events (for diagnostics)
	dropCounts func(int)
}

// Option configures a Machine at construction.
type Option func(*Machine)

// WithTimeout overrides the default 60s arming timeout.
func WithTimeout(d time.Duration) Option {
	return func(m *Machine) {
		m.timeout = d
	}
}

// WithEventBuffer sets the event channel buffer size. Default is 16.
// Larger buffers tolerate slower consumers; smaller ones make
// dropped-event diagnostics fire sooner.
func WithEventBuffer(n int) Option {
	return func(m *Machine) {
		m.events = make(chan Event, n)
	}
}

// New returns a Machine in StateDisarmed. Call Init exactly once
// after construction with the actual physical key state at boot.
func New(opts ...Option) *Machine {
	m := &Machine{
		state:        StateDisarmed,
		throttleZero: true,
		fcReady:      false,
		timeout:      DefaultArmingTimeout,
		events:       make(chan Event, 16),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Init sets the boot physical state. Call exactly once after New,
// before any other input methods. If keyUp=true, fires
// EventBootKeyUp; the state remains DISARMED.
func (m *Machine) Init(keyUp bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keyUp = keyUp
	if keyUp {
		m.emit(EventBootKeyUp)
	}
}

// State returns the current state.
func (m *Machine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// Events returns the read-only event channel. Drain from a single
// consumer goroutine.
func (m *Machine) Events() <-chan Event {
	return m.events
}

// Dropped returns the number of events dropped due to channel
// backpressure since the Machine was created.
func (m *Machine) Dropped() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dropped
}

// KeyChanged updates the arm key state.
//
// Transitions:
//
//	DISARMED + keyUp=true   -> ARMING_REQUESTED
//	ARMING_REQUESTED + keyUp=false -> DISARMED (cancel)
//	ARMED + keyUp=false + throttleZero=true  -> DISARMED
//	ARMED + keyUp=false + throttleZero=false -> stay (denied event)
func (m *Machine) KeyChanged(keyUp bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.keyUp == keyUp {
		return // no edge
	}
	m.keyUp = keyUp

	switch m.state {
	case StateDisarmed:
		if keyUp {
			m.state = StateArmingRequested
			m.armRequestedAt = time.Now()
			m.emit(EventArmingRequested)
		}
	case StateArmingRequested:
		if !keyUp {
			m.state = StateDisarmed
			m.emit(EventArmingCancelled)
		}
	case StateArmed:
		if !keyUp {
			if m.throttleZero {
				m.state = StateDisarmed
				m.emit(EventDisarmed)
			} else {
				m.emit(EventDisarmDeniedInFlight)
			}
		}
	}
}

// Confirm signals the operator pressed the confirm key combo.
// Only meaningful in ARMING_REQUESTED. Other states ignore.
//
// Transitions (only from ARMING_REQUESTED):
//
//	throttle=0 + ready=true  -> ARMED
//	throttle=0 + ready=false -> stay (EventArmDeniedNotReady)
//	throttle!=0              -> stay (EventArmDeniedThrottle)
//
// Throttle takes precedence over readiness in the denial cue
// because throttle is the simpler, more visceral safety check.
func (m *Machine) Confirm() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != StateArmingRequested {
		return
	}
	if !m.throttleZero {
		m.emit(EventArmDeniedThrottle)
		return
	}
	if !m.fcReady {
		m.emit(EventArmDeniedNotReady)
		return
	}
	m.state = StateArmed
	m.emit(EventArmed)
}

// ThrottleChanged updates the throttle-is-zero flag. Pure cache
// update; does not transition by itself. Confirm and KeyChanged
// (when leaving ARMED) consult it at decision time.
func (m *Machine) ThrottleChanged(zero bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.throttleZero = zero
}

// FCReadyChanged updates the FC ready-to-arm flag from telemetry.
// Pure cache update; does not transition. Confirm consults it.
//
// Telemetry flapping in ARMING_REQUESTED state does not cancel the
// request; the operator decides. If they confirm during a flap-low,
// they get EventArmDeniedNotReady and can retry.
func (m *Machine) FCReadyChanged(ready bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fcReady = ready
}

// Tick advances time. The state machine itself doesn't observe
// real time except through this call; in tests, pass a fake time.
// Call frequency: anywhere between 1 Hz and 100 Hz works. Once a
// second is plenty for the 60s timeout.
//
// In ARMING_REQUESTED, if now - armRequestedAt >= timeout, the
// state reverts to DISARMED with EventArmingTimeout.
func (m *Machine) Tick(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != StateArmingRequested {
		return
	}
	if now.Sub(m.armRequestedAt) >= m.timeout {
		m.state = StateDisarmed
		m.emit(EventArmingTimeout)
	}
}

// Snapshot is a debug-friendly summary of internal state. The
// returned struct is safe to inspect; it doesn't share memory with
// the Machine.
type Snapshot struct {
	State        State `json:"state"`
	KeyUp        bool  `json:"keyUp"`
	ThrottleZero bool  `json:"throttleZero"`
	FCReady      bool  `json:"fcReady"`
	Dropped      int   `json:"dropped"`

	// RequestedAt is when the machine entered ARMING_REQUESTED. Zero
	// in any other state. Useful for clients that want to display
	// the original timestamp rather than the derived remaining.
	RequestedAt time.Time `json:"requestedAt,omitempty"`

	// RemainingSeconds is the time left before the arming-request
	// timeout fires. Zero in any state other than ARMING_REQUESTED;
	// negative is clamped to zero. Computed against time.Now() at
	// the moment Snapshot was taken.
	RemainingSeconds int `json:"remainingSeconds,omitempty"`
}

// Snapshot returns the current state for debugging or GUI display.
func (m *Machine) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := Snapshot{
		State:        m.state,
		KeyUp:        m.keyUp,
		ThrottleZero: m.throttleZero,
		FCReady:      m.fcReady,
		Dropped:      m.dropped,
	}
	if m.state == StateArmingRequested {
		out.RequestedAt = m.armRequestedAt
		remaining := m.timeout - time.Since(m.armRequestedAt)
		if remaining < 0 {
			remaining = 0
		}
		// Round up so a 59.6s remainder reads as 60 to the operator,
		// matching the "60 second timer" mental model.
		out.RemainingSeconds = int((remaining + time.Second - 1) / time.Second)
	}
	return out
}

// emit pushes an event to the events channel. If the channel is
// full, the event is dropped and m.dropped is incremented. Caller
// must hold m.mu.
func (m *Machine) emit(e Event) {
	select {
	case m.events <- e:
	default:
		m.dropped++
	}
}
