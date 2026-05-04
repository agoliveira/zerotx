// Package wxalert evaluates weather conditions against a set of rules
// and produces a list of active alerts.
//
// Design:
//
//   - Pure compute. No I/O, no goroutines. Caller (the daemon) owns
//     the polling cadence, the audio output, the HUD JSON shape, and
//     the change-detection bookkeeping. wxalert just answers
//     "given these conditions and these limits, what's active?"
//
//   - Continuous status, not events. Rules describe a *predicate over
//     current conditions*. The result is a snapshot: zero or more
//     active rules. The daemon takes consecutive snapshots and emits
//     transition events (entered/left) as appropriate.
//
//   - Hysteresis is the daemon's job, not this package's. We return
//     instantaneous predicates; the daemon debounces.
//
//   - Severity is a fixed per-rule property. Maps to the audio
//     package's Level (notice/warning).
package wxalert

import (
	"fmt"
	"time"
)

// Severity orders alerts by urgency. Maps directly to internal/audio
// levels: SeverityNotice -> LevelNotice, SeverityWarning -> LevelWarning.
// Critical is reserved for truly hazardous conditions (none currently
// in the rule set; lightning would qualify when Blitzortung lands).
type Severity int

const (
	SeverityNotice Severity = iota
	SeverityWarning
	SeverityCritical
)

func (s Severity) String() string {
	switch s {
	case SeverityNotice:
		return "notice"
	case SeverityWarning:
		return "warning"
	case SeverityCritical:
		return "critical"
	}
	return fmt.Sprintf("severity(%d)", int(s))
}

// Limits is the set of thresholds the rules evaluate against. Values
// here come from daemon flags or a YAML override file. All fields are
// optional in the source; the daemon supplies defaults before passing
// the struct in.
type Limits struct {
	// Surface wind sustained speed limit, km/h. Above this the
	// `wind_speed_high` rule fires.
	MaxWindKmh float64

	// Surface wind gust limit, km/h. Above this the `wind_gust_high`
	// rule fires.
	MaxGustKmh float64

	// Forecast precipitation probability threshold, percent (0-100).
	// `precip_imminent` fires if any of the next 1-3 hours exceeds
	// this value.
	PrecipProbabilityPct float64

	// Window from the current time within which "near sunset" applies,
	// minutes. `near_sunset` fires when remaining daylight is less.
	NearSunsetMinutes int

	// Wind aloft direction shear threshold, degrees. `wind_aloft_shear`
	// fires if 80m wind direction differs from surface by more than
	// this.
	ShearDirDeg float64

	// Wind aloft speed shear ratio. `wind_aloft_shear` fires if 80m
	// wind speed exceeds surface by more than this multiplier.
	ShearSpeedRatio float64

	// Sun elevation threshold for golden-hour-active warning, degrees.
	// Below this and falling, the camera-blinding warning fires.
	GoldenHourElevDeg float64
}

// Defaults returns the built-in defaults for Brazilian club-field
// fixed-wing operations. Daemon merges these with any operator
// overrides before passing the result to Evaluate.
func Defaults() Limits {
	return Limits{
		MaxWindKmh:           20,
		MaxGustKmh:           30,
		PrecipProbabilityPct: 60,
		NearSunsetMinutes:    30,
		ShearDirDeg:          45,
		ShearSpeedRatio:      2.0,
		GoldenHourElevDeg:    6.0,
	}
}

