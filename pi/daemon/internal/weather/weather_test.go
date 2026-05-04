package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ----------------------------------------------------------------------------
// Open-Meteo parsing.
// ----------------------------------------------------------------------------

const sampleResponse = `{
  "latitude": -22.91,
  "longitude": -47.06,
  "current": {
    "time": "2026-05-04T16:00",
    "temperature_2m": 22.5,
    "relative_humidity_2m": 65,
    "surface_pressure": 1013.2,
    "wind_speed_10m": 12.3,
    "wind_direction_10m": 90,
    "wind_gusts_10m": 18.5,
    "cloud_cover": 25,
    "weather_code": 2
  },
  "hourly": {
    "time": ["2026-05-04T15:00","2026-05-04T16:00","2026-05-04T17:00","2026-05-04T18:00"],
    "temperature_2m": [22.0, 22.5, 23.1, 22.8],
    "precipitation_probability": [10, 5, 5, 15],
    "wind_speed_10m": [11.0, 12.3, 13.2, 12.5],
    "wind_gusts_10m": [16.0, 18.5, 20.1, 19.2],
    "wind_speed_80m": [18.5, 20.1, 21.5, 20.8],
    "wind_direction_80m": [85, 90, 95, 100],
    "wind_speed_120m": [22.1, 23.5, 25.0, 24.2],
    "wind_direction_120m": [85, 90, 95, 100],
    "wind_speed_180m": [25.0, 27.0, 28.5, 27.8],
    "wind_direction_180m": [80, 85, 90, 95]
  }
}`

func newTestSource(t *testing.T, body string, status int) (*OpenMeteoSource, *httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	src := NewOpenMeteoSource(OpenMeteoConfig{BaseURL: srv.URL})
	t.Cleanup(srv.Close)
	return src, srv, &hits
}

func TestOpenMeteo_Fetch_ParsesCurrent(t *testing.T) {
	src, _, _ := newTestSource(t, sampleResponse, http.StatusOK)
	w, err := src.Fetch(context.Background(), -22.91, -47.06)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if w.Source != "open-meteo" {
		t.Errorf("Source = %q, want open-meteo", w.Source)
	}
	if w.Current.TempC != 22.5 {
		t.Errorf("TempC = %v, want 22.5", w.Current.TempC)
	}
	if w.Current.WindSpeedKmh != 12.3 {
		t.Errorf("WindSpeedKmh = %v, want 12.3", w.Current.WindSpeedKmh)
	}
	if w.Current.WeatherCode != 2 {
		t.Errorf("WeatherCode = %v, want 2", w.Current.WeatherCode)
	}
	wantObs := time.Date(2026, 5, 4, 16, 0, 0, 0, time.UTC)
	if !w.Current.ObservedAt.Equal(wantObs) {
		t.Errorf("ObservedAt = %v, want %v", w.Current.ObservedAt, wantObs)
	}
}

func TestOpenMeteo_Fetch_HourlyAlignedToObservation(t *testing.T) {
	// Sample observation is at 16:00. Hourly array starts at 15:00.
	// First hourly entry returned should be 16:00 (aligned to observation).
	src, _, _ := newTestSource(t, sampleResponse, http.StatusOK)
	w, err := src.Fetch(context.Background(), -22.91, -47.06)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(w.Hourly) == 0 {
		t.Fatalf("Hourly empty")
	}
	want := time.Date(2026, 5, 4, 16, 0, 0, 0, time.UTC)
	if !w.Hourly[0].Time.Equal(want) {
		t.Errorf("Hourly[0].Time = %v, want %v (hourly should start at observation hour, not before)",
			w.Hourly[0].Time, want)
	}
	if w.Hourly[0].TempC != 22.5 {
		t.Errorf("Hourly[0].TempC = %v, want 22.5", w.Hourly[0].TempC)
	}
}

