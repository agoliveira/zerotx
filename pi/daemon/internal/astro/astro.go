// Package astro provides pure-compute astronomical data: Sun position,
// named daylight events (sunrise/sunset, civil/nautical/astronomical
// twilight, golden hour bounds), and Moon phase. No internet, no
// dependencies beyond the Go standard library.
//
// Design notes:
//
//   - Targets minute-accuracy for the next several decades. The
//     simplified Meeus-style formulas implemented here are accurate
//     to ~0.01° on Sun position and ~1 minute on rise/set events for
//     typical mid-latitude observers within +/- 100 years of J2000.
//     Polar regions degrade gracefully (events return time.Time{}
//     and the SunAlwaysUp / SunAlwaysDown booleans are set).
//
//   - All inputs and outputs are absolute time.Time values. Callers
//     compute "minutes until X" themselves (cleaner than baking in
//     "near midnight" semantics).
//
//   - All angles in degrees on the public API. Internal computations
//     use radians but never leak.
//
//   - Azimuth convention: 0 = North, 90 = East, 180 = South, 270 = West
//     (clockwise from north, the surveyor / aviation convention).
//
//   - The Sun() function computes events for the date that the input
//     time falls on, at the observer's longitude. Edge cases at
//     local-midnight boundary use the input timezone.
package astro

import (
	"math"
	"time"
)

// SunInfo carries named daylight events for a given local day at an
// observer location. Times are absolute (UTC); zero values indicate
// the event does not occur on this day at this latitude.
type SunInfo struct {
	// Refraction-corrected horizon crossings (sun's apparent upper
	// limb at horizon, accounting for atmospheric refraction).
	Sunrise   time.Time
	Sunset    time.Time
	SolarNoon time.Time

	// Twilight bounds: sun's center at the named depression below
	// horizon. Civil = -6, Nautical = -12, Astronomical = -18.
	CivilDawn    time.Time
	CivilDusk    time.Time
	NauticalDawn time.Time
	NauticalDusk time.Time
	AstroDawn    time.Time
	AstroDusk    time.Time

	// Golden hour: sun ascends through +6 elevation (morning) /
	// descends through +6 elevation (evening). Other ends coincide
	// with Sunrise / Sunset respectively.
	MorningGoldenEnd   time.Time
	EveningGoldenBegin time.Time

	// DayLength is sunset - sunrise. Zero if no rise/set.
	DayLength time.Duration

	// Polar conditions. SunAlwaysUp means the sun never crosses the
	// horizon (refraction-corrected) during this day; sunset/sunrise
	// are zero. SunAlwaysDown is the opposite.
	SunAlwaysUp   bool
	SunAlwaysDown bool
}

// SunPosition is the instantaneous topocentric position of the Sun
// for an observer at the given coordinates.
type SunPosition struct {
	// AzimuthDeg: 0 = North, 90 = East, 180 = South, 270 = West.
	AzimuthDeg float64
	// ElevationDeg: 0 = horizon, 90 = zenith. Negative below horizon.
	ElevationDeg float64
	// Equatorial coordinates (informational).
	DeclinationDeg float64
	RightAscDeg    float64
}

// MoonInfo carries phase information for the Moon. Position-on-sky
// is omitted for v1; add when something needs it.
type MoonInfo struct {
	// PhaseAngleDeg: 0 = new, 90 = first quarter, 180 = full,
	// 270 = last quarter. Range [0, 360).
	PhaseAngleDeg float64
	// Illumination is the fraction of the Moon's disk lit by the Sun
	// as seen from Earth. Range [0, 1].
	Illumination float64
	// PhaseName is the conventional eight-segment label.
	PhaseName string
}

// Standard altitudes (degrees) used for named events. The -0.833 for
// rise/set accounts for atmospheric refraction (~34') plus the Sun's
// apparent angular semi-diameter (~16').
const (
	altSunRiseSet  = -0.833
	altCivil       = -6.0
	altNautical    = -12.0
	altAstro       = -18.0
	altGoldenHour  = 6.0
)

