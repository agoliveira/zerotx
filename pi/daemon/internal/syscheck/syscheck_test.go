package syscheck

import (
	"testing"
	"time"
)

// TestGate_StartsUndismissed: a fresh gate reports dismissed=false
// with no timestamp.
func TestGate_StartsUndismissed(t *testing.T) {
	g := New()
	s := g.Snapshot()
	if s.Dismissed {
		t.Error("fresh gate should not be dismissed")
	}
	if !s.DismissedAt.IsZero() {
		t.Errorf("fresh gate DismissedAt should be zero, got %v", s.DismissedAt)
	}
}

// TestGate_Dismiss flips the flag and stamps the time.
func TestGate_Dismiss(t *testing.T) {
	g := New()
	before := time.Now()
	g.Dismiss()
	after := time.Now()

	s := g.Snapshot()
	if !s.Dismissed {
		t.Error("after Dismiss, gate should be dismissed")
	}
	if s.DismissedAt.Before(before) || s.DismissedAt.After(after) {
		t.Errorf("DismissedAt=%v should fall between %v and %v", s.DismissedAt, before, after)
	}
}

// TestGate_DismissIsIdempotent: a second Dismiss does not move the
// timestamp. Important so kiosks observe a single transition, not
// repeated "redo" events on every operator click.
func TestGate_DismissIsIdempotent(t *testing.T) {
	g := New()
	g.Dismiss()
	first := g.Snapshot().DismissedAt

	time.Sleep(2 * time.Millisecond)
	g.Dismiss()
	second := g.Snapshot().DismissedAt

	if !first.Equal(second) {
		t.Errorf("second Dismiss moved timestamp from %v to %v", first, second)
	}
}

// TestGate_ConcurrentAccess sanity-checks that Snapshot doesn't race
// against Dismiss. With -race this fails if the locking is wrong.
func TestGate_ConcurrentAccess(t *testing.T) {
	g := New()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			_ = g.Snapshot()
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		g.Dismiss() // mostly idempotent after the first call
	}
	<-done
}
