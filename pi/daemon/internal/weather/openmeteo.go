package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Default Open-Meteo forecast endpoint. Override via OpenMeteoConfig.BaseURL
// in tests or to point at a self-hosted mirror.
const defaultOpenMeteoBaseURL = "https://api.open-meteo.com/v1/forecast"

// OpenMeteoConfig is the constructor input for the Open-Meteo source.
type OpenMeteoConfig struct {
	// BaseURL overrides the default endpoint. Empty uses the public
	// service at api.open-meteo.com.
	BaseURL string

	// HTTPClient is the client used for outbound requests. Empty uses
	// a sane default with an 8-second timeout.
	HTTPClient *http.Client

	// UserAgent is the User-Agent header sent with each request.
	// Empty uses "zerotx/dev".
	UserAgent string
}

// NewOpenMeteoSource constructs an Open-Meteo source. Stateless beyond
// configuration; safe to share across goroutines.
func NewOpenMeteoSource(cfg OpenMeteoConfig) *OpenMeteoSource {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultOpenMeteoBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 8 * time.Second}
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "zerotx/dev"
	}
	return &OpenMeteoSource{cfg: cfg}
}

// OpenMeteoSource implements Source against api.open-meteo.com.
type OpenMeteoSource struct {
	cfg OpenMeteoConfig
}

// Name returns "open-meteo".
func (s *OpenMeteoSource) Name() string { return "open-meteo" }

// Fetch performs a single forecast call covering current conditions,
// 48 hours of hourly forecast (we slice to 24 in the response), and
// wind data at 10/80/120/180m above ground level.
func (s *OpenMeteoSource) Fetch(ctx context.Context, latDeg, lonDeg float64) (Weather, error) {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(latDeg, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(lonDeg, 'f', 4, 64))
	q.Set("current", "temperature_2m,relative_humidity_2m,surface_pressure,"+
		"wind_speed_10m,wind_direction_10m,wind_gusts_10m,cloud_cover,weather_code")
	q.Set("hourly", "temperature_2m,precipitation_probability,wind_speed_10m,"+
		"wind_gusts_10m,wind_speed_80m,wind_direction_80m,"+
		"wind_speed_120m,wind_direction_120m,"+
		"wind_speed_180m,wind_direction_180m")
	q.Set("forecast_days", "2")
	q.Set("wind_speed_unit", "kmh")
	q.Set("timezone", "UTC")

	u := s.cfg.BaseURL + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Weather{}, fmt.Errorf("open-meteo: build request: %w", err)
	}
	req.Header.Set("User-Agent", s.cfg.UserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return Weather{}, fmt.Errorf("open-meteo: GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Weather{}, fmt.Errorf("open-meteo: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var raw openMeteoResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Weather{}, fmt.Errorf("open-meteo: decode: %w", err)
	}

	return raw.toWeather(latDeg, lonDeg)
}

