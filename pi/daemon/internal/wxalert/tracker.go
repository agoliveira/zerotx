package wxalert

import (
	"sort"
	"time"
)

// Tracker debounces alert state transitions. Callers feed it the
// instantaneous output of Evaluate at each weather refresh; it
// internally tracks how long each rule has been "above" or "below"
// threshold and only declares a rule active after FireAfter consecutive
// time above, only declares it cleared after ClearAfter consecutive
// time below.
//
// Tracker is single-goroutine; the daemon calls Update from its
// weather refresh goroutine. No internal locking.
//
// Transition events arrive via the return value of Update: a slice
// describing newly-active and newly-cleared rules. Empty slice means
// "no state changes this tick".
type Tracker struct {
	fireAfter  time.Duration
	clearAfter time.Duration

	// state[name] holds the per-rule debounce state.
	state map[string]*ruleState
}

type ruleState struct {
	// active reflects the published state - true when the daemon
	// considers this rule active (notified, displayed).
	active bool
	// firstAboveAt is the wall-clock instant the rule's predicate
	// first transitioned to true while inactive. Zero when the
	// predicate is currently false.
	firstAboveAt time.Time
	// firstBelowAt is the symmetric counterpart for clearing.
	firstBelowAt time.Time
	// lastAlert is the most recent Alert payload from Evaluate; used
	// to populate the Transition message text on activation.
	lastAlert Alert
}

// Transition describes a single rule's published-state change.
type Transition struct {
	// Name is the rule identifier.
	Name string
	// Activated is true for "rule became active", false for "rule
	// became cleared".
	Activated bool
	// Alert is the Alert payload at the time of the transition.
	// For activations this is the most recent payload; for
	// clearings it's the last-known active payload (so the daemon
	// has access to the rule's severity for the audio "all clear"
	// announcement).
	Alert Alert
}

// NewTracker constructs a Tracker with the given hysteresis windows.
// Zero values disable hysteresis (transitions are immediate).
func NewTracker(fireAfter, clearAfter time.Duration) *Tracker {
	return &Tracker{
		fireAfter:  fireAfter,
		clearAfter: clearAfter,
		state:      make(map[string]*ruleState),
	}
}

// DefaultTracker returns a Tracker with the agreed-upon defaults:
// 5 minutes above to fire, 10 minutes below to clear.
func DefaultTracker() *Tracker {
	return NewTracker(5*time.Minute, 10*time.Minute)
}

// Update consumes the latest Evaluate output. Returns the list of
// rules whose published state changed since the last call.
//
// `now` is the wall-clock instant of this tick (production passes
// time.Now(); tests inject deterministic values to exercise hysteresis).
func (t *Tracker) Update(now time.Time, evaluated []Alert) []Transition {
	// Index this tick's alerts by name for lookup.
	current := make(map[string]Alert, len(evaluated))
	for _, a := range evaluated {
		current[a.Name] = a
	}

	// Visit all known rule names (state ∪ current). New rules pick
	// up state on first encounter.
	allNames := make(map[string]struct{}, len(current)+len(t.state))
	for n := range current {
		allNames[n] = struct{}{}
	}
	for n := range t.state {
		allNames[n] = struct{}{}
	}

	var transitions []Transition

	for name := range allNames {
		s := t.state[name]
		if s == nil {
			s = &ruleState{}
			t.state[name] = s
		}
		alert, predicateNow := current[name]
		if predicateNow {
			s.lastAlert = alert
		}

		switch {
		case predicateNow && !s.active:
			// Predicate true while we publish "inactive". Either
			// start counting or fire if the count already exceeds
			// fireAfter.
			if s.firstAboveAt.IsZero() {
				s.firstAboveAt = now
			}
			s.firstBelowAt = time.Time{}
			if now.Sub(s.firstAboveAt) >= t.fireAfter {
				s.active = true
				s.firstAboveAt = time.Time{}
				transitions = append(transitions, Transition{
					Name:      name,
					Activated: true,
					Alert:     alert,
				})
			}
		case !predicateNow && s.active:
			// Predicate false while we publish "active". Either
			// start counting or clear if the count exceeds clearAfter.
			if s.firstBelowAt.IsZero() {
				s.firstBelowAt = now
			}
			s.firstAboveAt = time.Time{}
			if now.Sub(s.firstBelowAt) >= t.clearAfter {
				s.active = false
				s.firstBelowAt = time.Time{}
				transitions = append(transitions, Transition{
					Name:      name,
					Activated: false,
					Alert:     s.lastAlert,
				})
			}
		case predicateNow && s.active:
			// Already active and still above; reset any pending
			// clear count and refresh stored payload.
			s.firstBelowAt = time.Time{}
			s.lastAlert = alert
		case !predicateNow && !s.active:
			// Already inactive and still below; reset any pending
			// fire count.
			s.firstAboveAt = time.Time{}
		}
	}

	// Stable order so logs and tests are deterministic.
	sort.Slice(transitions, func(i, j int) bool {
		return transitions[i].Name < transitions[j].Name
	})
	return transitions
}

// ActiveAlerts returns the currently-published active alerts in name
// order. Used by the daemon to populate the API response and HUD.
func (t *Tracker) ActiveAlerts() []Alert {
	var out []Alert
	for _, s := range t.state {
		if s.active {
			out = append(out, s.lastAlert)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}
