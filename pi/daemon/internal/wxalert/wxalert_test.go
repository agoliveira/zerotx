package wxalert

import (
	"strings"
	"testing"
	"time"
)

// baseConditions returns a "calm sunny day" Conditions value that
// triggers no rules. Tests then mutate one field at a time to assert
// that the corresponding rule fires (or doesn't).
func baseConditions() Conditions {
	now := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC) // 14:00 UTC, mid-day
	return Conditions{
		CurrentWindKmh:       8,
		CurrentGustKmh:       12,
		CurrentDirDeg:        90,
		WeatherCode:          1, // mainly clear
		HourlyPrecipProb:     []float64{5, 5, 5, 5, 10, 10},
		WindAloft80mSpeedKmh: 10,
		WindAloft80mDirDeg:   95,
		SunsetUTC:            time.Date(2026, 5, 4, 21, 0, 0, 0, time.UTC), // 7h from now
		SunElevationDeg:      55,
		SunFalling:           false,
		Now:                  now,
	}
}

func TestEvaluate_QuietConditions_NoAlerts(t *testing.T) {
	out := Evaluate(baseConditions(), Defaults())
	if len(out) != 0 {
		t.Errorf("expected no alerts on calm day, got %d: %+v", len(out), out)
	}
}

func TestEvaluate_GustHigh_Fires(t *testing.T) {
	c := baseConditions()
	c.CurrentGustKmh = 35
	out := Evaluate(c, Defaults())
	if !hasAlert(out, "wind_gust_high") {
		t.Errorf("wind_gust_high should fire at 35 km/h gust (limit 30); got %+v", out)
	}
	a := findAlert(out, "wind_gust_high")
	if a.Severity != SeverityWarning {
		t.Errorf("severity = %v, want warning", a.Severity)
	}
	if !strings.Contains(a.Message, "35") {
		t.Errorf("message should include the gust value 35: %q", a.Message)
	}
}

func TestEvaluate_GustExactlyAtLimit_DoesNotFire(t *testing.T) {
	c := baseConditions()
	c.CurrentGustKmh = 30 // exactly the limit
	out := Evaluate(c, Defaults())
	if hasAlert(out, "wind_gust_high") {
		t.Errorf("wind_gust_high should NOT fire at exactly the limit")
	}
}

func TestEvaluate_WindHigh_Fires(t *testing.T) {
	c := baseConditions()
	c.CurrentWindKmh = 25
	out := Evaluate(c, Defaults())
	if !hasAlert(out, "wind_speed_high") {
		t.Errorf("wind_speed_high should fire at 25 km/h sustained (limit 20); got %+v", out)
	}
}

func TestEvaluate_AloftShear_Direction_Fires(t *testing.T) {
	c := baseConditions()
	c.CurrentDirDeg = 90
	c.WindAloft80mDirDeg = 180 // 90° delta, > 45°
	out := Evaluate(c, Defaults())
	if !hasAlert(out, "wind_aloft_shear") {
		t.Errorf("wind_aloft_shear should fire on 90° direction delta; got %+v", out)
	}
}

func TestEvaluate_AloftShear_Speed_Fires(t *testing.T) {
	c := baseConditions()
	c.CurrentWindKmh = 5
	c.WindAloft80mSpeedKmh = 12 // 2.4x ratio, > 2.0
	out := Evaluate(c, Defaults())
	if !hasAlert(out, "wind_aloft_shear") {
		t.Errorf("wind_aloft_shear should fire on speed ratio 2.4x; got %+v", out)
	}
}

func TestEvaluate_AloftShear_NoAloftData_Skipped(t *testing.T) {
	c := baseConditions()
	c.CurrentDirDeg = 0
	c.WindAloft80mSpeedKmh = 0 // not available
	c.WindAloft80mDirDeg = 180 // would be huge delta if speed were nonzero
	out := Evaluate(c, Defaults())
	if hasAlert(out, "wind_aloft_shear") {
		t.Errorf("wind_aloft_shear should be skipped when 80m speed = 0")
	}
}

func TestEvaluate_AngularDelta_WrapAround(t *testing.T) {
	// 350° vs 10° is a 20° delta, not 340°.
	if d := angularDelta(350, 10); d != 20 {
		t.Errorf("angularDelta(350, 10) = %v, want 20", d)
	}
	if d := angularDelta(10, 350); d != 20 {
		t.Errorf("angularDelta(10, 350) = %v, want 20", d)
	}
	if d := angularDelta(0, 180); d != 180 {
		t.Errorf("angularDelta(0, 180) = %v, want 180", d)
	}
}

func TestEvaluate_PrecipImminent_Fires(t *testing.T) {
	c := baseConditions()
	c.HourlyPrecipProb = []float64{20, 75, 50, 30, 20, 10} // hour +1 is 75%
	out := Evaluate(c, Defaults())
	if !hasAlert(out, "precip_imminent") {
		t.Errorf("precip_imminent should fire on 75%% prob within window; got %+v", out)
	}
}

