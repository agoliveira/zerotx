package devhealth

import (
	"errors"
	"testing"
	"time"
)

// TestRegister_NewDeviceIsUnknown: a fresh-registered device should
// report status "unknown" (zero LastSeen). This is the boot state
// before any liveness signal has arrived.
func TestRegister_NewDeviceIsUnknown(t *testing.T) {
	r := New()
	r.Register("rp2040", KindRP2040, true, 2*time.Second)
	snaps := r.SnapshotAll()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].Status != StatusUnknown {
		t.Errorf("fresh device status: got %q, want %q", snaps[0].Status, StatusUnknown)
	}
	if !snaps[0].LastSeen.IsZero() {
		t.Errorf("fresh device LastSeen should be zero, got %v", snaps[0].LastSeen)
	}
}

// TestTouch_UpdatesLastSeenAndStatus: calling Touch with err=nil
// should set LastSeen and flip status to "up".
func TestTouch_UpdatesLastSeenAndStatus(t *testing.T) {
	r := New()
	r.Register("mega", KindMega, false, 30*time.Second)
	if !r.Touch("mega", nil) {
		t.Fatalf("Touch on registered device returned false")
	}
	snap := r.SnapshotAll()[0]
	if snap.Status != StatusUp {
		t.Errorf("after Touch: status got %q, want %q", snap.Status, StatusUp)
	}
	if snap.LastSeen.IsZero() {
		t.Errorf("after Touch: LastSeen still zero")
	}
}

// TestTouch_UnregisteredIsNoOp: Touching a name we never Register'd
// returns false, doesn't panic, doesn't create a phantom entry.
// This is the contract we rely on for "Register on first event,
// Touch on subsequent ones" — Touch alone should NOT create.
func TestTouch_UnregisteredIsNoOp(t *testing.T) {
	r := New()
	if r.Touch("ghost", nil) {
		t.Errorf("Touch on unregistered device returned true")
	}
	if len(r.SnapshotAll()) != 0 {
		t.Errorf("Touch on unregistered device created a phantom entry")
	}
}

// TestStatus_Demotion: a device that WAS up becomes "down" once its
// Timeout has elapsed since LastSeen. We can't sleep in tests
// without flakiness, so this test uses a tiny Timeout and a
// targeted Sleep just past it. Tolerable: 10 ms is small enough
// not to slow the suite, large enough that scheduler jitter
// doesn't false-pass.
func TestStatus_Demotion(t *testing.T) {
	r := New()
	r.Register("mega", KindMega, false, 5*time.Millisecond)
	r.Touch("mega", nil)
	// Immediately after Touch: should be up.
	if s := r.SnapshotAll()[0].Status; s != StatusUp {
		t.Errorf("immediately after Touch: got %q, want %q", s, StatusUp)
	}
	time.Sleep(15 * time.Millisecond)
	// After timeout: should be down (LastSeen is non-zero, just stale).
	if s := r.SnapshotAll()[0].Status; s != StatusDown {
		t.Errorf("after timeout: got %q, want %q", s, StatusDown)
	}
}

// TestTouch_ErrorRecordsFirstError: Touching with an error should
// NOT advance LastSeen, but SHOULD record the error string.
// Subsequent identical errors are NOT overwritten (FirstError is
// sticky until a successful Touch clears it).
func TestTouch_ErrorRecordsFirstError(t *testing.T) {
	r := New()
	r.Register("mega", KindMega, false, 30*time.Second)

	r.Touch("mega", errors.New("port closed"))
	snap := r.SnapshotAll()[0]
	if snap.FirstError != "port closed" {
		t.Errorf("FirstError: got %q, want %q", snap.FirstError, "port closed")
	}
	if !snap.LastSeen.IsZero() {
		t.Errorf("LastSeen should still be zero after error Touch, got %v", snap.LastSeen)
	}

	// A second, different error should NOT overwrite the first.
	r.Touch("mega", errors.New("totally different problem"))
	snap = r.SnapshotAll()[0]
	if snap.FirstError != "port closed" {
		t.Errorf("FirstError should be sticky; got %q", snap.FirstError)
	}

	// A successful Touch should clear FirstError.
	r.Touch("mega", nil)
	snap = r.SnapshotAll()[0]
	if snap.FirstError != "" {
		t.Errorf("FirstError should be cleared after ok Touch; got %q", snap.FirstError)
	}
}

