// Package weather fetches and caches weather data for the operator's
// flight site. The package is structured around three concepts:
//
//   - Source: an external data provider (Open-Meteo today; INMET,
//     REDEMET, Blitzortung etc. could plug in later via the same
//     interface). Sources convert API responses into the typed
//     Weather struct exposed to consumers.
//
//   - Cache: a small in-memory + on-disk store keyed by rounded
//     coordinates (~1 km granularity). Survives daemon restarts so
//     a brief connection enriches a whole flight even if the network
//     drops between fetches.
//
//   - Service: the daemon-side coordinator. Owns a background
//     refresher that resolves the current observer location, fetches
//     when the cache entry is older than the refresh interval, and
//     writes back to the cache. Exposes Get for explicit-coords and
//     GetCurrent for "wherever I am right now" (the modal's path).
//
// Design notes:
//
//   - Pre-flight / post-flight only by intent. The Service is happy
//     to run during armed flight, but the modal that consumes its
//     data is not opened then.
//
//   - Network-aware refresh policy is stubbed for v1 to "home, always"
//     until the network classification subsystem is built. The hook
//     lives behind the Service.refreshInterval setter; rewiring is a
//     handful of lines when classification ships.
//
//   - Transparent on no-internet: an empty cache plus an offline
//     network produces an empty response, no error chrome. The modal
//     UI degrades gracefully (shown as "weather unavailable").
package weather

import (
	"context"
	"time"
)

// Weather is the typed payload exposed to the daemon's HTTP API and
// downstream consumers (alerts, modal UI). Time fields are absolute
// UTC; speeds are km/h; angles are degrees. Hourly entries cover the
// next 24 hours; WindAloft samples are a single point in time (the
// most recent hourly entry from the source) at multiple altitudes.
type Weather struct {
	// Coordinates the data was fetched for, rounded to ~1 km. The
	// caller-requested coordinates may be slightly different.
	LatDeg float64 `json:"latDeg"`
	LonDeg float64 `json:"lonDeg"`

	// FetchedAt is the wall-clock instant the daemon last successfully
	// retrieved data from the source. UTC.
	FetchedAt time.Time `json:"fetchedAt"`

	// Source identifies which provider produced this payload, e.g.
	// "open-meteo". Strings are stable across releases.
	Source string `json:"source"`

	Current   Current     `json:"current"`
	Hourly    []Hourly    `json:"hourly,omitempty"`
	WindAloft []WindLevel `json:"windAloft,omitempty"`
}

// Current is the present-moment surface observation.
type Current struct {
	TempC         float64 `json:"tempC"`
	Humidity      float64 `json:"humidity"` // 0-100 %
	PressureHPa   float64 `json:"pressureHpa"`
	WindSpeedKmh  float64 `json:"windSpeedKmh"`
	WindDirDeg    float64 `json:"windDirDeg"`   // 0=N, 90=E (whence the wind comes)
	WindGustKmh   float64 `json:"windGustKmh"`
	CloudCover    float64 `json:"cloudCover"`   // 0-100 %
	WeatherCode   int     `json:"weatherCode"`  // WMO 4677 code
	ObservedAt    time.Time `json:"observedAt"` // source-reported time
}

// Hourly is one point in the next-24-hour forecast.
type Hourly struct {
	Time              time.Time `json:"time"`
	TempC             float64   `json:"tempC"`
	PrecipProbability float64   `json:"precipProbability"` // 0-100 %
	WindSpeedKmh      float64   `json:"windSpeedKmh"`
	WindGustKmh       float64   `json:"windGustKmh"`
}

// WindLevel is wind data at a specific altitude above ground level.
// Used for the "wind aloft" panel: surface gusts are not the only
// hazard; FPV operating altitudes (50-300m) often see different
// speeds and directions than the surface.
type WindLevel struct {
	HeightM      int     `json:"heightM"`
	SpeedKmh     float64 `json:"speedKmh"`
	DirDeg       float64 `json:"dirDeg"`
}

// Source produces Weather payloads on demand. Implementations are
// expected to be pure (no internal state beyond config), so the
// Service can call Fetch concurrently if a future refresher fans out
// across multiple sources for redundancy.
type Source interface {
	// Name is a stable identifier for the source ("open-meteo",
	// "inmet", "redemet"). Returned in Weather.Source.
	Name() string

	// Fetch returns weather for the given coordinates, or an error.
	// Network-level failures, source-side errors, and parse errors
	// all produce a non-nil error. Callers retry on next interval.
	Fetch(ctx context.Context, latDeg, lonDeg float64) (Weather, error)
}

// CoordResolver returns the observer's current coordinates, with a
// label describing the priority tier the value came from. Resolution
// order in the daemon is:
//
//	gps      - locked GPS fix from telemetry
//	home     - home position recorded at arm time
//	site     - configured site coordinates (model YAML or env)
//
// ok=false means none of the tiers produced a coordinate; the Service
// skips the fetch entirely (no fallback to a hardcoded location:
// silent fallback hides bugs).
type CoordResolver func() (latDeg, lonDeg float64, source string, ok bool)
