package recovery

import (
	"sync"
	"testing"
	"time"
)

// stubOperator is a test OperatorSource that returns a configurable
// position. The mutex lets the test mutate the position between
// Manager.State() calls.
type stubOperator struct {
	mu  sync.Mutex
	pos OperatorPosition
}

func (s *stubOperator) OperatorPosition() OperatorPosition {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pos
}

// stubRecorder is a test PreservableRecorder that records every
// PreserveCurrentSession call.
type stubRecorder struct {
	mu      sync.Mutex
	reasons []string
}

func (s *stubRecorder) PreserveCurrentSession(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reasons = append(s.reasons, reason)
}

func (s *stubRecorder) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.reasons))
	copy(out, s.reasons)
	return out
}

func TestNew_IdleByDefault(t *testing.T) {
	m := New(nil, nil)
	s := m.State()
	if s.Active {
		t.Errorf("New().State().Active = true, want false")
	}
	if s.Operator.Source != "none" {
		t.Errorf("Operator.Source = %q, want \"none\" (no source injected)", s.Operator.Source)
	}
}

func TestState_ResolvesOperatorFresh(t *testing.T) {
	op := &stubOperator{pos: OperatorPosition{LatDeg: 1, LonDeg: 2, Source: "gps"}}
	m := New(op, nil)
	if got := m.State().Operator.LatDeg; got != 1 {
		t.Errorf("Operator.LatDeg = %v, want 1", got)
	}
	// Mutate the operator source and confirm next State() sees the change.
	op.mu.Lock()
	op.pos = OperatorPosition{LatDeg: 3, LonDeg: 4, Source: "site"}
	op.mu.Unlock()
	got := m.State().Operator
	if got.LatDeg != 3 || got.LonDeg != 4 || got.Source != "site" {
		t.Errorf("Operator after mutation = %+v, want {3,4,site}", got)
	}
}

func TestTrigger_ActivatesAndSnapshots(t *testing.T) {
	m := New(nil, nil)
	snap := Snapshot{LatDeg: -22.9, LonDeg: -47.06, AltMeters: 180, Mode: "FS", HasGPS: true}
	if !m.Trigger(ReasonFailsafe, snap) {
		t.Fatal("first Trigger returned false, expected true")
	}
	s := m.State()
	if !s.Active {
		t.Error("State.Active = false after Trigger")
	}
	if s.Reason != ReasonFailsafe {
		t.Errorf("State.Reason = %q, want %q", s.Reason, ReasonFailsafe)
	}
	if s.Frozen.AltMeters != 180 {
		t.Errorf("Frozen.AltMeters = %d, want 180", s.Frozen.AltMeters)
	}
	if s.LastKnown == nil || s.LastKnown.LatDeg != -22.9 {
		t.Errorf("LastKnown not initialised from frozen.HasGPS snapshot: %+v", s.LastKnown)
	}
	if s.TriggeredAt.IsZero() {
		t.Error("TriggeredAt zero after Trigger")
	}
}

func TestTrigger_IdempotentWhenActive(t *testing.T) {
	m := New(nil, nil)
	first := Snapshot{LatDeg: 1, LonDeg: 2, HasGPS: true}
	second := Snapshot{LatDeg: 99, LonDeg: 99, HasGPS: true}

	m.Trigger(ReasonFailsafe, first)
	originalT := m.State().TriggeredAt

	// Sleep then re-trigger; second call should be a no-op.
	time.Sleep(10 * time.Millisecond)
	if m.Trigger(ReasonManual, second) {
		t.Error("second Trigger returned true while active, expected false")
	}
	s := m.State()
	if s.Reason != ReasonFailsafe {
		t.Errorf("Reason changed on re-trigger: got %q, want %q", s.Reason, ReasonFailsafe)
	}
	if !s.TriggeredAt.Equal(originalT) {
		t.Errorf("TriggeredAt mutated on re-trigger")
	}
	if s.LastKnown == nil || s.LastKnown.LatDeg != 1 {
		t.Errorf("LastKnown overwritten on re-trigger: %+v", s.LastKnown)
	}
}

func TestTrigger_NoGPSLeavesLastKnownNil(t *testing.T) {
	m := New(nil, nil)
	m.Trigger(ReasonFailsafe, Snapshot{Mode: "FS", HasGPS: false})
	if m.State().LastKnown != nil {
		t.Error("LastKnown set despite HasGPS=false")
	}
}

func TestUpdateLastKnown_OnlyWhenActive(t *testing.T) {
	m := New(nil, nil)

	// Idle: update is a no-op.
	m.UpdateLastKnown(10, 20)
	if m.State().LastKnown != nil {
		t.Error("UpdateLastKnown while idle populated LastKnown")
	}

	// Active: update populates LastKnown.
	m.Trigger(ReasonFailsafe, Snapshot{HasGPS: false})
	m.UpdateLastKnown(10, 20)
	lk := m.State().LastKnown
	if lk == nil {
		t.Fatal("LastKnown nil after UpdateLastKnown while active")
	}
	if lk.LatDeg != 10 || lk.LonDeg != 20 {
		t.Errorf("LastKnown = %+v, want {10,20,...}", lk)
	}

	// Subsequent updates overwrite.
	m.UpdateLastKnown(11, 21)
	lk2 := m.State().LastKnown
	if lk2.LatDeg != 11 || lk2.LonDeg != 21 {
		t.Errorf("LastKnown after second update = %+v, want {11,21,...}", lk2)
	}
}

func TestDismiss_ClearsState(t *testing.T) {
	m := New(nil, nil)
	m.Trigger(ReasonFailsafe, Snapshot{LatDeg: 1, HasGPS: true, Mode: "FS"})
	m.Dismiss()
	s := m.State()
	if s.Active {
		t.Error("State.Active = true after Dismiss")
	}
	if s.LastKnown != nil {
		t.Error("LastKnown not cleared by Dismiss")
	}
	if s.Frozen.Mode != "" {
		t.Errorf("Frozen.Mode = %q, want empty after Dismiss", s.Frozen.Mode)
	}
}

func TestDismiss_AllowsReTrigger(t *testing.T) {
	m := New(nil, nil)
	m.Trigger(ReasonFailsafe, Snapshot{HasGPS: false})
	m.Dismiss()
	if !m.Trigger(ReasonManual, Snapshot{HasGPS: false}) {
		t.Error("Trigger after Dismiss returned false")
	}
	if r := m.State().Reason; r != ReasonManual {
		t.Errorf("Reason after re-trigger = %q, want %q", r, ReasonManual)
	}
}

func TestTrigger_PreservesOnFailsafeOnly(t *testing.T) {
	r := &stubRecorder{}
	m := New(nil, r)

	// Manual trigger: no preserve call.
	m.Trigger(ReasonManual, Snapshot{HasGPS: false})
	if calls := r.calls(); len(calls) != 0 {
		t.Errorf("Manual trigger called PreserveCurrentSession: %v", calls)
	}
	m.Dismiss()

	// Failsafe trigger: one preserve call with reason "failsafe".
	m.Trigger(ReasonFailsafe, Snapshot{HasGPS: false})
	calls := r.calls()
	if len(calls) != 1 || calls[0] != "failsafe" {
		t.Errorf("Failsafe trigger preserve calls = %v, want [failsafe]", calls)
	}
}

func TestTrigger_NoRecorderTolerated(t *testing.T) {
	m := New(nil, nil) // no recorder
	if !m.Trigger(ReasonFailsafe, Snapshot{}) {
		t.Error("Trigger returned false without recorder")
	}
}
