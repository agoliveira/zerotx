package arm

import (
	"testing"
	"time"
)

// drain reads up to maxN events without blocking. Returns the slice
// of what was drained. Used by tests that want to assert exact
// event sequences.
func drain(m *Machine, maxN int) []Event {
	out := []Event{}
	for i := 0; i < maxN; i++ {
		select {
		case e := <-m.Events():
			out = append(out, e)
		default:
			return out
		}
	}
	return out
}

func TestNew_StartsDisarmed(t *testing.T) {
	m := New()
	if m.State() != StateDisarmed {
		t.Errorf("expected StateDisarmed, got %v", m.State())
	}
}

func TestInit_KeyDownNoBootEvent(t *testing.T) {
	m := New()
	m.Init(false) // key down at boot
	if got := drain(m, 5); len(got) != 0 {
		t.Errorf("expected no boot event, got %v", got)
	}
	if m.State() != StateDisarmed {
		t.Errorf("expected DISARMED after key-down init, got %v", m.State())
	}
}

func TestInit_KeyUpEmitsBootWarning(t *testing.T) {
	m := New()
	m.Init(true) // key up at boot
	got := drain(m, 5)
	if len(got) != 1 || got[0] != EventBootKeyUp {
		t.Errorf("expected [EventBootKeyUp], got %v", got)
	}
	// State must remain DISARMED despite the key being up.
	if m.State() != StateDisarmed {
		t.Errorf("expected DISARMED after key-up init, got %v", m.State())
	}
}

func TestKeyChanged_DisarmedToArmingRequested(t *testing.T) {
	m := New()
	m.Init(false)
	m.KeyChanged(true)
	if m.State() != StateArmingRequested {
		t.Errorf("expected ARMING_REQUESTED, got %v", m.State())
	}
	got := drain(m, 5)
	if len(got) != 1 || got[0] != EventArmingRequested {
		t.Errorf("expected [arming-requested], got %v", got)
	}
}

func TestKeyChanged_NoEdgeNoTransition(t *testing.T) {
	m := New()
	m.Init(false)
	m.KeyChanged(false) // already false
	if got := drain(m, 5); len(got) != 0 {
		t.Errorf("expected no events on no-edge, got %v", got)
	}
	if m.State() != StateDisarmed {
		t.Errorf("expected DISARMED, got %v", m.State())
	}
}

func TestKeyChanged_ArmingCancel(t *testing.T) {
	m := New()
	m.Init(false)
	m.KeyChanged(true)  // request
	m.KeyChanged(false) // cancel
	if m.State() != StateDisarmed {
		t.Errorf("expected DISARMED after cancel, got %v", m.State())
	}
	got := drain(m, 5)
	if len(got) != 2 || got[0] != EventArmingRequested || got[1] != EventArmingCancelled {
		t.Errorf("expected [requested, cancelled], got %v", got)
	}
}

func TestConfirm_HappyPath(t *testing.T) {
	m := New()
	m.Init(false)
	m.ThrottleChanged(true)
	m.FCReadyChanged(true)
	m.KeyChanged(true) // ARMING_REQUESTED
	m.Confirm()        // should arm
	if m.State() != StateArmed {
		t.Errorf("expected ARMED, got %v", m.State())
	}
	got := drain(m, 5)
	if len(got) != 2 || got[0] != EventArmingRequested || got[1] != EventArmed {
		t.Errorf("expected [requested, armed], got %v", got)
	}
}

func TestConfirm_DeniedThrottle(t *testing.T) {
	m := New()
	m.Init(false)
	m.ThrottleChanged(false) // throttle non-zero
	m.FCReadyChanged(true)
	m.KeyChanged(true)
	m.Confirm()
	if m.State() != StateArmingRequested {
		t.Errorf("state should be unchanged: ARMING_REQUESTED, got %v", m.State())
	}
	got := drain(m, 5)
	if len(got) != 2 || got[0] != EventArmingRequested || got[1] != EventArmDeniedThrottle {
		t.Errorf("expected [requested, denied-throttle], got %v", got)
	}
}

func TestConfirm_DeniedNotReady(t *testing.T) {
	m := New()
	m.Init(false)
	m.ThrottleChanged(true)
	m.FCReadyChanged(false) // FC not ready
	m.KeyChanged(true)
	m.Confirm()
	if m.State() != StateArmingRequested {
		t.Errorf("state should be unchanged: ARMING_REQUESTED, got %v", m.State())
	}
	got := drain(m, 5)
	if len(got) != 2 || got[0] != EventArmingRequested || got[1] != EventArmDeniedNotReady {
		t.Errorf("expected [requested, denied-not-ready], got %v", got)
	}
}