// SunPos returns the topocentric Sun position at instant t for
// an observer at (latDeg, lonDeg). Longitude is east-positive.
func SunPos(t time.Time, latDeg, lonDeg float64) SunPosition {
	jd := toJulianDate(t)
	n := jd - 2451545.0 // days since J2000.0

	L := mod360(280.460 + 0.9856474*n)            // mean longitude
	g := mod360(357.528 + 0.9856003*n)            // mean anomaly
	gRad := degToRad(g)
	lambda := L + 1.915*math.Sin(gRad) + 0.020*math.Sin(2*gRad) // ecliptic longitude
	eps := 23.439 - 0.0000004*n                                 // obliquity

	lambdaRad := degToRad(lambda)
	epsRad := degToRad(eps)

	alpha := math.Atan2(math.Cos(epsRad)*math.Sin(lambdaRad), math.Cos(lambdaRad))
	delta := math.Asin(math.Sin(epsRad) * math.Sin(lambdaRad))

	// Greenwich mean sidereal time, hours.
	gmst := math.Mod(18.697374558+24.06570982441908*n, 24)
	if gmst < 0 {
		gmst += 24
	}
	// Local sidereal time, then hour angle of the sun (radians).
	lst := gmst + lonDeg/15.0
	H := degToRad(lst*15.0) - alpha

	latRad := degToRad(latDeg)
	sinAlt := math.Sin(latRad)*math.Sin(delta) + math.Cos(latRad)*math.Cos(delta)*math.Cos(H)
	altRad := math.Asin(clamp(sinAlt, -1, 1))

	// Azimuth measured from North, clockwise. Use atan2 form to get
	// the right quadrant directly.
	cosAlt := math.Cos(altRad)
	if cosAlt < 1e-9 {
		// Sun at zenith / nadir: azimuth undefined, return 0.
		return SunPosition{
			AzimuthDeg:     0,
			ElevationDeg:   radToDeg(altRad),
			DeclinationDeg: radToDeg(delta),
			RightAscDeg:    mod360(radToDeg(alpha)),
		}
	}
	sinAz := -math.Cos(delta) * math.Sin(H) / cosAlt
	cosAz := (math.Sin(delta) - math.Sin(latRad)*sinAlt) / (math.Cos(latRad) * cosAlt)
	az := math.Atan2(sinAz, cosAz)
	if az < 0 {
		az += 2 * math.Pi
	}

	return SunPosition{
		AzimuthDeg:     radToDeg(az),
		ElevationDeg:   radToDeg(altRad),
		DeclinationDeg: radToDeg(delta),
		RightAscDeg:    mod360(radToDeg(alpha)),
	}
}

// Sun returns named daylight events for the calendar date that t falls
// on, at the observer's location. The "calendar date" is determined
// from t in its own location (t.Year/Month/Day in t's timezone), so
// callers can pass a local-time t for the intuitive day boundary.
func Sun(t time.Time, latDeg, lonDeg float64) SunInfo {
	// Anchor at noon UTC of the given date; iterate once to refine
	// to the actual solar noon for the observer.
	dateStartUTC := time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, time.UTC)
	jdNoon := toJulianDate(dateStartUTC)

	delta, eqTimeMin := sunDeclinationAndEqTime(jdNoon)

	// Solar noon UTC, in minutes past 00:00 UTC of the date.
	// Negative lonDeg (west) shifts noon later in UTC.
	solarNoonMin := 720 - 4*lonDeg - eqTimeMin
	noonUTC := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).
		Add(time.Duration(solarNoonMin*60) * time.Second)

	info := SunInfo{SolarNoon: noonUTC}

	latRad := degToRad(latDeg)
	deltaRad := degToRad(delta)
	sinLat := math.Sin(latRad)
	cosLat := math.Cos(latRad)
	sinDelta := math.Sin(deltaRad)
	cosDelta := math.Cos(deltaRad)

	// hourAngleMinutes returns the time offset (minutes from solar noon)
	// at which the sun's center reaches altitude altDeg, plus a flag
	// for whether such an event occurs on this date.
	hourAngleMinutes := func(altDeg float64) (float64, bool) {
		cosH := (math.Sin(degToRad(altDeg)) - sinLat*sinDelta) / (cosLat * cosDelta)
		if cosH > 1 {
			return 0, false // sun never reaches this altitude (always lower)
		}
		if cosH < -1 {
			return 0, true // sun never drops below this altitude (event "always", flag still ok)
		}
		hRad := math.Acos(cosH)
		// hour angle radians -> degrees -> minutes (4 min per degree)
		return radToDeg(hRad) * 4, true
	}

	// Helper: compute (dawn, dusk) absolute times for an altitude threshold.
	// Returns zero times if the event doesn't occur on this date.
	eventTimes := func(altDeg float64) (dawn, dusk time.Time, occurs bool) {
		cosH := (math.Sin(degToRad(altDeg)) - sinLat*sinDelta) / (cosLat * cosDelta)
		if cosH > 1 || cosH < -1 {
			return time.Time{}, time.Time{}, false
		}
		hMin := radToDeg(math.Acos(cosH)) * 4
		dawn = noonUTC.Add(-time.Duration(hMin*60) * time.Second)
		dusk = noonUTC.Add(time.Duration(hMin*60) * time.Second)
		return dawn, dusk, true
	}

	// Sunrise / sunset.
	if rise, set, ok := eventTimes(altSunRiseSet); ok {
		info.Sunrise = rise
		info.Sunset = set
		info.DayLength = set.Sub(rise)
	} else {
		// No rise/set: determine which polar regime by checking sun
		// elevation at solar noon.
		if _, polarUp := hourAngleMinutes(altSunRiseSet); polarUp && sinLat*sinDelta > 0 {
			// Same-hemisphere summer-side configuration: sun stays up.
			info.SunAlwaysUp = true
			info.DayLength = 24 * time.Hour
		} else {
			info.SunAlwaysDown = true
		}
	}

	if dawn, dusk, ok := eventTimes(altCivil); ok {
		info.CivilDawn = dawn
		info.CivilDusk = dusk
	}
	if dawn, dusk, ok := eventTimes(altNautical); ok {
		info.NauticalDawn = dawn
		info.NauticalDusk = dusk
	}
	if dawn, dusk, ok := eventTimes(altAstro); ok {
		info.AstroDawn = dawn
		info.AstroDusk = dusk
	}
	if morningEnd, eveningBegin, ok := eventTimes(altGoldenHour); ok {
		info.MorningGoldenEnd = morningEnd
		info.EveningGoldenBegin = eveningBegin
	}

	return info
}

