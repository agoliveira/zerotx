// Package recovery manages the "lost aircraft" state for the GUI's
// recovery view. When the recovery state is active, the kiosks pivot
// to a recovery-focused presentation: last-known aircraft position,
// frozen telemetry at the moment of loss, bearing/distance from the
// operator's position. The package does not own any of that
// rendering itself; it just holds state and accepts updates.
//
// Two distinct data tracks while active:
//
//   - Frozen snapshot: captured at the moment Trigger() was called.
//     Never updates. Answers "what was happening when this fired?"
//
//   - Last-known position: updates whenever UpdateLastKnown() is
//     called with fresh GPS data. If telemetry comes back briefly
//     during recovery, the operator walks toward the current
//     position, not the trigger position.
//
// Triggering is idempotent: re-triggering while already active is
// a no-op (preserves the original trigger timestamp and frozen
// snapshot). Dismiss returns to idle and clears state. The state
// machine is intentionally simple -- there's no auto-clear; the
// operator decides when recovery is over.
package recovery

import (
	"sync"
	"time"
)

// Reason is why the recovery state was activated. Distinguishes
// daemon-triggered (failsafe) from operator-triggered (manual) so
// the GUI can phrase its presentation differently.
type Reason string

const (
	ReasonFailsafe Reason = "failsafe"
	ReasonManual   Reason = "manual"
)

// Snapshot is the telemetry-derived state captured at the moment
// the recovery view was triggered. Never updates after trigger;
// used to answer "what was happening when this fired?"
//
// HasGPS indicates whether the lat/lon/alt fields are meaningful
// at trigger time. If false, the operator has no aircraft-side
// position fix to walk to (the daemon's last-known-position is
// stale or absent).
type Snapshot struct {
	LatDeg     float64 `json:"latDeg"`
	LonDeg     float64 `json:"lonDeg"`
	AltMeters  int32   `json:"altMeters"`
	GroundKmh  float64 `json:"groundKmh"`
	HeadingDeg float64 `json:"headingDeg"`
	Mode       string  `json:"mode"`
	HasGPS     bool    `json:"hasGPS"`
}

// Position is a lat/lon observed at a specific time. Used for
// LastKnown updates that flow in after Trigger if telemetry briefly
// recovers.
type Position struct {
	LatDeg float64   `json:"latDeg"`
	LonDeg float64   `json:"lonDeg"`
	At     time.Time `json:"at"`
}

// OperatorPosition is the recovery view's "where I am standing"
// data, resolved fresh on every State() read. Source explains
// where it came from so the GUI can surface fallback warnings
// (configured site vs live GPS).
type OperatorPosition struct {
	LatDeg float64 `json:"latDeg"`
	LonDeg float64 `json:"lonDeg"`
	Source string  `json:"source"` // "gps" | "site" | "none"
}

// State is the JSON-shaped representation of the recovery state
// machine. Marshalled into the API stream by the daemon's API
// server.
type State struct {
	Active      bool             `json:"active"`
	Reason      Reason           `json:"reason,omitempty"`
	TriggeredAt time.Time        `json:"triggeredAt,omitempty"`
	Frozen      Snapshot         `json:"frozen"`
	LastKnown   *Position        `json:"lastKnown,omitempty"`
	Operator    OperatorPosition `json:"operator"`
}

// OperatorSource resolves the operator's current position on
// demand. Implementations are expected to be cheap (return cached
// state, no I/O).
type OperatorSource interface {
	OperatorPosition() OperatorPosition
}

// PreservableRecorder is the optional hook into the recorder. When
// recovery activates for ReasonFailsafe, the manager calls
// PreserveCurrentSession so the recording survives the post-disarm
// cleanup-on-rotate. Manual triggers do NOT preserve -- the operator
// chose to enter recovery view but the flight wasn't necessarily
// lost.
type PreservableRecorder interface {
	PreserveCurrentSession(reason string)
}

// Manager owns the recovery state machine. Safe for concurrent use
// from any goroutine.
type Manager struct {
	mu       sync.RWMutex
	state    State
	operator OperatorSource
	recorder PreservableRecorder
}

// New constructs a Manager. Both op and rec are optional; nil values
// mean "no operator position available" (Operator.Source = "none")
// and "don't preserve recordings on failsafe."
func New(op OperatorSource, rec PreservableRecorder) *Manager {
	return &Manager{
		operator: op,
		recorder: rec,
	}
}

// State returns the current state. Operator position is resolved
// fresh at every call so the GUI sees the operator move (Pi-side
// GPS updating between calls).
func (m *Manager) State() State {
	m.mu.RLock()
	s := m.state
	m.mu.RUnlock()
	if m.operator != nil {
		s.Operator = m.operator.OperatorPosition()
	} else {
		s.Operator = OperatorPosition{Source: "none"}
	}
	return s
}

// IsActive returns true when the recovery state machine is active.
// Cheap (single mutex acquire + bool read); intended for hot-path
// guards in telemetry handlers that need to skip work when recovery
// is idle. Use this instead of State().Active when you don't need
// the full state payload.
func (m *Manager) IsActive() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.Active
}

// Trigger activates the recovery view with the given reason and
// frozen snapshot. Idempotent: re-triggering while already active
// is a no-op (preserves the original timestamp and snapshot).
//
// Returns true if the call actually transitioned the state from
// idle to active (caller can log / emit events on first activation
// only). Returns false if the manager was already active.
//
// For ReasonFailsafe, calls PreserveCurrentSession on the recorder
// (if one was injected) so the in-progress recording survives the
// next save-and-rotate.
func (m *Manager) Trigger(reason Reason, frozen Snapshot) bool {
	m.mu.Lock()
	if m.state.Active {
		m.mu.Unlock()
		return false
	}
	now := time.Now()
	m.state = State{
		Active:      true,
		Reason:      reason,
		TriggeredAt: now,
		Frozen:      frozen,
	}
	if frozen.HasGPS {
		p := Position{LatDeg: frozen.LatDeg, LonDeg: frozen.LonDeg, At: now}
		m.state.LastKnown = &p
	}
	rec := m.recorder
	m.mu.Unlock()

	if rec != nil && reason == ReasonFailsafe {
		rec.PreserveCurrentSession(string(reason))
	}
	return true
}

// UpdateLastKnown is called by the telemetry consumer whenever
// fresh GPS arrives during an active recovery. No-op when idle.
// The first call after Trigger may overwrite the snapshot-derived
// last-known with the same position; subsequent calls track the
// aircraft as it drifts or is brought back under partial control.
func (m *Manager) UpdateLastKnown(latDeg, lonDeg float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.state.Active {
		return
	}
	p := Position{LatDeg: latDeg, LonDeg: lonDeg, At: time.Now()}
	m.state.LastKnown = &p
}

// Dismiss clears the recovery state. No-op when already idle.
// The frozen snapshot is discarded; the API state returns to
// Active=false with zero-valued sub-fields.
func (m *Manager) Dismiss() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = State{}
}