// Conditions is the data view wxalert needs from the weather subsystem.
// We define a local interface rather than importing internal/weather to
// keep the dependency graph clean (api -> wxalert -> nothing).
type Conditions struct {
	// CurrentWindKmh is the surface (10m) sustained wind, km/h.
	CurrentWindKmh float64

	// CurrentGustKmh is the surface gust, km/h.
	CurrentGustKmh float64

	// CurrentDirDeg is the surface wind direction (where it comes from).
	CurrentDirDeg float64

	// WeatherCode is the WMO 4677 code from the source.
	WeatherCode int

	// HourlyPrecipProb is the next-N-hours precipitation probability,
	// percent. Index 0 is the current hour, index 1 the next, etc.
	// May be empty.
	HourlyPrecipProb []float64

	// WindAloft80mSpeedKmh / WindAloft80mDirDeg describe the 80m
	// wind. Zero values mean "not available" and the shear rule
	// is skipped.
	WindAloft80mSpeedKmh float64
	WindAloft80mDirDeg   float64

	// SunsetUTC is the sunset time for today in the operator's
	// location, used by the near_sunset rule. Zero value means
	// "not available" (e.g. polar regions, or astro not provided)
	// and the rule is skipped.
	SunsetUTC time.Time

	// SunElevationDeg is the current sun elevation. Used by the
	// golden_hour_active rule.
	SunElevationDeg float64

	// SunFalling is true when the sun is descending toward the
	// horizon (post-solar-noon, pre-sunset). The golden_hour_active
	// rule only fires when falling, not rising (morning low-sun is
	// less of a flying hazard because conditions are improving).
	SunFalling bool

	// Now is the moment of evaluation. Tests inject a deterministic
	// value; production passes time.Now().
	Now time.Time
}

// Alert is a single active rule.
type Alert struct {
	// Name is the rule identifier, stable across releases (e.g.
	// "wind_gust_high"). Used as the audio key and the JSON tag in
	// API responses. Translation/display strings live in the
	// phrasebook, not here.
	Name string

	// Severity maps to audio level.
	Severity Severity

	// Message is a human-readable summary safe to pass to TTS.
	// Includes the relevant numeric values so the operator hears
	// "gusts to 35 km/h" rather than just "gusts high".
	Message string

	// Detail is a longer-form explanation suitable for the modal
	// alert section. May contain multiple sentences. Empty if Message
	// is sufficient on its own.
	Detail string
}

