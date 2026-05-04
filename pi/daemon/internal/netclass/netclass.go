// Package netclass holds the operator-declared network class for the
// daemon. The daemon does not detect or guess; the operator selects
// one of four classes and other subsystems (tilewarm, future
// stan-sync, etc.) consult the holder to decide what's allowed.
//
// Classes:
//
//   - Home: home network. Cheap internet, LAN access to home server.
//     All operations allowed including bandwidth-heavy syncs.
//   - Free: good but unfamiliar internet (sister's WiFi, café, hotel).
//     Cheap-ish bandwidth, no LAN. Tilewarm OK; no home-server ops.
//   - Field: metered or hostile (phone tether at the club).
//     Skip background fetches; small foreground ops only.
//   - Offline: no network. Skip everything network-bound.
//
// State is persisted to a small JSON file so the daemon remembers
// the operator's last setting across restarts. Default on first run
// is Offline (conservative).
package netclass

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Class is the operator-declared network class.
type Class string

const (
	Home    Class = "home"
	Free    Class = "free"
	Field   Class = "field"
	Offline Class = "offline"
)

// Valid returns true for the four canonical classes. Used by the API
// handler to reject unknown values.
func Valid(c Class) bool {
	switch c {
	case Home, Free, Field, Offline:
		return true
	}
	return false
}

// AllClasses returns the canonical classes in the order they should
// appear in UIs (Home -> Offline). Caller must not mutate.
func AllClasses() []Class {
	return []Class{Home, Free, Field, Offline}
}

// Snapshot is a point-in-time view of the holder.
type Snapshot struct {
	Class    Class     `json:"class"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Holder owns the current class and persists changes to disk. Safe
// for concurrent use; goroutines reading via Current() never block
// each other.
type Holder struct {
	mu        sync.RWMutex
	class     Class
	updatedAt time.Time
	path      string // file backing; empty disables persistence
}

// New constructs a Holder backed by the given file. If the file
// exists and parses, the stored class is loaded. Otherwise the
// holder starts at Offline. An empty path disables persistence;
// the holder lives in memory only.
func New(path string) (*Holder, error) {
	h := &Holder{
		class:     Offline,
		updatedAt: time.Now(),
		path:      path,
	}
	if path == "" {
		return h, nil
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// First run; keep default Offline.
		return h, nil
	case err != nil:
		return nil, fmt.Errorf("netclass: read %q: %w", path, err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("netclass: parse %q: %w", path, err)
	}
	if !Valid(snap.Class) {
		return nil, fmt.Errorf("netclass: invalid class %q in %q", snap.Class, path)
	}
	h.class = snap.Class
	if !snap.UpdatedAt.IsZero() {
		h.updatedAt = snap.UpdatedAt
	}
	return h, nil
}

// Current returns the current class.
func (h *Holder) Current() Class {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.class
}

// Snapshot returns the current class with its update timestamp.
func (h *Holder) Snapshot() Snapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return Snapshot{Class: h.class, UpdatedAt: h.updatedAt}
}

// Set changes the current class and persists to disk. Returns
// ErrInvalidClass if c is not one of the canonical classes.
func (h *Holder) Set(c Class) error {
	if !Valid(c) {
		return ErrInvalidClass
	}
	h.mu.Lock()
	if c == h.class {
		h.mu.Unlock()
		return nil
	}
	h.class = c
	h.updatedAt = time.Now()
	snap := Snapshot{Class: h.class, UpdatedAt: h.updatedAt}
	path := h.path
	h.mu.Unlock()

	if path == "" {
		return nil
	}
	return persist(path, snap)
}

// ErrInvalidClass is returned by Set when given a non-canonical class.
var ErrInvalidClass = errors.New("netclass: invalid class")

// AllowsHomeLAN returns true when the class permits LAN access to the
// home server (recordings sync, log accumulation). Only Home.
func (c Class) AllowsHomeLAN() bool {
	return c == Home
}

// AllowsBackgroundInternet returns true when the class permits
// non-essential internet operations (tilewarm fetches, voice library
// refreshes). Home and Free.
func (c Class) AllowsBackgroundInternet() bool {
	return c == Home || c == Free
}

// AllowsForegroundInternet returns true when the class permits any
// internet operations at all (weather refresh, alert API calls).
// Everything except Offline.
func (c Class) AllowsForegroundInternet() bool {
	return c != Offline
}

// persist writes the snapshot atomically to path, creating parent
// directories as needed.
func persist(path string, snap Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("netclass: mkdir parent %q: %w", path, err)
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("netclass: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("netclass: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("netclass: rename: %w", err)
	}
	return nil
}
