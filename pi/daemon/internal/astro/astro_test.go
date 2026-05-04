package astro

import (
	"math"
	"testing"
	"time"
)

// Reference values cross-checked against:
//   - NOAA Solar Position Calculator (gml.noaa.gov/grad/solcalc/)
//   - US Naval Observatory data (aa.usno.navy.mil/)
//   - The Astronomical Almanac
//
// Tolerances are deliberately loose: the simplified Meeus formulas
// implemented here target ~1 minute accuracy on rise/set events and
// ~0.01 degree on Sun position. We allow 3 minutes / 0.5 degree for
// safety against rounding differences and reference-value imprecision.

const (
	// Campinas, BR, where the operator lives.
	campinasLat = -22.9099
	campinasLon = -47.0626
)

// ----------------------------------------------------------------------------
// Julian date round-trip and J2000 anchor.
// ----------------------------------------------------------------------------

func TestJulianDate_J2000(t *testing.T) {
	// J2000.0 is defined as 2000-01-01 12:00:00 UTC = JD 2451545.0.
	j2000 := time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)
	got := toJulianDate(j2000)
	want := 2451545.0
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("J2000 JD = %v, want %v", got, want)
	}
}

func TestJulianDate_UnixEpoch(t *testing.T) {
	// 1970-01-01 00:00:00 UTC = JD 2440587.5.
	epoch := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	got := toJulianDate(epoch)
	want := 2440587.5
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("Unix epoch JD = %v, want %v", got, want)
	}
}

func TestJulianDate_TimezoneInvariance(t *testing.T) {
	// Same instant expressed in different zones must produce same JD.
	utc := time.Date(2024, 3, 20, 15, 0, 0, 0, time.UTC)
	brt := time.FixedZone("BRT", -3*3600)
	local := time.Date(2024, 3, 20, 12, 0, 0, 0, brt) // same instant
	if math.Abs(toJulianDate(utc)-toJulianDate(local)) > 1e-9 {
		t.Errorf("JD differs by timezone: utc=%v local=%v",
			toJulianDate(utc), toJulianDate(local))
	}
}

// ----------------------------------------------------------------------------
// Sun position at solar noon — geometric sanity.
// ----------------------------------------------------------------------------

func TestSunPos_Equator_Equinox_Zenith(t *testing.T) {
	// At the equator on the equinox, the Sun is near the zenith at
	// local solar noon. Test against the actual solar-noon time, not
	// 12:00 UTC: the equation of time on March 20 puts solar noon at
	// lon=0 around 12:07 UTC, and 7 minutes off-meridian costs ~1.8
	// degrees of elevation even from the equator. Use Sun()'s own
	// SolarNoon to find the right instant; the residual ~0.15 degree
	// gap from exact zenith is the true equinox/noon time mismatch
	// (the 2024 equinox was at 03:06 UTC, not noon).
	date := time.Date(2024, 3, 20, 12, 0, 0, 0, time.UTC)
	info := Sun(date, 0, 0)
	pos := SunPos(info.SolarNoon, 0, 0)
	if pos.ElevationDeg < 89.5 {
		t.Errorf("equator equinox solar-noon elevation = %.3f, want >= 89.5", pos.ElevationDeg)
	}
}

func TestSunPos_Campinas_Equinox_NoonNorth(t *testing.T) {
	// Campinas is south of the equator; on the autumn equinox at solar
	// noon the Sun is in the northern part of the sky. Elevation
	// should equal ~67 degrees (90 - |lat|).
	// Solar noon at Campinas on 2024-03-20 is around 15:15 UTC
	// (12:15 BRT). We use a window of values within +/- a few minutes
	// and verify the geometry, not the precise time.
	noon := time.Date(2024, 3, 20, 15, 15, 0, 0, time.UTC)
	pos := SunPos(noon, campinasLat, campinasLon)

	wantElev := 90.0 - math.Abs(campinasLat) // ~67.09
	if math.Abs(pos.ElevationDeg-wantElev) > 1.0 {
		t.Errorf("Campinas equinox noon elevation = %.3f, want ~%.3f", pos.ElevationDeg, wantElev)
	}
	// Azimuth: sun in the north, so close to 0 or 360.
	azFromNorth := math.Min(pos.AzimuthDeg, 360-pos.AzimuthDeg)
	if azFromNorth > 5 {
		t.Errorf("Campinas equinox noon azimuth = %.3f, want near 0/360", pos.AzimuthDeg)
	}
}

