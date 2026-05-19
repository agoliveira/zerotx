package main

import (
	"reflect"
	"testing"

	"github.com/agoliveira/zerotx/pi/daemon/internal/gps"
	"github.com/agoliveira/zerotx/pi/daemon/internal/recorder"
)

// === recoveryOperatorAdapter.ConfiguredSources ===

func TestConfiguredSources_NoneConfigured(t *testing.T) {
	// Daemon launched without -gps-port and without -site-lat/
	// -site-lon. The recovery view will be unable to compute
	// bearing/distance to a lost aircraft; pre-flight UI must
	// see an empty slice (non-nil) to surface the warning.
	a := &recoveryOperatorAdapter{}
	got := a.ConfiguredSources()
	if got == nil {
		t.Fatal("ConfiguredSources returned nil; want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("ConfiguredSources = %v, want []", got)
	}
}

func TestConfiguredSources_GPSOnly(t *testing.T) {
	// Pi GPS hardware fitted. Note that we only need the adapter to
	// see a non-nil *gps.Reader; the live fix state is irrelevant
	// here because ConfiguredSources is a static check.
	a := &recoveryOperatorAdapter{gps: &gps.Reader{}}
	got := a.ConfiguredSources()
	want := []string{"gps"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ConfiguredSources = %v, want %v", got, want)
	}
}

func TestConfiguredSources_SiteOnly(t *testing.T) {
	// No GPS hardware, operator passed -site-lat/-site-lon. The
	// site-flag check fires if EITHER lat OR lon is non-zero,
	// matching what OperatorPosition() consumes.
	a := &recoveryOperatorAdapter{siteLat: -22.91, siteLon: -47.06}
	got := a.ConfiguredSources()
	want := []string{"site"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ConfiguredSources = %v, want %v", got, want)
	}
}

func TestConfiguredSources_BothConfigured(t *testing.T) {
	// Both: GPS as primary, site flags as fallback. Order matters
	// for the wire format -- "gps" before "site" -- so the GUI
	// can render deterministically.
	a := &recoveryOperatorAdapter{
		gps:     &gps.Reader{},
		siteLat: -22.91,
		siteLon: -47.06,
	}
	got := a.ConfiguredSources()
	want := []string{"gps", "site"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ConfiguredSources = %v, want %v (order matters)", got, want)
	}
}

func TestConfiguredSources_SiteLatOnly(t *testing.T) {
	// Edge case: only one of -site-lat/-site-lon non-zero. The
	// OperatorPosition() resolver still emits a "site" position
	// using whichever is set plus a zero for the other; ConfiguredSources
	// matches that policy and reports "site" configured.
	a := &recoveryOperatorAdapter{siteLat: -22.91}
	got := a.ConfiguredSources()
	want := []string{"site"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ConfiguredSources = %v, want %v", got, want)
	}
}

func TestConfiguredSources_ReturnsFreshSlice(t *testing.T) {
	// Each call returns a fresh slice so callers can mutate
	// without affecting the adapter or other consumers.
	a := &recoveryOperatorAdapter{gps: &gps.Reader{}}
	first := a.ConfiguredSources()
	first[0] = "tampered"
	second := a.ConfiguredSources()
	if second[0] != "gps" {
		t.Errorf("second call saw mutation from first: got %v", second)
	}
}

// === newRecoveryWiring ===

func TestNewRecoveryWiring_PopulatesBoth(t *testing.T) {
	// Constructor must return non-nil Adapter and Manager fields.
	// Either being nil would crash main() at first State() query
	// or at the flightEvents.SetRecoveryManager registration.
	rec := recorder.NoOpRecorder{}
	rw := newRecoveryWiring(nil, 0, 0, rec)
	if rw == nil {
		t.Fatal("newRecoveryWiring returned nil")
	}
	if rw.Adapter == nil {
		t.Error("Adapter is nil")
	}
	if rw.Manager == nil {
		t.Error("Manager is nil")
	}
}

func TestNewRecoveryWiring_AdapterCarriesConfig(t *testing.T) {
	// The adapter must round-trip the construction params so
	// ConfiguredSources reports them correctly downstream.
	rw := newRecoveryWiring(&gps.Reader{}, -22.91, -47.06, recorder.NoOpRecorder{})
	got := rw.Adapter.ConfiguredSources()
	want := []string{"gps", "site"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Adapter.ConfiguredSources = %v, want %v", got, want)
	}
}

func TestNewRecoveryWiring_ManagerStartsIdle(t *testing.T) {
	// Fresh wiring is in IDLE: IsActive=false. Trigger has not been
	// called yet, and Dismiss is a no-op.
	rw := newRecoveryWiring(nil, 0, 0, recorder.NoOpRecorder{})
	if rw.Manager.IsActive() {
		t.Error("Manager.IsActive() = true on fresh wiring; want false")
	}
}

func TestNewRecoveryWiring_NilRecorderTolerated(t *testing.T) {
	// recovery.New accepts nil for the recorder. Daemon code paths
	// that don't have a recorder yet (or never do) must not crash
	// here.
	rw := newRecoveryWiring(nil, 0, 0, nil)
	if rw == nil || rw.Manager == nil {
		t.Fatal("nil recorder must not produce nil wiring/manager")
	}
}