func TestOpenMeteo_Fetch_WindAloft(t *testing.T) {
	src, _, _ := newTestSource(t, sampleResponse, http.StatusOK)
	w, err := src.Fetch(context.Background(), -22.91, -47.06)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(w.WindAloft) != 4 {
		t.Fatalf("WindAloft len = %d, want 4", len(w.WindAloft))
	}
	wantHeights := []int{10, 80, 120, 180}
	for i, h := range wantHeights {
		if w.WindAloft[i].HeightM != h {
			t.Errorf("WindAloft[%d].HeightM = %d, want %d", i, w.WindAloft[i].HeightM, h)
		}
	}
	// 10m surface should match the current observation.
	if w.WindAloft[0].SpeedKmh != 12.3 {
		t.Errorf("WindAloft[0] (10m) speed = %v, want 12.3 (from current)", w.WindAloft[0].SpeedKmh)
	}
	if w.WindAloft[0].DirDeg != 90 {
		t.Errorf("WindAloft[0] (10m) dir = %v, want 90 (from current)", w.WindAloft[0].DirDeg)
	}
	// Higher altitudes from hourly arrays at the start index (16:00 = idx 1).
	if w.WindAloft[1].SpeedKmh != 20.1 {
		t.Errorf("WindAloft[1] (80m) speed = %v, want 20.1", w.WindAloft[1].SpeedKmh)
	}
	if w.WindAloft[3].SpeedKmh != 27.0 {
		t.Errorf("WindAloft[3] (180m) speed = %v, want 27.0", w.WindAloft[3].SpeedKmh)
	}
}

func TestOpenMeteo_Fetch_HTTPError(t *testing.T) {
	src, _, _ := newTestSource(t, `{"error":"bad request"}`, http.StatusBadRequest)
	_, err := src.Fetch(context.Background(), 0, 0)
	if err == nil {
		t.Fatalf("want error on HTTP 400, got nil")
	}
}

func TestOpenMeteo_Fetch_BadJSON(t *testing.T) {
	src, _, _ := newTestSource(t, `not json`, http.StatusOK)
	_, err := src.Fetch(context.Background(), 0, 0)
	if err == nil {
		t.Fatalf("want decode error, got nil")
	}
}

// ----------------------------------------------------------------------------
// Cache.
// ----------------------------------------------------------------------------

func TestCache_GetPut_Memory(t *testing.T) {
	c, err := NewCache("")
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	w := Weather{
		LatDeg:    -22.91,
		LonDeg:    -47.06,
		FetchedAt: now,
		Source:    "open-meteo",
		Current:   Current{TempC: 25},
	}
	c.Put(w)

	got, ok := c.Get(-22.91, -47.06)
	if !ok {
		t.Fatalf("Get miss after Put")
	}
	if got.Current.TempC != 25 {
		t.Errorf("TempC = %v, want 25", got.Current.TempC)
	}
}

func TestCache_KeyRounding(t *testing.T) {
	c, err := NewCache("")
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	w := Weather{LatDeg: -22.9099, LonDeg: -47.0626, FetchedAt: time.Now().UTC()}
	c.Put(w)

	// Slightly different coords (still within the rounding tolerance)
	// must hit the same cache entry.
	if _, ok := c.Get(-22.9101, -47.0620); !ok {
		t.Errorf("Get with nearby coords missed; rounding broken")
	}
}

func TestCache_DiskRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c1, err := NewCache(dir)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	w := Weather{
		LatDeg:    -22.91,
		LonDeg:    -47.06,
		FetchedAt: time.Now().UTC().Truncate(time.Second),
		Source:    "open-meteo",
		Current:   Current{TempC: 25, WindSpeedKmh: 10},
	}
	c1.Put(w)

	// Verify a JSON file was written with the expected name.
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("disk dir has %d files, want 1", len(files))
	}
	if filepath.Ext(files[0].Name()) != ".json" {
		t.Errorf("disk file %q not .json", files[0].Name())
	}

	// New cache instance should rehydrate from disk.
	c2, err := NewCache(dir)
	if err != nil {
		t.Fatalf("NewCache (rehydrate): %v", err)
	}
	got, ok := c2.Get(-22.91, -47.06)
	if !ok {
		t.Fatalf("Get miss after disk rehydrate")
	}
	if got.Current.TempC != 25 {
		t.Errorf("rehydrated TempC = %v, want 25", got.Current.TempC)
	}
}

func TestCache_AgeMissing(t *testing.T) {
	c, _ := NewCache("")
	_, ok := c.Age(0, 0, time.Now())
	if ok {
		t.Errorf("Age on missing entry returned ok=true")
	}
}

// ----------------------------------------------------------------------------
// Service.
// ----------------------------------------------------------------------------

