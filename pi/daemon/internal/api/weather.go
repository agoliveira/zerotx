package api

import (
	"context"
	"net/http"
	"strconv"
	"time"
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
//	  "weather":     { ... full weather payload ... },
//	  "coordSource": "gps" | "home" | "site"  // omitted when explicit lat/lon
//	}
//
// The wrapper exists so the GUI knows which tier produced the
// coordinates without parsing them out of the request URL itself.
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

	result, src, ok := s.providers.WeatherCurrent()
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
		"coordSource": src,
	})
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
	})
}
