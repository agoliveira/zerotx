package main

import (
	"strings"
	"testing"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/telemetry"
)

func TestResolveNarrateContent_Explicit(t *testing.T) {
	got := resolveNarrateContent("battery,distance,unknown", "")
	want := []narrateField{fieldBattery, fieldDistance}
	if !equalFields(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestResolveNarrateContent_PresetCompact(t *testing.T) {
	got := resolveNarrateContent("", "compact")
	want := []narrateField{fieldBattery, fieldDistance, fieldAltitude}
	if !equalFields(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestResolveNarrateContent_PresetFull(t *testing.T) {
	got := resolveNarrateContent("", "full")
	if len(got) != len(allNarrateFields) {
		t.Errorf("full preset: got %d fields, want %d", len(got), len(allNarrateFields))
	}
}

func TestResolveNarrateContent_ExplicitOverridesPreset(t *testing.T) {
	got := resolveNarrateContent("battery", "full")
	want := []narrateField{fieldBattery}
	if !equalFields(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestResolveNarrateContent_Empty(t *testing.T) {
	if got := resolveNarrateContent("", ""); len(got) != 0 {
		t.Errorf("got %v want empty", got)
	}
}

func TestResolveNarrateContent_BadPreset(t *testing.T) {
	if got := resolveNarrateContent("", "bogus"); len(got) != 0 {
		t.Errorf("got %v want empty (bad preset)", got)
	}
}

func TestParseFieldList_OrderIsCanonical(t *testing.T) {
	// Caller asks in reverse order; we should still get canonical ordering.
	got := parseFieldList("altitude,battery,distance")
	want := []narrateField{fieldBattery, fieldDistance, fieldAltitude}
	if !equalFields(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestBuildPeriodicStatus_AllFields(t *testing.T) {
	v := 16.2
	a := 1.5
	pct := uint8(78)
	cells := 4
	cellV := 4.05
	dist := int32(245)
	alt := int32(95)
	speed := 35.4
	lq := uint8(92)

	snap := telemetry.Snapshot{
		Battery: &telemetry.BatteryEntry{
			Data: telemetry.Battery{
				Volts:     v,
				Amps:      a,
				Percent:   pct,
				CellCount: cells,
				VoltsCell: cellV,
			},
		},
		GPS: &telemetry.GPSEntry{
			Data: telemetry.GPS{AltMeters: alt, GroundKmh: speed, Sats: 11},
		},
		Link: &telemetry.LinkEntry{
			Data: telemetry.Link{UplinkLQ: lq},
		},
		FlightMode: &telemetry.FlightModeEntry{
			Data: telemetry.FlightMode{Mode: "ANGL"},
		},
		Home: &telemetry.HomeEntry{
			Data: telemetry.Home{DistanceM: dist},
		},
	}
	got := buildPeriodicStatus(snap, allNarrateFields, 90*time.Second)
	wantContains := []string{
		"Battery 78 percent, 16.2 volts.",
		"Distance 245 meters.",
		"Altitude 95 meters.",
		"Speed 35 kilometers per hour.",
		"Link 92 percent.",
		"Mode angle.",
		"Aloft 1 minute 30 seconds.",
	}
	for _, w := range wantContains {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
}

func TestBuildPeriodicStatus_MissingTelemetry(t *testing.T) {
	// No snapshot data at all. We requested fields, but nothing is
	// available. Should return empty so the caller skips speaking.
	got := buildPeriodicStatus(telemetry.Snapshot{}, []narrateField{fieldBattery, fieldDistance}, 30*time.Second)
	if got != "" {
		t.Errorf("got %q want empty", got)
	}
}

func TestBuildPeriodicStatus_TimeAloftOnly(t *testing.T) {
	got := buildPeriodicStatus(telemetry.Snapshot{}, []narrateField{fieldTimeAloft}, 65*time.Second)
	want := "Aloft 1 minute 5 seconds."
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestBuildPeriodicStatus_StaleSensorIgnored(t *testing.T) {
	snap := telemetry.Snapshot{
		Battery: &telemetry.BatteryEntry{
			Stale: true,
			Data:  telemetry.Battery{Volts: 16.2, Percent: 78},
		},
	}
	got := buildPeriodicStatus(snap, []narrateField{fieldBattery}, 30*time.Second)
	if got != "" {
		t.Errorf("stale battery should be skipped; got %q", got)
	}
}

func equalFields(a, b []narrateField) bool {
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
