package model

import (
	"strings"
	"testing"
)

func TestValidate_AcceptsEmptyMeta(t *testing.T) {
	m := &ZeroTXMeta{}
	if err := m.Validate(); err != nil {
		t.Fatalf("empty meta should validate, got: %v", err)
	}
}

func TestValidate_AcceptsLegacyBindingsOnly(t *testing.T) {
	m := &ZeroTXMeta{
		SourceBindings: map[string]Binding{
			"Thr": {Device: "HOTAS", Axis: ptrInt(2)},
		},
		Notes: "legacy file with no thresholds",
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("legacy meta should validate, got: %v", err)
	}
}

func TestValidate_FCType(t *testing.T) {
	tests := []struct {
		name    string
		fcType  string
		wantErr bool
	}{
		{"empty allowed", "", false},
		{"inav", "inav", false},
		{"ardupilot", "ardupilot", false},
		{"betaflight", "betaflight", false},
		{"unknown", "px4", true},
		{"capitalized rejected", "INAV", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &ZeroTXMeta{FCType: tt.fcType}
			err := m.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("FCType=%q: err=%v wantErr=%v", tt.fcType, err, tt.wantErr)
			}
		})
	}
}

func TestValidate_Airframe(t *testing.T) {
	tests := []struct {
		name     string
		airframe string
		wantErr  bool
	}{
		{"empty allowed", "", false},
		{"quad", "quad", false},
		{"wing", "wing", false},
		{"plane", "plane", false},
		{"heli", "heli", false},
		{"unknown", "boat", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &ZeroTXMeta{Airframe: tt.airframe}
			err := m.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Airframe=%q: err=%v wantErr=%v", tt.airframe, err, tt.wantErr)
			}
		})
	}
}

func TestValidate_Battery_Valid4S(t *testing.T) {
	m := &ZeroTXMeta{
		Thresholds: &Thresholds{
			Battery: &BatteryThresholds{
				Cells: 4, CellMinV: 3.2, CellCritV: 3.4, CellWarnV: 3.6, CellFullV: 4.2,
			},
		},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("valid 4S battery should pass, got: %v", err)
	}
}

func TestValidate_Battery_Errors(t *testing.T) {
	tests := []struct {
		name    string
		battery BatteryThresholds
		wantSub string
	}{
		{
			name:    "cells too low",
			battery: BatteryThresholds{Cells: 0, CellMinV: 3.2, CellCritV: 3.4, CellWarnV: 3.6, CellFullV: 4.2},
			wantSub: "cells",
		},
		{
			name:    "cells too high",
			battery: BatteryThresholds{Cells: 17, CellMinV: 3.2, CellCritV: 3.4, CellWarnV: 3.6, CellFullV: 4.2},
			wantSub: "cells",
		},
		{
			name:    "missing voltages",
			battery: BatteryThresholds{Cells: 4},
			wantSub: "must be > 0",
		},
		{
			name:    "warn below crit",
			battery: BatteryThresholds{Cells: 4, CellMinV: 3.2, CellCritV: 3.6, CellWarnV: 3.4, CellFullV: 4.2},
			wantSub: "min <= crit <= warn <= full",
		},
		{
			name:    "min above crit",
			battery: BatteryThresholds{Cells: 4, CellMinV: 3.5, CellCritV: 3.4, CellWarnV: 3.6, CellFullV: 4.2},
			wantSub: "min <= crit <= warn <= full",
		},
		{
			name:    "full too high",
			battery: BatteryThresholds{Cells: 4, CellMinV: 3.2, CellCritV: 3.4, CellWarnV: 3.6, CellFullV: 4.5},
			wantSub: "cell_full_v",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &ZeroTXMeta{Thresholds: &Thresholds{Battery: &tt.battery}}
			err := m.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("expected error to contain %q, got: %v", tt.wantSub, err)
			}
		})
	}
}

func TestValidate_Altitude(t *testing.T) {
	tests := []struct {
		name    string
		alt     AltitudeThresholds
		wantErr bool
	}{
		{"valid", AltitudeThresholds{WarnM: 100, CritM: 200}, false},
		{"warn equals crit", AltitudeThresholds{WarnM: 100, CritM: 100}, true},
		{"warn above crit", AltitudeThresholds{WarnM: 200, CritM: 100}, true},
		{"negative warn", AltitudeThresholds{WarnM: -10, CritM: 200}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &ZeroTXMeta{Thresholds: &Thresholds{Altitude: &tt.alt}}
			err := m.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Altitude=%+v: err=%v wantErr=%v", tt.alt, err, tt.wantErr)
			}
		})
	}
}