// TestAllBlockingUp_NoBlockingDevicesIsTrue: with no blocking
// devices registered, the helper returns true (no blockers to wait
// on). This is the boot-time state before commits 2 and 3 wire in
// the RP2040 and HDMI checks.
func TestAllBlockingUp_NoBlockingDevicesIsTrue(t *testing.T) {
	r := New()
	r.Register("vfd.0", KindMegaSubsys, false, 30*time.Second)
	r.Register("encoder", KindMegaSubsys, false, 30*time.Second)
	if !r.AllBlockingUp() {
		t.Errorf("AllBlockingUp with no blocking devices: got false, want true")
	}
}

// TestAllBlockingUp_BlockingUnknownIsFalse: a registered-but-never-
// touched blocking device should NOT count as up.
func TestAllBlockingUp_BlockingUnknownIsFalse(t *testing.T) {
	r := New()
	r.Register("rp2040", KindRP2040, true, 2*time.Second)
	if r.AllBlockingUp() {
		t.Errorf("AllBlockingUp with unknown blocking device: got true, want false")
	}
}

// TestAllBlockingUp_MixedStates: with two blocking devices, one up
// and one not, AllBlockingUp returns false. With both up, true.
func TestAllBlockingUp_MixedStates(t *testing.T) {
	r := New()
	r.Register("rp2040", KindRP2040, true, 2*time.Second)
	r.Register("hdmi.0", KindHDMIDisplay, true, 30*time.Second)

	// Both unknown:
	if r.AllBlockingUp() {
		t.Errorf("both unknown: got true, want false")
	}

	// Touch one:
	r.Touch("rp2040", nil)
	if r.AllBlockingUp() {
		t.Errorf("one up, one unknown: got true, want false")
	}

	// Touch the other:
	r.Touch("hdmi.0", nil)
	if !r.AllBlockingUp() {
		t.Errorf("both up: got false, want true")
	}
}

// TestBlockingDown_ListsBlockingNotUp: BlockingDown should return
// the names of blocking devices that aren't up, sorted, and exclude
// non-blocking devices regardless of their state.
func TestBlockingDown_ListsBlockingNotUp(t *testing.T) {
	r := New()
	r.Register("rp2040", KindRP2040, true, 2*time.Second)
	r.Register("hdmi.0", KindHDMIDisplay, true, 30*time.Second)
	r.Register("hdmi.1", KindHDMIDisplay, true, 30*time.Second)
	r.Register("vfd.0", KindMegaSubsys, false, 30*time.Second)

	// All unknown except the non-blocking one (which doesn't matter).
	down := r.BlockingDown()
	expected := []string{"hdmi.0", "hdmi.1", "rp2040"}
	if !equalStringSlices(down, expected) {
		t.Errorf("BlockingDown all unknown: got %v, want %v", down, expected)
	}

	// Touch one HDMI; should drop from list.
	r.Touch("hdmi.0", nil)
	down = r.BlockingDown()
	expected = []string{"hdmi.1", "rp2040"}
	if !equalStringSlices(down, expected) {
		t.Errorf("BlockingDown one up: got %v, want %v", down, expected)
	}

	// Touch all; should be empty (non-nil).
	r.Touch("hdmi.1", nil)
	r.Touch("rp2040", nil)
	down = r.BlockingDown()
	if len(down) != 0 {
		t.Errorf("BlockingDown all up: got %v, want []", down)
	}
	if down == nil {
		t.Errorf("BlockingDown all up: got nil, want empty slice")
	}
}

// TestSnapshotAll_SortedByName: the deterministic-ordering contract
// for UI rendering.
func TestSnapshotAll_SortedByName(t *testing.T) {
	r := New()
	r.Register("zeta", KindMega, false, 30*time.Second)
	r.Register("alpha", KindRP2040, false, 30*time.Second)
	r.Register("mike", KindHDMIDisplay, false, 30*time.Second)

	snaps := r.SnapshotAll()
	if len(snaps) != 3 {
		t.Fatalf("got %d snaps, want 3", len(snaps))
	}
	want := []string{"alpha", "mike", "zeta"}
	for i, w := range want {
		if snaps[i].Name != w {
			t.Errorf("snap %d name: got %q, want %q", i, snaps[i].Name, w)
		}
	}
}

// TestRegister_Replaces: registering the same name twice replaces
// (resets LastSeen). Lets startup code re-Register without leaking
// stale liveness from a previous instance.
func TestRegister_Replaces(t *testing.T) {
	r := New()
	r.Register("mega", KindMega, false, 30*time.Second)
	r.Touch("mega", nil)
	if r.SnapshotAll()[0].Status != StatusUp {
		t.Fatalf("setup: should be up")
	}

	// Re-Register: resets to unknown.
	r.Register("mega", KindMega, false, 30*time.Second)
	if s := r.SnapshotAll()[0].Status; s != StatusUnknown {
		t.Errorf("after re-Register: status got %q, want %q", s, StatusUnknown)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