func TestEvaluate_PrecipImminent_OutsideWindow_DoesNotFire(t *testing.T) {
	c := baseConditions()
	// Hour +4 is 75%, but window is only 3 hours.
	c.HourlyPrecipProb = []float64{20, 30, 30, 30, 75}
	out := Evaluate(c, Defaults())
	if hasAlert(out, "precip_imminent") {
		t.Errorf("precip_imminent should NOT fire when high prob is past window")
	}
}

func TestEvaluate_LowVisibility_Fog(t *testing.T) {
	c := baseConditions()
	c.WeatherCode = 45 // fog
	out := Evaluate(c, Defaults())
	if !hasAlert(out, "low_visibility") {
		t.Errorf("low_visibility should fire on code 45 (fog)")
	}
}

func TestEvaluate_LowVisibility_LightRain_DoesNotFire(t *testing.T) {
	c := baseConditions()
	c.WeatherCode = 61 // light rain
	out := Evaluate(c, Defaults())
	if hasAlert(out, "low_visibility") {
		t.Errorf("low_visibility should NOT fire on code 61 (light rain)")
	}
}

func TestEvaluate_NearSunset_Fires(t *testing.T) {
	c := baseConditions()
	c.Now = time.Date(2026, 5, 4, 20, 45, 0, 0, time.UTC)
	c.SunsetUTC = time.Date(2026, 5, 4, 21, 5, 0, 0, time.UTC) // 20 min away
	out := Evaluate(c, Defaults())
	if !hasAlert(out, "near_sunset") {
		t.Errorf("near_sunset should fire when sunset is 20 min away (window 30); got %+v", out)
	}
}

func TestEvaluate_NearSunset_PastSunset_DoesNotFire(t *testing.T) {
	c := baseConditions()
	c.Now = time.Date(2026, 5, 4, 21, 30, 0, 0, time.UTC) // already past
	c.SunsetUTC = time.Date(2026, 5, 4, 21, 0, 0, 0, time.UTC)
	out := Evaluate(c, Defaults())
	if hasAlert(out, "near_sunset") {
		t.Errorf("near_sunset should NOT fire after sunset")
	}
}

func TestEvaluate_NearSunset_NoSunsetData_Skipped(t *testing.T) {
	c := baseConditions()
	c.SunsetUTC = time.Time{}
	out := Evaluate(c, Defaults())
	if hasAlert(out, "near_sunset") {
		t.Errorf("near_sunset should be skipped when sunset unknown")
	}
}

func TestEvaluate_GoldenHourActive_Fires(t *testing.T) {
	c := baseConditions()
	c.SunElevationDeg = 4 // below 6° threshold
	c.SunFalling = true
	out := Evaluate(c, Defaults())
	if !hasAlert(out, "golden_hour_active") {
		t.Errorf("golden_hour_active should fire at elev 4° falling; got %+v", out)
	}
}

func TestEvaluate_GoldenHourActive_Rising_DoesNotFire(t *testing.T) {
	c := baseConditions()
	c.SunElevationDeg = 4
	c.SunFalling = false // morning - not a flying hazard
	out := Evaluate(c, Defaults())
	if hasAlert(out, "golden_hour_active") {
		t.Errorf("golden_hour_active should NOT fire at low rising sun")
	}
}

func TestEvaluate_GoldenHourActive_BelowHorizon_DoesNotFire(t *testing.T) {
	c := baseConditions()
	c.SunElevationDeg = -1 // already set
	c.SunFalling = true
	out := Evaluate(c, Defaults())
	if hasAlert(out, "golden_hour_active") {
		t.Errorf("golden_hour_active should NOT fire when sun is below horizon")
	}
}

func TestEvaluate_MultipleRules_AllReturned(t *testing.T) {
	// Severe weather day: high gusts, high sustained, near sunset.
	c := baseConditions()
	c.CurrentWindKmh = 25
	c.CurrentGustKmh = 40
	c.Now = time.Date(2026, 5, 4, 20, 50, 0, 0, time.UTC)
	c.SunsetUTC = time.Date(2026, 5, 4, 21, 5, 0, 0, time.UTC) // 15 min away
	out := Evaluate(c, Defaults())
	if !hasAlert(out, "wind_gust_high") {
		t.Errorf("missing wind_gust_high")
	}
	if !hasAlert(out, "wind_speed_high") {
		t.Errorf("missing wind_speed_high")
	}
	if !hasAlert(out, "near_sunset") {
		t.Errorf("missing near_sunset")
	}
}

func TestEvaluate_CustomLimits_Honored(t *testing.T) {
	// Tighter gust limit - should fire on conditions Defaults() ignores.
	c := baseConditions()
	c.CurrentGustKmh = 15
	tightLimits := Defaults()
	tightLimits.MaxGustKmh = 10
	out := Evaluate(c, tightLimits)
	if !hasAlert(out, "wind_gust_high") {
		t.Errorf("wind_gust_high should fire at 15 km/h with custom limit 10")
	}
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

func hasAlert(alerts []Alert, name string) bool {
	for _, a := range alerts {
		if a.Name == name {
			return true
		}
	}
	return false
}

func findAlert(alerts []Alert, name string) Alert {
	for _, a := range alerts {
		if a.Name == name {
			return a
		}
	}
	return Alert{}
}