// Moon returns the lunar phase data at instant t. Position on the
// celestial sphere is not computed in v1.
func Moon(t time.Time) MoonInfo {
	jd := toJulianDate(t)
	// Reference new moon: 2000-01-06 18:14 UT, JD 2451550.260 (computed
	// from the date plus 18.233/24 fractional day). Synodic month length
	// 29.530589 days. Accuracy is sufficient for phase-name labelling
	// across the eight-segment scheme; not for predicting exact eclipse
	// timing.
	const synodicMonth = 29.530589
	const refNewMoonJD = 2451550.260

	cycles := (jd - refNewMoonJD) / synodicMonth
	cycles -= math.Floor(cycles)
	if cycles < 0 {
		cycles += 1
	}
	phase := cycles * 360
	illum := (1 - math.Cos(degToRad(phase))) / 2

	var name string
	switch {
	case phase < 22.5 || phase >= 337.5:
		name = "new"
	case phase < 67.5:
		name = "waxing crescent"
	case phase < 112.5:
		name = "first quarter"
	case phase < 157.5:
		name = "waxing gibbous"
	case phase < 202.5:
		name = "full"
	case phase < 247.5:
		name = "waning gibbous"
	case phase < 292.5:
		name = "last quarter"
	default:
		name = "waning crescent"
	}

	return MoonInfo{
		PhaseAngleDeg: phase,
		Illumination:  illum,
		PhaseName:     name,
	}
}

// sunDeclinationAndEqTime returns the Sun's declination (degrees) and
// the equation of time (minutes) at the given Julian Date. The eqTime
// is the offset between mean solar time and apparent solar time:
// solarNoonUTC = 12:00 - lon/15 - eqTime/60 (hours).
func sunDeclinationAndEqTime(jd float64) (deltaDeg float64, eqTimeMin float64) {
	n := jd - 2451545.0
	L := mod360(280.460 + 0.9856474*n)
	g := mod360(357.528 + 0.9856003*n)
	gRad := degToRad(g)
	lambda := L + 1.915*math.Sin(gRad) + 0.020*math.Sin(2*gRad)
	eps := 23.439 - 0.0000004*n

	lambdaRad := degToRad(lambda)
	epsRad := degToRad(eps)

	alpha := radToDeg(math.Atan2(math.Cos(epsRad)*math.Sin(lambdaRad), math.Cos(lambdaRad)))
	delta := radToDeg(math.Asin(math.Sin(epsRad) * math.Sin(lambdaRad)))

	// Equation of time in minutes (4 min per degree of L - alpha).
	// Aberration correction (-0.0057183 deg) is the small fixed offset.
	eq := 4 * (mod360PlusMinus180(L - 0.0057183 - alpha))

	return delta, eq
}

// toJulianDate converts a Go time.Time to Julian Date. Computation
// is done in UTC regardless of the time's zone.
func toJulianDate(t time.Time) float64 {
	// Unix epoch 1970-01-01T00:00:00Z corresponds to JD 2440587.5.
	return float64(t.UTC().UnixNano())/86400e9 + 2440587.5
}

// mod360 normalises x to [0, 360).
func mod360(x float64) float64 {
	x = math.Mod(x, 360)
	if x < 0 {
		x += 360
	}
	return x
}

// mod360PlusMinus180 normalises x to (-180, 180].
func mod360PlusMinus180(x float64) float64 {
	x = math.Mod(x, 360)
	if x > 180 {
		x -= 360
	} else if x <= -180 {
		x += 360
	}
	return x
}

func degToRad(d float64) float64 { return d * math.Pi / 180 }
func radToDeg(r float64) float64 { return r * 180 / math.Pi }

func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
