package narrator

import (
	"reflect"
	"strings"
	"testing"

	"github.com/agoliveira/zerotx/pi/daemon/internal/recorder"
)

// === Number fragments ===

func TestNumberFragments_Direct(t *testing.T) {
	cases := []struct {
		in   int
		want []string
	}{
		{0, []string{"n-0"}},
		{1, []string{"n-1"}},
		{15, []string{"n-15"}},
		{30, []string{"n-30"}},
	}
	for _, c := range cases {
		got := numberFragments(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%d: got %v want %v", c.in, got, c.want)
		}
	}
}

func TestNumberFragments_Decomposed(t *testing.T) {
	cases := []struct {
		in   int
		want []string
	}{
		{45, []string{"n-40", "n-5"}},
		{99, []string{"n-90", "n-9"}},
		{100, []string{"n-100"}},
		{124, []string{"n-100", "n-24"}}, // 24 has its own track
		{125, []string{"n-100", "n-25"}}, // 25 has its own track
		{135, []string{"n-100", "n-30", "n-5"}}, // 35 must decompose: 30 + 5
		{347, []string{"n-300", "n-40", "n-7"}},
		{1000, []string{"n-1000"}},
		{1500, []string{"n-1000", "n-500"}},
		{4275, []string{"n-4000", "n-200", "n-70", "n-5"}},
	}
	for _, c := range cases {
		got := numberFragments(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%d: got %v want %v", c.in, got, c.want)
		}
	}
}