func TestValidate_Distance(t *testing.T) {
	tests := []struct {
		name    string
		dist    DistanceThresholds
		wantErr bool
	}{
		{"valid", DistanceThresholds{WarnM: 500, CritM: 1500}, false},
		{"warn equals crit", DistanceThresholds{WarnM: 500, CritM: 500}, true},
		{"warn above crit", DistanceThresholds{WarnM: 2000, CritM: 1500}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &ZeroTXMeta{Thresholds: &Thresholds{Distance: &tt.dist}}
			err := m.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Distance=%+v: err=%v wantErr=%v", tt.dist, err, tt.wantErr)
			}
		})
	}
}

func TestValidate_Link(t *testing.T) {
	tests := []struct {
		name    string
		link    LinkThresholds
		wantErr bool
	}{
		{"valid", LinkThresholds{RSSIWarnDBM: -90, RSSICritDBM: -100, LQWarnPct: 70, LQCritPct: 50}, false},
		{"rssi warn equals crit", LinkThresholds{RSSIWarnDBM: -100, RSSICritDBM: -100, LQWarnPct: 70, LQCritPct: 50}, true},
		{"rssi warn more negative", LinkThresholds{RSSIWarnDBM: -100, RSSICritDBM: -90, LQWarnPct: 70, LQCritPct: 50}, true},
		{"lq out of range", LinkThresholds{RSSIWarnDBM: -90, RSSICritDBM: -100, LQWarnPct: 110, LQCritPct: 50}, true},
		{"lq warn below crit", LinkThresholds{RSSIWarnDBM: -90, RSSICritDBM: -100, LQWarnPct: 50, LQCritPct: 70}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &ZeroTXMeta{Thresholds: &Thresholds{Link: &tt.link}}
			err := m.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Link=%+v: err=%v wantErr=%v", tt.link, err, tt.wantErr)
			}
		})
	}
}

func TestValidate_FlightTime(t *testing.T) {
	tests := []struct {
		name    string
		ft      FlightTimeThresholds
		wantErr bool
	}{
		{"valid", FlightTimeThresholds{WarnS: 600, CritS: 900}, false},
		{"warn equals crit", FlightTimeThresholds{WarnS: 900, CritS: 900}, true},
		{"warn above crit", FlightTimeThresholds{WarnS: 900, CritS: 600}, true},
		{"zero warn", FlightTimeThresholds{WarnS: 0, CritS: 900}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &ZeroTXMeta{Thresholds: &Thresholds{FlightTime: &tt.ft}}
			err := m.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("FlightTime=%+v: err=%v wantErr=%v", tt.ft, err, tt.wantErr)
			}
		})
	}
}

func TestPackVoltageHelpers(t *testing.T) {
	m := &ZeroTXMeta{
		Thresholds: &Thresholds{
			Battery: &BatteryThresholds{
				Cells: 4, CellMinV: 3.2, CellCritV: 3.4, CellWarnV: 3.6, CellFullV: 4.2,
			},
		},
	}
	cases := []struct {
		name string
		got  float64
		want float64
	}{
		{"warn", m.PackWarnV(), 14.4},
		{"crit", m.PackCritV(), 13.6},
		{"min", m.PackMinV(), 12.8},
		{"full", m.PackFullV(), 16.8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !floatNearlyEqual(c.got, c.want, 1e-6) {
				t.Errorf("Pack%sV = %.4f, want %.4f", c.name, c.got, c.want)
			}
		})
	}
}

func TestPackVoltageHelpers_NoBattery(t *testing.T) {
	m := &ZeroTXMeta{}
	if got := m.PackWarnV(); got != 0 {
		t.Errorf("PackWarnV with no battery = %v, want 0", got)
	}
	if m.HasBatteryThresholds() {
		t.Error("HasBatteryThresholds with no battery should be false")
	}
}

func TestHasXxxThresholds(t *testing.T) {
	empty := &ZeroTXMeta{}
	if empty.HasBatteryThresholds() || empty.HasAltThresholds() || empty.HasDistThresholds() ||
		empty.HasLinkThresholds() || empty.HasFlightTimeThresholds() {
		t.Error("empty meta should report no thresholds for any domain")
	}

	full := &ZeroTXMeta{Thresholds: &Thresholds{
		Battery:    &BatteryThresholds{Cells: 4, CellMinV: 3.2, CellCritV: 3.4, CellWarnV: 3.6, CellFullV: 4.2},
		Altitude:   &AltitudeThresholds{WarnM: 100, CritM: 200},
		Distance:   &DistanceThresholds{WarnM: 500, CritM: 1500},
		Link:       &LinkThresholds{RSSIWarnDBM: -90, RSSICritDBM: -100, LQWarnPct: 70, LQCritPct: 50},
		FlightTime: &FlightTimeThresholds{WarnS: 600, CritS: 900},
	}}
	if !full.HasBatteryThresholds() || !full.HasAltThresholds() || !full.HasDistThresholds() ||
		!full.HasLinkThresholds() || !full.HasFlightTimeThresholds() {
		t.Error("fully-populated meta should report thresholds for all domains")
	}
}

// === helpers ===

func ptrInt(i int) *int { return &i }
func floatNearlyEqual(a, b, eps float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}