func TestSunPos_BeforeSunrise_NegativeElevation(t *testing.T) {
	// Pre-dawn at Campinas, sun should be below the horizon.
	preDawn := time.Date(2024, 3, 20, 8, 0, 0, 0, time.UTC) // 05:00 BRT
	pos := SunPos(preDawn, campinasLat, campinasLon)
	if pos.ElevationDeg > 0 {
		t.Errorf("pre-dawn elevation = %.3f, want negative", pos.ElevationDeg)
	}
}

// ----------------------------------------------------------------------------
// Sun events for Campinas (operator location).
// Reference values from NOAA Solar Calculator for 2024-03-20 (autumn
// equinox), allowing 3-minute tolerance.
// ----------------------------------------------------------------------------

func TestSun_Campinas_Equinox_DayLength(t *testing.T) {
	// On equinox, day length is ~12 hours regardless of latitude
	// (atmospheric refraction adds ~5 minutes to apparent day).
	date := time.Date(2024, 3, 20, 12, 0, 0, 0, time.UTC)
	info := Sun(date, campinasLat, campinasLon)

	if info.SunAlwaysUp || info.SunAlwaysDown {
		t.Fatalf("equinox at Campinas should have rise and set, got polar flags")
	}
	hours := info.DayLength.Hours()
	if hours < 11.9 || hours > 12.3 {
		t.Errorf("equinox day length at Campinas = %.3f h, want ~12.0", hours)
	}
}

func TestSun_Campinas_Equinox_SolarNoon(t *testing.T) {
	// Solar noon at Campinas on equinox is around 15:15 UTC.
	date := time.Date(2024, 3, 20, 12, 0, 0, 0, time.UTC)
	info := Sun(date, campinasLat, campinasLon)

	wantNoon := time.Date(2024, 3, 20, 15, 15, 0, 0, time.UTC)
	delta := info.SolarNoon.Sub(wantNoon)
	if delta < -3*time.Minute || delta > 3*time.Minute {
		t.Errorf("solar noon = %v, want ~%v (delta %v)",
			info.SolarNoon.Format("15:04:05"),
			wantNoon.Format("15:04:05"), delta)
	}
}

func TestSun_Campinas_Equinox_RiseSet(t *testing.T) {
	// NOAA Solar Calculator for Campinas (-22.91, -47.06), 2024-03-20:
	// sunrise 06:12 BRT = 09:12 UTC, sunset 18:18 BRT = 21:18 UTC.
	date := time.Date(2024, 3, 20, 12, 0, 0, 0, time.UTC)
	info := Sun(date, campinasLat, campinasLon)

	wantRise := time.Date(2024, 3, 20, 9, 12, 0, 0, time.UTC)
	wantSet := time.Date(2024, 3, 20, 21, 18, 0, 0, time.UTC)

	if d := info.Sunrise.Sub(wantRise); d < -3*time.Minute || d > 3*time.Minute {
		t.Errorf("sunrise = %v, want ~%v (delta %v)",
			info.Sunrise.Format("15:04:05"),
			wantRise.Format("15:04:05"), d)
	}
	if d := info.Sunset.Sub(wantSet); d < -3*time.Minute || d > 3*time.Minute {
		t.Errorf("sunset = %v, want ~%v (delta %v)",
			info.Sunset.Format("15:04:05"),
			wantSet.Format("15:04:05"), d)
	}
}

func TestSun_Campinas_Equinox_RiseSet_Invariants(t *testing.T) {
	// Invariants that don't depend on third-party reference precision:
	//   - Solar noon should bisect sunrise and sunset to within seconds
	//   - Equinox day length is 12h plus refraction (~6-10 min)
	//   - Rise comes before noon, set after, both on the same calendar date
	date := time.Date(2024, 3, 20, 12, 0, 0, 0, time.UTC)
	info := Sun(date, campinasLat, campinasLon)

	mid := info.Sunrise.Add(info.Sunset.Sub(info.Sunrise) / 2)
	if d := mid.Sub(info.SolarNoon); d < -30*time.Second || d > 30*time.Second {
		t.Errorf("solar noon should bisect rise/set: noon=%v midpoint=%v delta=%v",
			info.SolarNoon.Format("15:04:05"),
			mid.Format("15:04:05"), d)
	}
	if h := info.DayLength.Hours(); h < 12.0 || h > 12.25 {
		t.Errorf("equinox day length = %.4f h, want 12.0-12.25 (12h + refraction)", h)
	}
	if !info.Sunrise.Before(info.SolarNoon) || !info.SolarNoon.Before(info.Sunset) {
		t.Errorf("ordering broken: rise=%v noon=%v set=%v",
			info.Sunrise, info.SolarNoon, info.Sunset)
	}
	if info.Sunrise.Day() != 20 || info.Sunset.Day() != 20 {
		t.Errorf("rise/set should fall on March 20 UTC, got rise=%v set=%v",
			info.Sunrise, info.Sunset)
	}
}