func TestNumberFragments_NegativeReturnsAbs(t *testing.T) {
	got := numberFragments(-5)
	want := []string{"n-5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// === Duration ===

func TestDurationFragments(t *testing.T) {
	cases := []struct {
		in   int
		want []string
	}{
		{30, []string{"n-30", "u-seconds"}},
		{59, []string{"n-50", "n-9", "u-seconds"}},
		{60, []string{"n-1", "u-minutes"}},                 // exact minute drops seconds
		{90, []string{"n-1", "u-minutes", "n-30", "u-seconds"}},
		{125, []string{"n-2", "u-minutes", "n-5", "u-seconds"}},
		{300, []string{"n-5", "u-minutes"}},                // exact 5 minutes
		{725, []string{"n-12", "u-minutes", "n-5", "u-seconds"}},
	}
	for _, c := range cases {
		got := durationFragments(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%d: got %v want %v", c.in, got, c.want)
		}
	}
}

// === Altitude ===

func TestAltitudeFragments(t *testing.T) {
	cases := []struct {
		in   int
		want []string
	}{
		{50, []string{"n-50", "u-meters"}},                  // <= 100, no rounding
		{85, []string{"n-80", "n-5", "u-meters"}},           // <= 100, no rounding
		{124, []string{"n-100", "n-20", "u-meters"}},        // 124 rounds to 120
		{127, []string{"n-100", "n-30", "u-meters"}},        // 127 rounds to 130
		{1234, []string{"n-1000", "n-200", "n-30", "u-meters"}}, // 1234 -> 1230
	}
	for _, c := range cases {
		got := altitudeFragments(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%d: got %v want %v", c.in, got, c.want)
		}
	}
}

// === Speed ===

func TestSpeedFragments_RoundsToInt(t *testing.T) {
	got := speedFragments(22.4)
	want := []string{"n-22", "u-kmh"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// === Percent ===

func TestPercentFragments_RoundsTo5(t *testing.T) {
	cases := []struct {
		in   int
		want []string
	}{
		{37, []string{"n-30", "n-5", "u-percent"}}, // 35 decomposes to 30+5
		{38, []string{"n-40", "u-percent"}},        // rounds to 40 (single track)
		{50, []string{"n-50", "u-percent"}},
		{0, []string{"n-0", "u-percent"}},
		{20, []string{"n-20", "u-percent"}},        // exact, single track
		{15, []string{"n-15", "u-percent"}},        // exact, single track
	}
	for _, c := range cases {
		got := percentFragments(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%d: got %v want %v", c.in, got, c.want)
		}
	}
}

// === Battery ===

func TestBatteryUsedPercent_3S(t *testing.T) {
	// 3S full = 12.6, empty (flight) = 9.9. 50% would be 11.25.
	used := batteryUsedPercent(12.6, 11.25)
	if used < 45 || used > 55 {
		t.Errorf("expected ~50%%, got %d", used)
	}
}

func TestBatteryUsedPercent_NoChange(t *testing.T) {
	used := batteryUsedPercent(11.7, 11.7)
	if used != 0 {
		t.Errorf("expected 0%% (same start/end), got %d", used)
	}
}

func TestBatteryUsedPercent_EndAboveStartReturnsZero(t *testing.T) {
	// Voltage rebounded above start (e.g. measurement noise). Should
	// not produce a negative percentage.
	used := batteryUsedPercent(11.5, 11.7)
	if used != 0 {
		t.Errorf("expected 0%% when end > start, got %d", used)
	}
}

// === Alarm summary ===

func TestAlarmSummaryFragments(t *testing.T) {
	cases := []struct {
		name   string
		counts map[string]int
		want   []string
	}{
		{"empty", nil, []string{"no-alarms-triggered"}},
		{"none", map[string]int{}, []string{"no-alarms-triggered"}},
		{"one warning", map[string]int{"warning": 1}, []string{"one-warning-triggered"}},
		{"three warnings", map[string]int{"warning": 3}, []string{"n-3", "warnings-triggered"}},
		{"one critical", map[string]int{"critical": 1}, []string{"critical-alarm-triggered"}},
		{"two critical", map[string]int{"critical": 2}, []string{"n-2", "critical-alarms-triggered"}},
		{"critical wins over warning", map[string]int{"warning": 5, "critical": 1}, []string{"critical-alarm-triggered"}},
	}
	for _, c := range cases {
		got := alarmSummaryFragments(c.counts)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// === Flight number extraction ===

func TestFlightNumber(t *testing.T) {
	cases := []struct {
		name string
		want int
	}{
		{"flight-43.sqlite", 43},
		{"flight-43", 43},
		{"recording-1.sqlite", 1},
		{"43.sqlite", 43},
		{"", 0},
		{"no-number-here.sqlite", 0},
		{"2026-04-30T18-43-12.sqlite", 12},                // last run of digits is "12"
		{"flight-99999.sqlite", 0},                         // too big, sanity-cap
	}
	for _, c := range cases {
		got := flightNumber(c.name)
		if got != c.want {
			t.Errorf("%q: got %d want %d", c.name, got, c.want)
		}
	}
}

// === Round to step ===

func TestRoundToStep(t *testing.T) {
	cases := []struct {
		n, step, want int
	}{
		{37, 5, 35},
		{38, 5, 40},
		{124, 10, 120},
		{125, 10, 130},
		{125, 1, 125},
		{0, 5, 0},
	}
	for _, c := range cases {
		got := roundToStep(c.n, c.step)
		if got != c.want {
			t.Errorf("(%d, %d): got %d want %d", c.n, c.step, got, c.want)
		}
	}
}

// === End-to-end summary ===

func TestBuildPostFlightSequence_Quiet(t *testing.T) {
	// 4 minutes 30 seconds, peak 124m, max 22 km/h, 12.6V -> 11.25V (50% used),
	// flight number 43, no alarms.
	startV, endV := 12.6, 11.25
	maxAlt := 124
	maxKmh := 22.0
	s := &recorder.Summary{
		Name:        "flight-43.sqlite",
		DurationSec: 270,
		BatStartV:   &startV,
		BatEndV:     &endV,
		GpsMaxAlt:   &maxAlt,
		GpsMaxKmh:   &maxKmh,
		AlarmCounts: nil,
	}
	got := buildPostFlightSequence(s)

	// Spot-check key transitions rather than full equality (the
	// number tracks for non-trivial values are long).
	expectations := map[string]bool{
		"flight-complete":         false,
		"you-were-up-for":         false,
		"u-minutes":               false,
		"u-seconds":               false,
		"peak-altitude-of":        false,
		"u-meters":                false,
		"average-speed-of":        false,
		"u-kmh":                   false,
		"battery-used":            false,
		"u-percent":               false,
		"no-alarms-triggered":     false,
		"saved-as-flight-number":  false,
	}
	for _, frag := range got {
		if _, ok := expectations[frag]; ok {
			expectations[frag] = true
		}
	}
	for k, found := range expectations {
		if !found {
			t.Errorf("expected fragment %q in sequence, got %v", k, got)
		}
	}

	// Sanity: must start with flight-complete.
	if len(got) == 0 || got[0] != "flight-complete" {
		t.Errorf("sequence must start with flight-complete, got %v", got)
	}
}

func TestBuildPostFlightSequence_NilSummary(t *testing.T) {
	// Caller path uses nil; narrator would fall back to single track,
	// but the build function returning empty signals "no useful data".
	got := buildPostFlightSequence(&recorder.Summary{Name: "empty.sqlite"})
	// Should at minimum have flight-complete + alarm closer.
	if len(got) < 2 {
		t.Errorf("expected at least flight-complete + alarm closer, got %v", got)
	}
	if got[0] != "flight-complete" {
		t.Errorf("first fragment must be flight-complete, got %q", got[0])
	}
}

func TestBuildPostFlightSequence_CriticalAlarm(t *testing.T) {
	s := &recorder.Summary{
		Name:        "flight-44.sqlite",
		DurationSec: 60,
		AlarmCounts: map[string]int{"critical": 1},
	}
	got := buildPostFlightSequence(s)
	found := false
	for _, frag := range got {
		if frag == "critical-alarm-triggered" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected critical-alarm-triggered in sequence: %v", got)
	}
}

// === TTS post-flight ===

func TestBuildPostFlightTTS_Empty(t *testing.T) {
	if got := buildPostFlightTTS("en", nil, nil); got != "" {
		t.Errorf("nil events: got %q want empty", got)
	}
	if got := buildPostFlightTTS("en", []recorder.Event{}, nil); got != "" {
		t.Errorf("empty events: got %q want empty", got)
	}
}

func TestBuildPostFlightTTS_CleanFlight(t *testing.T) {
	events := []recorder.Event{
		{TsMs: 0, Kind: "flight", Name: "armed", Level: "info"},
		{TsMs: 8000, Kind: "flight", Name: "gps-lock-acquired", Level: "info"},
		{TsMs: 60000, Kind: "flight", Name: "peak-distance", Level: "info", Detail: map[string]interface{}{"meters": float64(100)}},
		{TsMs: 90000, Kind: "flight", Name: "peak-distance", Level: "info", Detail: map[string]interface{}{"meters": float64(412)}},
		{TsMs: 120000, Kind: "flight", Name: "peak-altitude", Level: "info", Detail: map[string]interface{}{"meters": float64(95)}},
		{TsMs: 252000, Kind: "flight", Name: "disarmed", Level: "info"},
	}
	got := buildPostFlightTTS("en", events, nil)
	want := "Flight complete. 4 minutes 12 seconds. Peak distance 412 meters. Peak altitude 95 meters."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestBuildPostFlightTTS_PortugueseClean(t *testing.T) {
	events := []recorder.Event{
		{TsMs: 0, Kind: "flight", Name: "armed", Level: "info"},
		{TsMs: 90000, Kind: "flight", Name: "peak-distance", Level: "info", Detail: map[string]interface{}{"meters": float64(412)}},
		{TsMs: 252000, Kind: "flight", Name: "disarmed", Level: "info"},
	}
	got := buildPostFlightTTS("pt", events, nil)
	want := "Voo concluído. 4 minutos 12 segundos. Distância máxima 412 metros."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestBuildPostFlightTTS_WithRTHAndBatteryLow(t *testing.T) {
	events := []recorder.Event{
		{TsMs: 0, Kind: "flight", Name: "armed", Level: "info"},
		{TsMs: 100000, Kind: "flight", Name: "peak-distance", Level: "info", Detail: map[string]interface{}{"meters": float64(300)}},
		{TsMs: 200000, Kind: "flight", Name: "battery-low", Level: "warning"},
		{TsMs: 230000, Kind: "flight", Name: "rth-active", Level: "warning"},
		{TsMs: 252000, Kind: "flight", Name: "disarmed", Level: "info"},
	}
	got := buildPostFlightTTS("en", events, nil)
	want := "Flight complete. 4 minutes 12 seconds. Peak distance 300 meters. Return to home triggered. Battery low at 3 minutes 20 seconds."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestBuildPostFlightTTS_FailsafeWins(t *testing.T) {
	events := []recorder.Event{
		{TsMs: 0, Kind: "flight", Name: "armed", Level: "info"},
		{TsMs: 60000, Kind: "flight", Name: "battery-low", Level: "warning"},
		{TsMs: 70000, Kind: "flight", Name: "battery-critical", Level: "critical"},
		{TsMs: 80000, Kind: "flight", Name: "failsafe", Level: "critical"},
		{TsMs: 90000, Kind: "flight", Name: "disarmed", Level: "info"},
	}
	got := buildPostFlightTTS("en", events, nil)
	wantContains := []string{"Failsafe triggered", "Battery critical at 1 minute 10 seconds"}
	for _, w := range wantContains {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
	if strings.Contains(got, "Battery low") {
		t.Errorf("should suppress 'Battery low' when 'Battery critical' fired: %q", got)
	}
}

// stubGeo is a test-only GeoLookup that returns a predetermined
// name regardless of position. Used to verify the narrator's
// enrichment plumbing without needing a real sqlite database.
type stubGeo struct {
	name string
}

func (s stubGeo) NearestName(_, _ float64) string { return s.name }

func TestBuildPostFlightTTS_GeoEnrichment(t *testing.T) {
	events := []recorder.Event{
		{TsMs: 0, Kind: "flight", Name: "armed", Level: "info"},
		{TsMs: 90000, Kind: "flight", Name: "peak-distance", Level: "info",
			Detail: map[string]interface{}{
				"meters": float64(412),
				"lat":    float64(-23.20),
				"lon":    float64(-47.29),
			}},
		{TsMs: 120000, Kind: "flight", Name: "peak-altitude", Level: "info",
			Detail: map[string]interface{}{
				"meters": float64(95),
				"lat":    float64(-23.20),
				"lon":    float64(-47.29),
			}},
		{TsMs: 252000, Kind: "flight", Name: "disarmed", Level: "info"},
	}
	got := buildPostFlightTTS("en", events, stubGeo{name: "Vila Industrial"})
	want := "Flight complete. 4 minutes 12 seconds. Peak distance 412 meters near Vila Industrial. Peak altitude 95 meters over Vila Industrial."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestBuildPostFlightTTS_GeoMissingPos(t *testing.T) {
	// Old recordings pre-date lat/lon enrichment of peak events.
	// Should fall back to no-location phrasing even when geo is set.
	events := []recorder.Event{
		{TsMs: 0, Kind: "flight", Name: "armed", Level: "info"},
		{TsMs: 90000, Kind: "flight", Name: "peak-distance", Level: "info",
			Detail: map[string]interface{}{"meters": float64(412)}},
		{TsMs: 252000, Kind: "flight", Name: "disarmed", Level: "info"},
	}
	got := buildPostFlightTTS("en", events, stubGeo{name: "Should Not Appear"})
	if strings.Contains(got, "Should Not Appear") {
		t.Errorf("should not enrich when lat/lon missing: %q", got)
	}
	if !strings.Contains(got, "Peak distance 412 meters.") {
		t.Errorf("expected unenriched form: %q", got)
	}
}

func TestBuildPostFlightTTS_GeoEmptyName(t *testing.T) {
	// When the lookup returns "" (no place in threshold), the
	// narrator should omit the location phrase rather than say
	// "near nothing."
	events := []recorder.Event{
		{TsMs: 0, Kind: "flight", Name: "armed", Level: "info"},
		{TsMs: 90000, Kind: "flight", Name: "peak-distance", Level: "info",
			Detail: map[string]interface{}{
				"meters": float64(412),
				"lat":    float64(-23.20),
				"lon":    float64(-47.29),
			}},
		{TsMs: 252000, Kind: "flight", Name: "disarmed", Level: "info"},
	}
	got := buildPostFlightTTS("en", events, stubGeo{name: ""})
	if !strings.Contains(got, "Peak distance 412 meters.") {
		t.Errorf("expected unenriched form when geo returns empty: %q", got)
	}
}