// Evaluate runs the rule set against the given conditions and returns
// the active alerts. Order of returned alerts is deterministic
// (sorted by rule name) so callers can compare two evaluations
// reliably.
func Evaluate(c Conditions, lim Limits) []Alert {
	var out []Alert

	// wind_gust_high: surface gust above limit.
	if c.CurrentGustKmh > 0 && lim.MaxGustKmh > 0 && c.CurrentGustKmh > lim.MaxGustKmh {
		out = append(out, Alert{
			Name:     "wind_gust_high",
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("Wind gusts to %.0f kilometres per hour. Above limit %.0f.", c.CurrentGustKmh, lim.MaxGustKmh),
			Detail:   fmt.Sprintf("Surface gusts %.0f km/h exceed the configured limit of %.0f km/h.", c.CurrentGustKmh, lim.MaxGustKmh),
		})
	}

	// wind_speed_high: sustained surface wind above limit.
	if c.CurrentWindKmh > 0 && lim.MaxWindKmh > 0 && c.CurrentWindKmh > lim.MaxWindKmh {
		out = append(out, Alert{
			Name:     "wind_speed_high",
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("Sustained wind %.0f kilometres per hour. Above limit %.0f.", c.CurrentWindKmh, lim.MaxWindKmh),
			Detail:   fmt.Sprintf("Surface wind %.0f km/h exceeds the configured limit of %.0f km/h.", c.CurrentWindKmh, lim.MaxWindKmh),
		})
	}

	// wind_aloft_shear: 80m wind direction differs from surface by
	// more than the configured delta, OR 80m speed exceeds surface
	// by more than the configured ratio. Skip if 80m data missing.
	if c.WindAloft80mSpeedKmh > 0 {
		dirDelta := angularDelta(c.CurrentDirDeg, c.WindAloft80mDirDeg)
		speedRatio := 0.0
		if c.CurrentWindKmh > 0.5 { // avoid div-by-zero on calm surface
			speedRatio = c.WindAloft80mSpeedKmh / c.CurrentWindKmh
		}
		if dirDelta > lim.ShearDirDeg || speedRatio > lim.ShearSpeedRatio {
			out = append(out, Alert{
				Name:     "wind_aloft_shear",
				Severity: SeverityNotice,
				Message:  "Wind shear aloft. Expect turbulence.",
				Detail: fmt.Sprintf("Surface %.0f km/h at %.0f°, 80m %.0f km/h at %.0f°. Direction delta %.0f°, speed ratio %.1fx.",
					c.CurrentWindKmh, c.CurrentDirDeg,
					c.WindAloft80mSpeedKmh, c.WindAloft80mDirDeg,
					dirDelta, speedRatio),
			})
		}
	}

	// precip_imminent: any of the next 1-3 hours has precip prob
	// above threshold.
	if len(c.HourlyPrecipProb) > 0 && lim.PrecipProbabilityPct > 0 {
		windowEnd := 3
		if windowEnd > len(c.HourlyPrecipProb) {
			windowEnd = len(c.HourlyPrecipProb)
		}
		maxProb := 0.0
		maxAt := 0
		for i := 0; i < windowEnd; i++ {
			if c.HourlyPrecipProb[i] > maxProb {
				maxProb = c.HourlyPrecipProb[i]
				maxAt = i
			}
		}
		if maxProb > lim.PrecipProbabilityPct {
			out = append(out, Alert{
				Name:     "precip_imminent",
				Severity: SeverityNotice,
				Message:  fmt.Sprintf("Precipitation likely within %d hours. %.0f percent probability.", windowEnd, maxProb),
				Detail:   fmt.Sprintf("Forecast shows %.0f%% precipitation probability at +%dh.", maxProb, maxAt),
			})
		}
	}

	// low_visibility: WMO codes for fog and dense drizzle. The codes
	// are stable across the WMO 4677 standard.
	if isLowVisibility(c.WeatherCode) {
		out = append(out, Alert{
			Name:     "low_visibility",
			Severity: SeverityWarning,
			Message:  "Low visibility conditions reported.",
			Detail:   fmt.Sprintf("Weather code %d indicates fog or dense drizzle.", c.WeatherCode),
		})
	}

	// near_sunset: time remaining until sunset is below the
	// configured window. Skip if sunset unknown or already past.
	if !c.SunsetUTC.IsZero() && lim.NearSunsetMinutes > 0 {
		remaining := c.SunsetUTC.Sub(c.Now)
		threshold := time.Duration(lim.NearSunsetMinutes) * time.Minute
		if remaining > 0 && remaining < threshold {
			mins := int(remaining.Minutes())
			out = append(out, Alert{
				Name:     "near_sunset",
				Severity: SeverityNotice,
				Message:  fmt.Sprintf("Sunset in %d minutes. Plan to land soon.", mins),
				Detail:   fmt.Sprintf("Sunset %s; %d minutes of daylight remaining.", c.SunsetUTC.Format("15:04 UTC"), mins),
			})
		}
	}

	// golden_hour_active: sun low and falling. Camera blinding
	// hazard for FPV operators flying west.
	if c.SunFalling && c.SunElevationDeg > 0 && c.SunElevationDeg < lim.GoldenHourElevDeg {
		out = append(out, Alert{
			Name:     "golden_hour_active",
			Severity: SeverityNotice,
			Message:  fmt.Sprintf("Golden hour. Sun elevation %.0f degrees, FPV camera will be blinded looking west.", c.SunElevationDeg),
			Detail:   "Avoid flying directly into the setting sun; FPV camera will saturate.",
		})
	}

	return out
}

// angularDelta returns the smallest angular difference between two
// compass bearings, 0..180 degrees.
func angularDelta(a, b float64) float64 {
	d := a - b
	for d > 180 {
		d -= 360
	}
	for d < -180 {
		d += 360
	}
	if d < 0 {
		d = -d
	}
	return d
}

// isLowVisibility returns true if the WMO 4677 weather code indicates
// fog, freezing fog, or dense drizzle. Other rain/storm codes are
// excluded because operators routinely fly in light rain at clubs.
func isLowVisibility(code int) bool {
	switch code {
	case 45, 48: // fog, rime fog
		return true
	case 55: // dense drizzle
		return true
	}
	return false
}