func TestSun_Campinas_OrderingInvariants(t *testing.T) {
	// Across a normal mid-latitude day, the named events must occur in
	// the documented order.
	date := time.Date(2024, 6, 21, 12, 0, 0, 0, time.UTC) // SH winter
	info := Sun(date, campinasLat, campinasLon)
	if info.SunAlwaysUp || info.SunAlwaysDown {
		t.Fatalf("Campinas on solstice should still have rise/set")
	}

	// Dawn ordering: astro -> nautical -> civil -> sunrise -> golden end -> noon
	checks := []struct {
		name string
		t1   time.Time
		t2   time.Time
	}{
		{"astro->nautical dawn", info.AstroDawn, info.NauticalDawn},
		{"nautical->civil dawn", info.NauticalDawn, info.CivilDawn},
		{"civil dawn->sunrise", info.CivilDawn, info.Sunrise},
		{"sunrise->morning golden end", info.Sunrise, info.MorningGoldenEnd},
		{"morning golden end->solar noon", info.MorningGoldenEnd, info.SolarNoon},
		{"solar noon->evening golden begin", info.SolarNoon, info.EveningGoldenBegin},
		{"evening golden begin->sunset", info.EveningGoldenBegin, info.Sunset},
		{"sunset->civil dusk", info.Sunset, info.CivilDusk},
		{"civil->nautical dusk", info.CivilDusk, info.NauticalDusk},
		{"nautical->astro dusk", info.NauticalDusk, info.AstroDusk},
	}
	for _, c := range checks {
		if !c.t1.Before(c.t2) {
			t.Errorf("%s: %v should be before %v", c.name,
				c.t1.Format("15:04:05"), c.t2.Format("15:04:05"))
		}
	}
}

func TestSun_Campinas_WinterSolstice_Shorter(t *testing.T) {
	// Southern-hemisphere winter solstice should have less daylight
	// than southern-hemisphere summer solstice.
	winter := time.Date(2024, 6, 21, 12, 0, 0, 0, time.UTC)
	summer := time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC)

	w := Sun(winter, campinasLat, campinasLon)
	s := Sun(summer, campinasLat, campinasLon)
	if !(w.DayLength < s.DayLength) {
		t.Errorf("winter day length %v not shorter than summer %v",
			w.DayLength, s.DayLength)
	}
	// Sanity: winter ~10.7h, summer ~13.4h at Campinas latitude.
	if h := w.DayLength.Hours(); h < 10.3 || h > 11.0 {
		t.Errorf("Campinas winter day length = %.3f h, want ~10.7", h)
	}
	if h := s.DayLength.Hours(); h < 13.1 || h > 13.7 {
		t.Errorf("Campinas summer day length = %.3f h, want ~13.4", h)
	}
}

// ----------------------------------------------------------------------------
// Polar regimes.
// ----------------------------------------------------------------------------

func TestSun_PolarDay_HighNorth_Solstice(t *testing.T) {
	// 80 degrees north on June solstice: sun never sets.
	date := time.Date(2024, 6, 21, 12, 0, 0, 0, time.UTC)
	info := Sun(date, 80, 0)
	if !info.SunAlwaysUp {
		t.Errorf("80N June solstice: SunAlwaysUp = false, want true")
	}
	if info.SunAlwaysDown {
		t.Errorf("80N June solstice: SunAlwaysDown = true unexpectedly")
	}
	if info.DayLength != 24*time.Hour {
		t.Errorf("polar day DayLength = %v, want 24h", info.DayLength)
	}
}