// openMeteoResponse mirrors the relevant subset of the Open-Meteo
// forecast endpoint response. Hourly data is column-oriented in the
// API (one array per variable) and we re-zip it into row-oriented
// Hourly entries.
type openMeteoResponse struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`

	Current struct {
		Time              string  `json:"time"`
		Temperature2m     float64 `json:"temperature_2m"`
		RelativeHumidity  float64 `json:"relative_humidity_2m"`
		SurfacePressure   float64 `json:"surface_pressure"`
		WindSpeed10m      float64 `json:"wind_speed_10m"`
		WindDirection10m  float64 `json:"wind_direction_10m"`
		WindGusts10m      float64 `json:"wind_gusts_10m"`
		CloudCover        float64 `json:"cloud_cover"`
		WeatherCode       int     `json:"weather_code"`
	} `json:"current"`

	Hourly struct {
		Time                    []string  `json:"time"`
		Temperature2m           []float64 `json:"temperature_2m"`
		PrecipitationProbability []float64 `json:"precipitation_probability"`
		WindSpeed10m            []float64 `json:"wind_speed_10m"`
		WindGusts10m            []float64 `json:"wind_gusts_10m"`
		WindSpeed80m            []float64 `json:"wind_speed_80m"`
		WindDirection80m        []float64 `json:"wind_direction_80m"`
		WindSpeed120m           []float64 `json:"wind_speed_120m"`
		WindDirection120m       []float64 `json:"wind_direction_120m"`
		WindSpeed180m           []float64 `json:"wind_speed_180m"`
		WindDirection180m       []float64 `json:"wind_direction_180m"`
	} `json:"hourly"`
}

// toWeather converts the raw response into the package's typed
// Weather struct. The first matching hourly entry at-or-after the
// current observation time supplies the wind-aloft snapshot.
func (r *openMeteoResponse) toWeather(latReq, lonReq float64) (Weather, error) {
	now := time.Now().UTC()
	observedAt, err := parseOpenMeteoTime(r.Current.Time)
	if err != nil {
		// Don't hard-fail the parse on a single bad timestamp; fall
		// back to "now" and let the caller see the data.
		observedAt = now
	}

	w := Weather{
		LatDeg:    r.Latitude,
		LonDeg:    r.Longitude,
		FetchedAt: now,
		Source:    "open-meteo",
		Current: Current{
			TempC:        r.Current.Temperature2m,
			Humidity:     r.Current.RelativeHumidity,
			PressureHPa:  r.Current.SurfacePressure,
			WindSpeedKmh: r.Current.WindSpeed10m,
			WindDirDeg:   r.Current.WindDirection10m,
			WindGustKmh:  r.Current.WindGusts10m,
			CloudCover:   r.Current.CloudCover,
			WeatherCode:  r.Current.WeatherCode,
			ObservedAt:   observedAt,
		},
	}

	// Determine the hourly index aligned with the current observation
	// time (used both to slice the next 24h and to read wind-aloft).
	startIdx := 0
	for i, ts := range r.Hourly.Time {
		t, err := parseOpenMeteoTime(ts)
		if err != nil {
			continue
		}
		if !t.Before(observedAt.Truncate(time.Hour)) {
			startIdx = i
			break
		}
	}

	// Slice next 24 hours.
	endIdx := startIdx + 24
	if endIdx > len(r.Hourly.Time) {
		endIdx = len(r.Hourly.Time)
	}
	for i := startIdx; i < endIdx; i++ {
		t, err := parseOpenMeteoTime(r.Hourly.Time[i])
		if err != nil {
			continue
		}
		w.Hourly = append(w.Hourly, Hourly{
			Time:              t,
			TempC:             pickFloat(r.Hourly.Temperature2m, i),
			PrecipProbability: pickFloat(r.Hourly.PrecipitationProbability, i),
			WindSpeedKmh:      pickFloat(r.Hourly.WindSpeed10m, i),
			WindGustKmh:       pickFloat(r.Hourly.WindGusts10m, i),
		})
	}

	// Wind aloft snapshot from the start-aligned hourly entry. The 10m
	// surface entry uses the current observation directly (authoritative
	// for surface); higher altitudes come from the hourly per-height
	// arrays at the same time index.
	if startIdx < len(r.Hourly.Time) {
		w.WindAloft = []WindLevel{
			{
				HeightM:  10,
				SpeedKmh: r.Current.WindSpeed10m,
				DirDeg:   r.Current.WindDirection10m,
			},
			{
				HeightM:  80,
				SpeedKmh: pickFloat(r.Hourly.WindSpeed80m, startIdx),
				DirDeg:   pickFloat(r.Hourly.WindDirection80m, startIdx),
			},
			{
				HeightM:  120,
				SpeedKmh: pickFloat(r.Hourly.WindSpeed120m, startIdx),
				DirDeg:   pickFloat(r.Hourly.WindDirection120m, startIdx),
			},
			{
				HeightM:  180,
				SpeedKmh: pickFloat(r.Hourly.WindSpeed180m, startIdx),
				DirDeg:   pickFloat(r.Hourly.WindDirection180m, startIdx),
			},
		}
	}

	return w, nil
}

// pickFloat returns arr[i] or 0 if i is out of range. Used to shield
// the parser from an upstream array that's shorter than expected (an
// Open-Meteo response with fewer hourly entries than the schema's
// nominal 48-per-day, which can happen near month boundaries on the
// free tier).
func pickFloat(arr []float64, i int) float64 {
	if i < 0 || i >= len(arr) {
		return 0
	}
	return arr[i]
}

// parseOpenMeteoTime parses Open-Meteo's "YYYY-MM-DDTHH:MM" timestamp
// (no seconds, no timezone) as UTC. We requested timezone=UTC in the
// query so all server-emitted timestamps are UTC by contract.
func parseOpenMeteoTime(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02T15:04", s)
	if err != nil {
		// Try with seconds in case a future API revision adds them.
		t, err = time.Parse("2006-01-02T15:04:05", s)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
		}
	}
	return t.UTC(), nil
}