func TestConfirm_ThrottleTakesPrecedenceOverReady(t *testing.T) {
	// Both throttle non-zero AND not-ready should produce the
	// throttle denial cue (more visceral, simpler check).
	m := New()
	m.Init(false)
	m.ThrottleChanged(false)
	m.FCReadyChanged(false)
	m.KeyChanged(true)
	m.Confirm()
	got := drain(m, 5)
	if len(got) != 2 || got[1] != EventArmDeniedThrottle {
		t.Errorf("expected throttle denial (precedence), got %v", got)
	}
}

func TestConfirm_IgnoredFromDisarmed(t *testing.T) {
	m := New()
	m.Init(false)
	m.ThrottleChanged(true)
	m.FCReadyChanged(true)
	m.Confirm() // no key request first
	if m.State() != StateDisarmed {
		t.Errorf("expected DISARMED, got %v", m.State())
	}
	if got := drain(m, 5); len(got) != 0 {
		t.Errorf("confirm from DISARMED should be silent, got %v", got)
	}
}

func TestConfirm_IgnoredFromArmed(t *testing.T) {
	m := New()
	m.Init(false)
	m.ThrottleChanged(true)
	m.FCReadyChanged(true)
	m.KeyChanged(true)
	m.Confirm()
	drain(m, 5) // discard arming events
	m.Confirm() // already armed, second confirm is a no-op
	if got := drain(m, 5); len(got) != 0 {
		t.Errorf("confirm from ARMED should be silent, got %v", got)
	}
}

func TestKeyChanged_DisarmFromArmedOnGround(t *testing.T) {
	m := New()
	m.Init(false)
	m.ThrottleChanged(true)
	m.FCReadyChanged(true)
	m.KeyChanged(true)
	m.Confirm() // ARMED
	drain(m, 5)
	// throttle still zero (on ground); flip key down -> disarm.
	m.KeyChanged(false)
	if m.State() != StateDisarmed {
		t.Errorf("expected DISARMED, got %v", m.State())
	}
	got := drain(m, 5)
	if len(got) != 1 || got[0] != EventDisarmed {
		t.Errorf("expected [disarmed], got %v", got)
	}
}

func TestKeyChanged_DisarmFromArmedInFlight(t *testing.T) {
	m := New()
	m.Init(false)
	m.ThrottleChanged(true)
	m.FCReadyChanged(true)
	m.KeyChanged(true)
	m.Confirm()
	drain(m, 5)
	// throttle now non-zero (in flight); flip key down -> denied.
	m.ThrottleChanged(false)
	m.KeyChanged(false)
	if m.State() != StateArmed {
		t.Errorf("state should still be ARMED in flight, got %v", m.State())
	}
	got := drain(m, 5)
	if len(got) != 1 || got[0] != EventDisarmDeniedInFlight {
		t.Errorf("expected [disarm-denied-in-flight], got %v", got)
	}
}

func TestTick_ArmingTimeout(t *testing.T) {
	m := New(WithTimeout(60 * time.Second))
	m.Init(false)
	t0 := time.Now()
	m.KeyChanged(true)
	// Force armRequestedAt to a known value via reflection-free path:
	// just call Tick with t0+59s -> no timeout, then t0+60s -> timeout.
	m.Tick(t0.Add(59 * time.Second))
	if m.State() != StateArmingRequested {
		t.Errorf("expected ARMING_REQUESTED at 59s, got %v", m.State())
	}
	// Arm-requested-at is set inside KeyChanged using time.Now(),
	// not our t0, so we have to pass an absolute time that's
	// >= armRequestedAt + 60s. Easiest: ask for snapshot, use that.
	snap := m.Snapshot()
	_ = snap // we don't expose armRequestedAt; instead use a much
	// later wall-clock time to guarantee >= 60s elapsed regardless.
	m.Tick(time.Now().Add(120 * time.Second))
	if m.State() != StateDisarmed {
		t.Errorf("expected DISARMED after timeout, got %v", m.State())
	}
	got := drain(m, 5)
	// Sequence so far: requested, then timeout.
	if len(got) != 2 || got[0] != EventArmingRequested || got[1] != EventArmingTimeout {
		t.Errorf("expected [requested, timeout], got %v", got)
	}
}