func TestSun_PolarNight_HighSouth_JuneSolstice(t *testing.T) {
	// 80 degrees south on June solstice: sun never rises.
	date := time.Date(2024, 6, 21, 12, 0, 0, 0, time.UTC)
	info := Sun(date, -80, 0)
	if !info.SunAlwaysDown {
		t.Errorf("80S June solstice: SunAlwaysDown = false, want true")
	}
	if info.SunAlwaysUp {
		t.Errorf("80S June solstice: SunAlwaysUp = true unexpectedly")
	}
}

// ----------------------------------------------------------------------------
// Moon phase: cross-check known new and full moons.
// USNO new moons in 2024:
//   2024-01-11 11:57 UTC
//   2024-04-08 18:21 UTC (the eclipse)
//   2024-12-30 22:27 UTC
// USNO full moons in 2024:
//   2024-01-25 17:54 UTC
//   2024-08-19 18:26 UTC
// Tolerance: ~25 degrees on phase angle, 0.10 on illumination.
// (The simple synodic-cycle model ignores Moon orbital perturbations.)
// ----------------------------------------------------------------------------

func TestMoon_NewMoon_Approx(t *testing.T) {
	cases := []time.Time{
		time.Date(2024, 1, 11, 11, 57, 0, 0, time.UTC),
		time.Date(2024, 4, 8, 18, 21, 0, 0, time.UTC),
		time.Date(2024, 12, 30, 22, 27, 0, 0, time.UTC),
	}
	for _, c := range cases {
		m := Moon(c)
		// Phase angle should be near 0 (or 360, which is the same).
		distFromNew := math.Min(m.PhaseAngleDeg, 360-m.PhaseAngleDeg)
		if distFromNew > 25 {
			t.Errorf("new moon at %v: phase angle = %.1f, want near 0/360",
				c, m.PhaseAngleDeg)
		}
		if m.Illumination > 0.15 {
			t.Errorf("new moon at %v: illumination = %.3f, want near 0",
				c, m.Illumination)
		}
	}
}

func TestMoon_FullMoon_Approx(t *testing.T) {
	cases := []time.Time{
		time.Date(2024, 1, 25, 17, 54, 0, 0, time.UTC),
		time.Date(2024, 8, 19, 18, 26, 0, 0, time.UTC),
	}
	for _, c := range cases {
		m := Moon(c)
		if math.Abs(m.PhaseAngleDeg-180) > 25 {
			t.Errorf("full moon at %v: phase angle = %.1f, want near 180",
				c, m.PhaseAngleDeg)
		}
		if m.Illumination < 0.85 {
			t.Errorf("full moon at %v: illumination = %.3f, want near 1.0",
				c, m.Illumination)
		}
	}
}

func TestMoon_PhaseNames_Coverage(t *testing.T) {
	// Sweep one synodic cycle and verify each label appears at least once.
	start := time.Date(2024, 1, 11, 11, 57, 0, 0, time.UTC) // new moon
	seen := map[string]bool{}
	for d := 0; d < 30; d++ {
		m := Moon(start.Add(time.Duration(d) * 24 * time.Hour))
		seen[m.PhaseName] = true
	}
	for _, name := range []string{
		"new", "waxing crescent", "first quarter", "waxing gibbous",
		"full", "waning gibbous", "last quarter", "waning crescent",
	} {
		if !seen[name] {
			t.Errorf("phase %q not seen across one synodic month", name)
		}
	}
}

// ----------------------------------------------------------------------------
// Helper sanity.
// ----------------------------------------------------------------------------

func TestMod360(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{0, 0},
		{360, 0},
		{370, 10},
		{-10, 350},
		{-720, 0},
	}
	for _, c := range cases {
		got := mod360(c.in)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("mod360(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestMod360PlusMinus180(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{0, 0},
		{180, 180},
		{181, -179},
		{-181, 179},
		{360, 0},
	}
	for _, c := range cases {
		got := mod360PlusMinus180(c.in)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("mod360PlusMinus180(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestClamp(t *testing.T) {
	cases := []struct {
		x, lo, hi, want float64
	}{
		{0.5, 0, 1, 0.5},
		{-2, -1, 1, -1},
		{2, -1, 1, 1},
	}
	for _, c := range cases {
		got := clamp(c.x, c.lo, c.hi)
		if got != c.want {
			t.Errorf("clamp(%v, %v, %v) = %v, want %v", c.x, c.lo, c.hi, got, c.want)
		}
	}
}
