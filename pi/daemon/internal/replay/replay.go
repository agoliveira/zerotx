// Package replay holds the daemon-side "is a replay session active"
// state. The replay UI is client-side (the /hud and /map pages
// fetch recordings and run their own playback clocks), but the
// daemon needs to know a replay is happening so it can:
//
//   - Switch the HUB75 panel to display.ModeReplay (so the panel
//     doesn't show stale flight state during a replay).
//   - Refuse to enter replay while the aircraft is armed (safety
//     gate: replay should never run during a real flight).
//   - Provide kiosks the "we should be replaying recording X"
//     signal so a fresh kiosk WS connect doesn't ignore an
//     ongoing replay (commit 2 work; this commit only stores).
//
// State is process-local and resets on daemon restart. The replay
// itself runs entirely in browser tabs; killing the daemon mid-
// replay just clears the panel mode, the tabs keep playing.
package replay

import (
	"sync"
	"time"
)

// State is the small mutable bag of "replay is active" facts.
// Safe for concurrent use; all methods take the internal lock.
type State struct {
	mu     sync.RWMutex
	active bool
	name   string
	since  time.Time
}

// New returns an idle State.
func New() *State {
	return &State{}
}

// Snapshot is the read-only view callers (API handlers, dashboards)
// consume. Returned by value so the caller can't accidentally race
// on the State's internal lock by holding a pointer.
type Snapshot struct {
	Active    bool      `json:"active"`
	Name      string    `json:"name,omitempty"`
	StartedAt time.Time `json:"startedAt,omitempty"`
}

// Snapshot returns the current state in a value-typed form.
func (s *State) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot{Active: s.active, Name: s.name, StartedAt: s.since}
}

// Start marks a replay session as active for the named recording.
// Returns false if a replay was already active (idempotent on the
// same name; conflict on a different name). The caller -- typically
// the /api/v1/replay/start handler -- pairs this with the side
// effects (flipping display mode, notifying kiosks).
func (s *State) Start(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active && s.name != name {
		return false
	}
	s.active = true
	s.name = name
	s.since = time.Now()
	return true
}

// Stop clears the replay session. Returns true if a session was
// active and is now cleared, false if there was nothing to stop.
func (s *State) Stop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return false
	}
	s.active = false
	s.name = ""
	s.since = time.Time{}
	return true
}
