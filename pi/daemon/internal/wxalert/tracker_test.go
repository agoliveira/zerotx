package wxalert

import (
	"testing"
	"time"
)

func gust(value float64) []Alert {
	if value > 0 {
		return []Alert{{Name: "wind_gust_high", Severity: SeverityWarning,
			Message: "gust high"}}
	}
	return nil
}

func TestTracker_NoHysteresis_ImmediateTransitions(t *testing.T) {
	tr := NewTracker(0, 0)
	t0 := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)

	// First tick: predicate true, immediate fire.
	trs := tr.Update(t0, gust(35))
	if len(trs) != 1 || !trs[0].Activated || trs[0].Name != "wind_gust_high" {
		t.Errorf("expected immediate activation; got %+v", trs)
	}

	// Second tick: still true, no transition.
	trs = tr.Update(t0.Add(time.Minute), gust(36))
	if len(trs) != 0 {
		t.Errorf("expected no transition; got %+v", trs)
	}

	// Third tick: predicate false, immediate clear.
	trs = tr.Update(t0.Add(2*time.Minute), nil)
	if len(trs) != 1 || trs[0].Activated || trs[0].Name != "wind_gust_high" {
		t.Errorf("expected immediate clear; got %+v", trs)
	}
}

func TestTracker_FireHysteresis_DelaysActivation(t *testing.T) {
	tr := NewTracker(5*time.Minute, 10*time.Minute)
	t0 := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)

	// t=0: above. No transition yet (need 5 min).
	if trs := tr.Update(t0, gust(35)); len(trs) != 0 {
		t.Errorf("t=0 should not transition; got %+v", trs)
	}
	// t=4m: still above, still no transition.
	if trs := tr.Update(t0.Add(4*time.Minute), gust(35)); len(trs) != 0 {
		t.Errorf("t=4m should not transition; got %+v", trs)
	}
	// t=5m: meets threshold, fires.
	trs := tr.Update(t0.Add(5*time.Minute), gust(35))
	if len(trs) != 1 || !trs[0].Activated {
		t.Errorf("t=5m should activate; got %+v", trs)
	}
}

func TestTracker_FireHysteresis_ResetsOnDip(t *testing.T) {
	tr := NewTracker(5*time.Minute, 10*time.Minute)
	t0 := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)

	tr.Update(t0, gust(35))                    // t=0 above
	tr.Update(t0.Add(2*time.Minute), gust(35)) // t=2m above
	tr.Update(t0.Add(3*time.Minute), nil)      // t=3m DIP - resets count
	tr.Update(t0.Add(4*time.Minute), gust(35)) // t=4m above again - count restarts
	// t=8m: 4 min above since restart. Still under 5 min. Should NOT fire.
	if trs := tr.Update(t0.Add(8*time.Minute), gust(35)); len(trs) != 0 {
		t.Errorf("t=8m: only 4m since dip, should not fire; got %+v", trs)
	}
	// t=9m: 5 min since restart. Fires.
	if trs := tr.Update(t0.Add(9*time.Minute), gust(35)); len(trs) != 1 || !trs[0].Activated {
		t.Errorf("t=9m: 5m since dip, should fire; got %+v", trs)
	}
}

func TestTracker_ClearHysteresis_DelaysClear(t *testing.T) {
	tr := NewTracker(0, 10*time.Minute) // fire immediately, slow clear
	t0 := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)

	tr.Update(t0, gust(35))                        // immediate fire at t=0
	tr.Update(t0.Add(2*time.Minute), nil)          // t=2m: predicate false, clear-count starts
	// t=11m: 9 min below since count started. Should NOT clear yet.
	if trs := tr.Update(t0.Add(11*time.Minute), nil); len(trs) != 0 {
		t.Errorf("t=11m: only 9m below since predicate dropped, should not clear; got %+v", trs)
	}
	// t=12m: 10m below since count started. Clears.
	if trs := tr.Update(t0.Add(12*time.Minute), nil); len(trs) != 1 || trs[0].Activated {
		t.Errorf("t=12m: 10m below since predicate dropped, should clear; got %+v", trs)
	}
}

func TestTracker_ClearHysteresis_ResetsOnSpike(t *testing.T) {
	tr := NewTracker(0, 10*time.Minute)
	t0 := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)

	tr.Update(t0, gust(35))                    // fire
	tr.Update(t0.Add(2*time.Minute), nil)      // start counting clear
	tr.Update(t0.Add(5*time.Minute), nil)      // 3m of below
	tr.Update(t0.Add(6*time.Minute), gust(35)) // SPIKE back above - resets clear count
	tr.Update(t0.Add(7*time.Minute), nil)      // back below; count restarts at t=7m
	// t=16m: 9 min below since restart. Should NOT clear.
	if trs := tr.Update(t0.Add(16*time.Minute), nil); len(trs) != 0 {
		t.Errorf("t=16m: only 9m below since spike, should not clear; got %+v", trs)
	}
	// t=17m: 10 min, clears.
	if trs := tr.Update(t0.Add(17*time.Minute), nil); len(trs) != 1 || trs[0].Activated {
		t.Errorf("t=17m: 10m below since spike, should clear; got %+v", trs)
	}
}

func TestTracker_ActiveAlerts_ReflectsState(t *testing.T) {
	tr := NewTracker(0, 0)
	t0 := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)

	tr.Update(t0, []Alert{
		{Name: "wind_gust_high", Severity: SeverityWarning, Message: "gust"},
		{Name: "near_sunset", Severity: SeverityNotice, Message: "sunset"},
	})
	active := tr.ActiveAlerts()
	if len(active) != 2 {
		t.Fatalf("expected 2 active, got %d: %+v", len(active), active)
	}
	// Sorted alphabetically.
	if active[0].Name != "near_sunset" || active[1].Name != "wind_gust_high" {
		t.Errorf("active not sorted by name: %+v", active)
	}

	// Drop one.
	tr.Update(t0.Add(time.Minute), []Alert{
		{Name: "wind_gust_high", Severity: SeverityWarning, Message: "gust"},
	})
	active = tr.ActiveAlerts()
	if len(active) != 1 || active[0].Name != "wind_gust_high" {
		t.Errorf("expected only wind_gust_high active, got %+v", active)
	}
}

func TestTracker_ClearTransitionCarriesLastAlert(t *testing.T) {
	tr := NewTracker(0, 0)
	t0 := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)

	original := Alert{Name: "wind_gust_high", Severity: SeverityWarning, Message: "gust 35"}
	tr.Update(t0, []Alert{original})
	trs := tr.Update(t0.Add(time.Minute), nil)

	if len(trs) != 1 || trs[0].Activated {
		t.Fatalf("expected one clear transition; got %+v", trs)
	}
	if trs[0].Alert.Message != "gust 35" {
		t.Errorf("clear transition should carry last-known alert; got %q",
			trs[0].Alert.Message)
	}
	if trs[0].Alert.Severity != SeverityWarning {
		t.Errorf("clear transition lost severity; got %v", trs[0].Alert.Severity)
	}
}
