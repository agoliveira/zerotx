// Package panel exposes the GCS control panel state (switches, selectors,
// buttons) to the rest of the daemon through a single interface. Real
// hardware backends (over USB-CDC from the RP2040) and synthetic backends
// (file-driven, stdin REPL) implement the same Panel interface so the
// mapper, logic engine, and tests don't care where the state comes from.
//
// EdgeTX naming convention is preserved:
//
//	SA, SB, SC, SE, SF, SG, SH ... 3-position toggles, value 0..2
//	6POS                           rotary selector, value 0..15
//	Buttons (custom names)         momentary, true while pressed
//
// Backends:
//
//	NullPanel       all positions zero, all buttons released
//	FilePanel       reads a YAML file, polls mtime for live reload
//	StdinPanel      reads "NAME VALUE" lines on stdin (REPL)
//	IPCPanel        future: ingests INPUT_STATE frames from RP2040
package panel

import (
	"strings"
	"sync"
	"time"
)

// Panel is the read-side interface used by the rest of the daemon. All
// methods must be safe for concurrent use.
type Panel interface {
	// Switch returns the position of a multi-position toggle (0..N-1).
	// ok=false if the switch isn't defined for this panel.
	Switch(name string) (pos int, ok bool)

	// Selector returns the position of a rotary selector (0..15).
	Selector(name string) (pos int, ok bool)

	// Button returns whether a momentary button is currently pressed.
	Button(name string) (pressed bool, ok bool)

	// LastUpdate returns the timestamp of the last state change. Useful
	// for staleness checks (e.g. log a warning if the panel hasn't
	// updated in N seconds while the file backend is in use).
	LastUpdate() time.Time

	// Snapshot returns a full copy of the panel state. Used by the API
	// layer for state broadcasts.
	Snapshot() Snapshot
}

// Snapshot is a complete read of panel state at a moment in time.
type Snapshot struct {
	Switches  map[string]int  `json:"switches"`
	Selectors map[string]int  `json:"selectors"`
	Buttons   map[string]bool `json:"buttons"`
}

// normalizeName returns the canonical key for a switch/selector/button name.
// Stored and queried names are uppercased so "se", "Se", and "SE" all hit
// the same slot. EdgeTX uses uppercase by convention.
func normalizeName(s string) string {
	return strings.ToUpper(s)
}

// state is the shared mutable state for backends that store everything
// in a single struct. Backends embed this and use the helpers below.
type state struct {
	mu        sync.RWMutex
	switches  map[string]int
	selectors map[string]int
	buttons   map[string]bool
	updated   time.Time
}

func newState() state {
	return state{
		switches:  make(map[string]int),
		selectors: make(map[string]int),
		buttons:   make(map[string]bool),
		updated:   time.Now(),
	}
}

func (s *state) getSwitch(name string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.switches[normalizeName(name)]
	return v, ok
}

func (s *state) getSelector(name string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.selectors[normalizeName(name)]
	return v, ok
}

func (s *state) getButton(name string) (bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.buttons[normalizeName(name)]
	return v, ok
}

func (s *state) lastUpdate() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updated
}

// snapshot returns a deep copy of the state. Each map is copied so the
// caller can mutate freely without races against subsequent set/replace
// calls.
func (s *state) snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Snapshot{
		Switches:  make(map[string]int, len(s.switches)),
		Selectors: make(map[string]int, len(s.selectors)),
		Buttons:   make(map[string]bool, len(s.buttons)),
	}
	for k, v := range s.switches {
		out.Switches[k] = v
	}
	for k, v := range s.selectors {
		out.Selectors[k] = v
	}
	for k, v := range s.buttons {
		out.Buttons[k] = v
	}
	return out
}

func (s *state) replace(switches map[string]int, selectors map[string]int, buttons map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.switches = make(map[string]int, len(switches))
	for k, v := range switches {
		s.switches[normalizeName(k)] = v
	}
	s.selectors = make(map[string]int, len(selectors))
	for k, v := range selectors {
		s.selectors[normalizeName(k)] = v
	}
	s.buttons = make(map[string]bool, len(buttons))
	for k, v := range buttons {
		s.buttons[normalizeName(k)] = v
	}
	s.updated = time.Now()
}

func (s *state) setSwitch(name string, pos int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.switches[normalizeName(name)] = pos
	s.updated = time.Now()
}

func (s *state) setSelector(name string, pos int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selectors[normalizeName(name)] = pos
	s.updated = time.Now()
}

func (s *state) setButton(name string, pressed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buttons[normalizeName(name)] = pressed
	s.updated = time.Now()
}

// NullPanel is the default backend when no panel source is configured.
// All switches / selectors / buttons return ok=false; the mapper falls
// back to its safe defaults.
type NullPanel struct{}

// Switch returns 0, false.
func (NullPanel) Switch(string) (int, bool) { return 0, false }

// Selector returns 0, false.
func (NullPanel) Selector(string) (int, bool) { return 0, false }

// Button returns false, false.
func (NullPanel) Button(string) (bool, bool) { return false, false }

// LastUpdate returns the zero time.
func (NullPanel) LastUpdate() time.Time { return time.Time{} }

// Snapshot returns an empty snapshot.
func (NullPanel) Snapshot() Snapshot {
	return Snapshot{
		Switches:  map[string]int{},
		Selectors: map[string]int{},
		Buttons:   map[string]bool{},
	}
}
