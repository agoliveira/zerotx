package replay

import (
	"testing"
	"time"
)

func TestNew_StartsIdle(t *testing.T) {
	s := New()
	if snap := s.Snapshot(); snap.Active {
		t.Errorf("new replay state should be idle, got active=true")
	}
}

func TestStart_ActivatesAndRecordsName(t *testing.T) {
	s := New()
	if ok := s.Start("flight-42.db"); !ok {
		t.Fatalf("Start on idle state should return true")
	}
	snap := s.Snapshot()
	if !snap.Active {
		t.Errorf("Active should be true after Start")
	}
	if snap.Name != "flight-42.db" {
		t.Errorf("Name: got %q, want %q", snap.Name, "flight-42.db")
	}
	if snap.StartedAt.IsZero() {
		t.Errorf("StartedAt should be set after Start")
	}
}

// TestStart_SameNameIsIdempotent: starting twice with the same name
// is fine. The 'started' timestamp moves forward (we record the most
// recent start), which matches the intuition that 'start replay X'
// is the only-thing-that-matters semantic.
func TestStart_SameNameIsIdempotent(t *testing.T) {
	s := New()
	s.Start("a.db")
	first := s.Snapshot().StartedAt
	time.Sleep(5 * time.Millisecond)
	if ok := s.Start("a.db"); !ok {
		t.Errorf("Start with same name should succeed")
	}
	second := s.Snapshot().StartedAt
	if !second.After(first) {
		t.Errorf("repeat Start should update StartedAt; got %v then %v", first, second)
	}
}

// TestStart_DifferentNameConflicts: changing recording while a
// replay is active requires an explicit Stop first. Prevents two
// sources of truth in the UI ("we're showing X, but the daemon
// thinks Y is playing").
func TestStart_DifferentNameConflicts(t *testing.T) {
	s := New()
	s.Start("a.db")
	if ok := s.Start("b.db"); ok {
		t.Errorf("Start with different name should conflict")
	}
	if snap := s.Snapshot(); snap.Name != "a.db" {
		t.Errorf("Name should remain a.db, got %q", snap.Name)
	}
}

func TestStop_DeactivatesAndClears(t *testing.T) {
	s := New()
	s.Start("a.db")
	if ok := s.Stop(); !ok {
		t.Errorf("Stop on active state should return true")
	}
	snap := s.Snapshot()
	if snap.Active {
		t.Errorf("Active should be false after Stop")
	}
	if snap.Name != "" {
		t.Errorf("Name should be cleared after Stop, got %q", snap.Name)
	}
}

func TestStop_IdleNoop(t *testing.T) {
	s := New()
	if ok := s.Stop(); ok {
		t.Errorf("Stop on idle state should return false")
	}
}