func TestTick_NoTimeoutFromOtherStates(t *testing.T) {
	m := New(WithTimeout(1 * time.Millisecond))
	m.Init(false)
	// DISARMED: tick should do nothing.
	m.Tick(time.Now().Add(time.Hour))
	if m.State() != StateDisarmed {
		t.Errorf("DISARMED + tick should stay DISARMED, got %v", m.State())
	}
	// ARMED: tick should do nothing.
	m.ThrottleChanged(true)
	m.FCReadyChanged(true)
	m.KeyChanged(true)
	m.Confirm()
	drain(m, 5)
	m.Tick(time.Now().Add(time.Hour))
	if m.State() != StateArmed {
		t.Errorf("ARMED + tick should stay ARMED, got %v", m.State())
	}
}

func TestTelemetryFlapping_DoesNotAutoCancel(t *testing.T) {
	m := New()
	m.Init(false)
	m.ThrottleChanged(true)
	m.FCReadyChanged(true)
	m.KeyChanged(true) // ARMING_REQUESTED
	// Now FC reports not-ready (e.g., GPS lock blip).
	m.FCReadyChanged(false)
	if m.State() != StateArmingRequested {
		t.Errorf("flap-down should not cancel, got %v", m.State())
	}
	// FC recovers.
	m.FCReadyChanged(true)
	if m.State() != StateArmingRequested {
		t.Errorf("flap-up should not transition, got %v", m.State())
	}
	// Now operator confirms; should arm (FC is ready right now).
	m.Confirm()
	if m.State() != StateArmed {
		t.Errorf("confirm during ready=true should arm, got %v", m.State())
	}
}

func TestEventDropping_BufferOverflow(t *testing.T) {
	// 1-deep buffer; emit several without draining.
	m := New(WithEventBuffer(1))
	m.Init(false) // key down at boot, no event emitted
	// Flip key up to fill buffer with arming-requested.
	m.KeyChanged(true)
	// Flip key down to attempt arming-cancelled; channel is full so drop.
	m.KeyChanged(false)
	if got := m.Dropped(); got != 1 {
		t.Errorf("expected 1 dropped event, got %d", got)
	}
}

func TestSnapshot_Reflects(t *testing.T) {
	m := New()
	m.Init(false)
	m.ThrottleChanged(true)
	m.FCReadyChanged(true)
	m.KeyChanged(true)
	s := m.Snapshot()
	if s.State != StateArmingRequested {
		t.Errorf("snapshot state: got %v", s.State)
	}
	if !s.KeyUp || !s.ThrottleZero || !s.FCReady {
		t.Errorf("snapshot inputs: %+v", s)
	}
}

func TestSnapshot_TimeoutFields(t *testing.T) {
	m := New()
	m.Init(false)
	m.KeyChanged(true)

	s := m.Snapshot()
	if s.State != StateArmingRequested {
		t.Fatalf("expected ARMING_REQUESTED, got %v", s.State)
	}
	if s.RequestedAt.IsZero() {
		t.Error("RequestedAt should be set in ARMING_REQUESTED")
	}
	if s.RemainingSeconds <= 0 || s.RemainingSeconds > 60 {
		t.Errorf("RemainingSeconds=%d, want 1..60", s.RemainingSeconds)
	}

	m.KeyChanged(false)
	s = m.Snapshot()
	if s.State != StateDisarmed {
		t.Fatalf("expected DISARMED after key down, got %v", s.State)
	}
	if !s.RequestedAt.IsZero() {
		t.Error("RequestedAt should be zero outside ARMING_REQUESTED")
	}
	if s.RemainingSeconds != 0 {
		t.Errorf("RemainingSeconds=%d outside ARMING_REQUESTED, want 0", s.RemainingSeconds)
	}
}

func TestStateString(t *testing.T) {
	cases := map[State]string{
		StateDisarmed:        "DISARMED",
		StateArmingRequested: "ARMING_REQUESTED",
		StateArmed:           "ARMED",
		State(99):            "UNKNOWN",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("%d.String(): got %q, want %q", s, got, want)
		}
	}
}

func TestEventString(t *testing.T) {
	// Just spot-check coverage.
	if EventArmed.String() != "armed" {
		t.Error("EventArmed.String mismatch")
	}
	if EventDisarmDeniedInFlight.String() != "disarm-denied-in-flight" {
		t.Error("EventDisarmDeniedInFlight.String mismatch")
	}
	if Event(99).String() != "unknown" {
		t.Error("unknown event string")
	}
}