// fakeSource is a Source backed by a func, with a hit counter and an
// optional error injection. Tests use this rather than the real Open-
// Meteo source so service-level logic is exercised without HTTP.
type fakeSource struct {
	mu       sync.Mutex
	hits     int
	produce  func(lat, lon float64) (Weather, error)
}

func (f *fakeSource) Name() string { return "fake" }
func (f *fakeSource) Fetch(_ context.Context, lat, lon float64) (Weather, error) {
	f.mu.Lock()
	f.hits++
	f.mu.Unlock()
	return f.produce(lat, lon)
}
func (f *fakeSource) Hits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits
}

func TestService_Get_FreshCache(t *testing.T) {
	c, _ := NewCache("")
	src := &fakeSource{produce: func(lat, lon float64) (Weather, error) {
		return Weather{
			LatDeg: lat, LonDeg: lon,
			FetchedAt: time.Now().UTC(),
			Source:    "fake",
			Current:   Current{TempC: 20},
		}, nil
	}}
	resolver := func() (float64, float64, string, bool) {
		return -22.91, -47.06, "configured", true
	}
	svc, err := New(Options{Source: src, Cache: c, Resolver: resolver, MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// First call fetches.
	if _, err := svc.Get(context.Background(), -22.91, -47.06); err != nil {
		t.Fatalf("Get1: %v", err)
	}
	if src.Hits() != 1 {
		t.Errorf("hits = %d, want 1 after first Get", src.Hits())
	}

	// Second call within MaxAge serves cache.
	if _, err := svc.Get(context.Background(), -22.91, -47.06); err != nil {
		t.Fatalf("Get2: %v", err)
	}
	if src.Hits() != 1 {
		t.Errorf("hits = %d, want 1 (second Get should hit cache)", src.Hits())
	}
}

// TestService_Get_GridSnap_AliasesUnderRequestCoords reproduces the
// bug observed in the field with Open-Meteo: the source snaps to its
// own grid point and returns coordinates a few hundredths of a degree
// off. The cached entry must still be findable via the originally-
// requested coordinates, otherwise every Get triggers a fresh fetch.
func TestService_Get_GridSnap_AliasesUnderRequestCoords(t *testing.T) {
	c, _ := NewCache("")
	src := &fakeSource{produce: func(lat, lon float64) (Weather, error) {
		// Source snaps -22.91 -> -22.95, -47.06 -> -47.07.
		return Weather{
			LatDeg: -22.95, LonDeg: -47.07,
			FetchedAt: time.Now().UTC(),
			Source:    "fake",
			Current:   Current{TempC: 26.5},
		}, nil
	}}
	resolver := func() (float64, float64, string, bool) {
		return -22.91, -47.06, "site", true
	}
	svc, _ := New(Options{Source: src, Cache: c, Resolver: resolver, MaxAge: time.Hour})

	// First Get triggers fetch. Source returns snapped coords.
	if _, err := svc.Get(context.Background(), -22.91, -47.06); err != nil {
		t.Fatalf("Get1: %v", err)
	}
	if src.Hits() != 1 {
		t.Fatalf("hits after Get1 = %d, want 1", src.Hits())
	}

	// Second Get with the SAME requested coords must hit cache, not
	// re-fetch. Before the alias fix this was a re-fetch because
	// the cache key was the snapped coords, not the requested ones.
	if _, err := svc.Get(context.Background(), -22.91, -47.06); err != nil {
		t.Fatalf("Get2: %v", err)
	}
	if src.Hits() != 1 {
		t.Errorf("hits after Get2 = %d, want 1 (cache miss after grid-snap means alias broken)", src.Hits())
	}

	// And Get under the snapped coords also hits (both keys alias the
	// same entry).
	if _, err := svc.Get(context.Background(), -22.95, -47.07); err != nil {
		t.Fatalf("Get3: %v", err)
	}
	if src.Hits() != 1 {
		t.Errorf("hits after Get3 = %d, want 1 (snapped-coord lookup should also hit)", src.Hits())
	}
}

func TestService_Get_FetchError_ServesStale(t *testing.T) {
	c, _ := NewCache("")
	// Pre-seed cache with an old entry.
	old := Weather{
		LatDeg: -22.91, LonDeg: -47.06,
		FetchedAt: time.Now().UTC().Add(-2 * time.Hour),
		Source:    "fake",
		Current:   Current{TempC: 15},
	}
	c.Put(old)

	src := &fakeSource{produce: func(lat, lon float64) (Weather, error) {
		return Weather{}, errors.New("network down")
	}}
	resolver := func() (float64, float64, string, bool) {
		return -22.91, -47.06, "configured", true
	}
	svc, err := New(Options{Source: src, Cache: c, Resolver: resolver, MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := svc.Get(context.Background(), -22.91, -47.06)
	if err != nil {
		t.Fatalf("Get with stale fallback should not error: %v", err)
	}
	if got.Current.TempC != 15 {
		t.Errorf("expected stale cached entry, got TempC=%v", got.Current.TempC)
	}
}

func TestService_Get_FetchError_NoCache_Errors(t *testing.T) {
	c, _ := NewCache("")
	src := &fakeSource{produce: func(lat, lon float64) (Weather, error) {
		return Weather{}, errors.New("offline")
	}}
	resolver := func() (float64, float64, string, bool) { return 0, 0, "", true }
	svc, _ := New(Options{Source: src, Cache: c, Resolver: resolver})

	if _, err := svc.Get(context.Background(), 0, 0); err == nil {
		t.Errorf("Get with no cache and source error should return error")
	}
}

func TestService_GetCurrent_NoCoords_NotOk(t *testing.T) {
	c, _ := NewCache("")
	src := &fakeSource{produce: func(_, _ float64) (Weather, error) {
		return Weather{}, nil
	}}
	resolver := func() (float64, float64, string, bool) { return 0, 0, "", false }
	svc, _ := New(Options{Source: src, Cache: c, Resolver: resolver})

	_, _, ok := svc.GetCurrent()
	if ok {
		t.Errorf("GetCurrent should return ok=false when resolver fails")
	}
}

func TestService_GetCurrent_ReadsFromCache(t *testing.T) {
	c, _ := NewCache("")
	c.Put(Weather{
		LatDeg: -22.91, LonDeg: -47.06,
		FetchedAt: time.Now().UTC(),
		Source:    "fake",
		Current:   Current{TempC: 18},
	})
	src := &fakeSource{produce: func(_, _ float64) (Weather, error) {
		t.Errorf("GetCurrent should not call Source")
		return Weather{}, nil
	}}
	resolver := func() (float64, float64, string, bool) {
		return -22.91, -47.06, "gps", true
	}
	svc, _ := New(Options{Source: src, Cache: c, Resolver: resolver})

	w, src2, ok := svc.GetCurrent()
	if !ok {
		t.Fatalf("GetCurrent ok=false unexpectedly")
	}
	if w.Current.TempC != 18 {
		t.Errorf("TempC = %v, want 18", w.Current.TempC)
	}
	if src2 != "gps" {
		t.Errorf("coord source = %q, want gps", src2)
	}
}

// ----------------------------------------------------------------------------
// JSON shape: confirm encoding produces the field names the GUI will
// consume, and decoding the encoded form round-trips.
// ----------------------------------------------------------------------------

func TestWeather_JSON_RoundTrip(t *testing.T) {
	original := Weather{
		LatDeg: -22.91, LonDeg: -47.06,
		FetchedAt: time.Date(2026, 5, 4, 16, 0, 0, 0, time.UTC),
		Source:    "open-meteo",
		Current: Current{
			TempC: 22.5, Humidity: 65, PressureHPa: 1013.2,
			WindSpeedKmh: 12.3, WindDirDeg: 90, WindGustKmh: 18.5,
			CloudCover: 25, WeatherCode: 2,
			ObservedAt: time.Date(2026, 5, 4, 16, 0, 0, 0, time.UTC),
		},
		Hourly: []Hourly{
			{Time: time.Date(2026, 5, 4, 17, 0, 0, 0, time.UTC), TempC: 23.1, WindSpeedKmh: 13.2},
		},
		WindAloft: []WindLevel{
			{HeightM: 10, SpeedKmh: 12.3, DirDeg: 90},
		},
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Weather
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Current.TempC != original.Current.TempC {
		t.Errorf("round-trip TempC: got %v, want %v", decoded.Current.TempC, original.Current.TempC)
	}
	if len(decoded.Hourly) != len(original.Hourly) {
		t.Errorf("round-trip Hourly len mismatch")
	}
	if len(decoded.WindAloft) != len(original.WindAloft) {
		t.Errorf("round-trip WindAloft len mismatch")
	}
}
