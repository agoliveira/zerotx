package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/astro"
)

// handleWeather serves /api/v1/weather.
//
// With no query params: returns the cached weather for the observer's
// current location (whichever the daemon's coordinate resolver picked).
// 404 when no coordinates are available at all (no GPS, no home, no
// configured site). 503 when coordinates are known but no cached
// fetch has succeeded yet.
//
// With ?lat=X&lon=Y: returns weather at those exact coordinates. The
// daemon fetches inline if the cache is missing or stale; on fetch
// error 503 is returned (with reason).
//
// On success the response shape is:
//
//	{
//	  "weather":     { ... full weather payload from internal/weather ... },
//	  "astro":       { ... sunrise/sunset/twilight/golden hour, sun position now, moon ... },
//	  "coordSource": "gps" | "home" | "site"  // omitted when explicit lat/lon
//	}
//
// The astro block is computed at request time (not at fetch time) so
// the live "sun position" alt/az is always current; cached weather
// data can be minutes old without making the sun appear stale.
//
// The coordSource wrapper exists so the GUI knows which tier produced
// the coordinates without parsing the request URL.
func (s *Server) handleWeather(w http.ResponseWriter, r *http.Request) {
	if s.providers.WeatherCurrent == nil {
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}

	latStr := r.URL.Query().Get("lat")
	lonStr := r.URL.Query().Get("lon")
	if latStr != "" || lonStr != "" {
		s.handleWeatherExplicit(w, r, latStr, lonStr)
		return
	}

	result, lat, lon, src, ok := s.providers.WeatherCurrent()
	if !ok {
		if src == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "no coordinates",
				"reason": "no GPS lock, no home position, no configured site",
			})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":       "weather not yet available",
			"reason":      "background fetch hasn't completed",
			"coordSource": src,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"weather":     result,
		"astro":       astroSnapshot(lat, lon, time.Now()),
		"alerts":      currentAlerts(s),
		"coordSource": src,
	})
}

func currentAlerts(s *Server) []WeatherAlert {
	if s.providers.WeatherAlerts == nil {
		return nil
	}
	return s.providers.WeatherAlerts()
}

func (s *Server) handleWeatherExplicit(w http.ResponseWriter, r *http.Request, latStr, lonStr string) {
	lat, err1 := strconv.ParseFloat(latStr, 64)
	lon, err2 := strconv.ParseFloat(lonStr, 64)
	if err1 != nil || err2 != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid lat/lon",
			"reason": "lat and lon must both be valid floats",
		})
		return
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "out of range",
			"reason": "lat must be -90..90, lon must be -180..180",
		})
		return
	}
	if s.providers.WeatherFetch == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "weather fetch not configured",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, err := s.providers.WeatherFetch(ctx, lat, lon)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "fetch failed",
			"reason": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"weather": result,
		"astro":   astroSnapshot(lat, lon, time.Now()),
		"alerts":  currentAlerts(s),
	})
}

// astroResponse mirrors the public types in internal/astro but with
// JSON tags suited to the API. Built at request time so sunPosition
// (current alt/az) tracks real time even when weather data is cached.
type astroResponse struct {
	Now         time.Time            `json:"now"`
	SunPosition astroSunPositionResp `json:"sunPosition"`
	Sun         astroSunInfoResp     `json:"sun"`
	Moon        astroMoonResp        `json:"moon"`
}

type astroSunPositionResp struct {
	AzimuthDeg     float64 `json:"azimuthDeg"`
	ElevationDeg   float64 `json:"elevationDeg"`
	DeclinationDeg float64 `json:"declinationDeg"`
	RightAscDeg    float64 `json:"rightAscDeg"`
}

// astroSunInfoResp uses time.Time fields tagged omitempty. Zero values
// (which represent "event does not occur today" at polar latitudes)
// are omitted entirely from the JSON output. The polar flags below
// disambiguate: SunAlwaysUp + missing sunrise/sunset means polar day,
// SunAlwaysDown + missing means polar night.
type astroSunInfoResp struct {
	Sunrise            time.Time `json:"sunrise,omitempty"`
	Sunset             time.Time `json:"sunset,omitempty"`
	SolarNoon          time.Time `json:"solarNoon,omitempty"`
	CivilDawn          time.Time `json:"civilDawn,omitempty"`
	CivilDusk          time.Time `json:"civilDusk,omitempty"`
	NauticalDawn       time.Time `json:"nauticalDawn,omitempty"`
	NauticalDusk       time.Time `json:"nauticalDusk,omitempty"`
	AstroDawn          time.Time `json:"astroDawn,omitempty"`
	AstroDusk          time.Time `json:"astroDusk,omitempty"`
	MorningGoldenEnd   time.Time `json:"morningGoldenEnd,omitempty"`
	EveningGoldenBegin time.Time `json:"eveningGoldenBegin,omitempty"`
	DayLengthSec       int       `json:"dayLengthSec"`
	SunAlwaysUp        bool      `json:"sunAlwaysUp,omitempty"`
	SunAlwaysDown      bool      `json:"sunAlwaysDown,omitempty"`
}

type astroMoonResp struct {
	PhaseAngleDeg float64 `json:"phaseAngleDeg"`
	Illumination  float64 `json:"illumination"`
	PhaseName     string  `json:"phaseName"`
}

// astroSnapshot calls into internal/astro at the given time and reshapes
// the output into the JSON structure the GUI consumes. Pure compute,
// no allocation beyond the response struct itself.
func astroSnapshot(latDeg, lonDeg float64, now time.Time) astroResponse {
	pos := astro.SunPos(now, latDeg, lonDeg)
	sun := astro.Sun(now, latDeg, lonDeg)
	moon := astro.Moon(now)
	return astroResponse{
		Now: now.UTC(),
		SunPosition: astroSunPositionResp{
			AzimuthDeg:     pos.AzimuthDeg,
			ElevationDeg:   pos.ElevationDeg,
			DeclinationDeg: pos.DeclinationDeg,
			RightAscDeg:    pos.RightAscDeg,
		},
		Sun: astroSunInfoResp{
			Sunrise:            sun.Sunrise,
			Sunset:             sun.Sunset,
			SolarNoon:          sun.SolarNoon,
			CivilDawn:          sun.CivilDawn,
			CivilDusk:          sun.CivilDusk,
			NauticalDawn:       sun.NauticalDawn,
			NauticalDusk:       sun.NauticalDusk,
			AstroDawn:          sun.AstroDawn,
			AstroDusk:          sun.AstroDusk,
			MorningGoldenEnd:   sun.MorningGoldenEnd,
			EveningGoldenBegin: sun.EveningGoldenBegin,
			DayLengthSec:       int(sun.DayLength.Seconds()),
			SunAlwaysUp:        sun.SunAlwaysUp,
			SunAlwaysDown:      sun.SunAlwaysDown,
		},
		Moon: astroMoonResp{
			PhaseAngleDeg: moon.PhaseAngleDeg,
			Illumination:  moon.Illumination,
			PhaseName:     moon.PhaseName,
		},
	}
}
