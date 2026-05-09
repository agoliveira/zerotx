// Package syscheck tracks the operator's "I have completed the
// system check" acknowledgement. The kiosk launcher initially opens
// /status on both displays; clicking the page's "Proceed to flight"
// button flips this gate, and the kiosks observe the transition via
// /api/v1/state to navigate themselves to /hud and /map.
//
// Distinct from the daemon's readiness aggregation (the older
// api.Preflight type at /api/v1/preflight), which computes whether
// the daemon thinks the system is ready. This package only tracks
// whether the operator has acknowledged the system-check page.
//
// State is process-local: the daemon starts with the gate undismissed
// and forgets every reboot. That is the desired behavior — a Pi
// reboot brings the operator back to the system check, and they
// either verify and proceed, or click through immediately if they
// were just rebooting after a known fix.
package syscheck

import (
	"sync"
	"time"
)

// Gate is the operator-acknowledgement state. Safe for concurrent use.
type Gate struct {
	mu          sync.RWMutex
	dismissed   bool
	dismissedAt time.Time
}

// New returns a fresh gate in the undismissed state.
func New() *Gate {
	return &Gate{}
}

// Dismiss marks the gate as dismissed and records the wall-clock
// time. Idempotent: a second call leaves the original timestamp
// untouched, so the kiosks see a single transition rather than a
// new "redo" event on every operator click.
func (g *Gate) Dismiss() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.dismissed {
		return
	}
	g.dismissed = true
	g.dismissedAt = time.Now()
}

// Snapshot is the wire-shape consumers see via /api/v1/state.
type Snapshot struct {
	Dismissed   bool      `json:"dismissed"`
	DismissedAt time.Time `json:"dismissedAt,omitempty"`
}

// Snapshot returns the current state. Safe for concurrent use.
func (g *Gate) Snapshot() Snapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := Snapshot{Dismissed: g.dismissed}
	if g.dismissed {
		out.DismissedAt = g.dismissedAt
	}
	return out
}
