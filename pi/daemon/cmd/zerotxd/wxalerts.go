package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/astro"
	"github.com/agoliveira/zerotx/pi/daemon/internal/audio"
	"github.com/agoliveira/zerotx/pi/daemon/internal/weather"
	"github.com/agoliveira/zerotx/pi/daemon/internal/wxalert"
)

// wxAlertHolder owns the alert tracker and exposes the active alert
// list to the API. Single goroutine writes via runWxAlerts; readers
// take the snapshot pointer atomically (a cheap mutex is fine).
type wxAlertHolder struct {
	mu      sync.RWMutex
	tracker *wxalert.Tracker
	limits  wxalert.Limits
	active  []wxalert.Alert
}

func newWxAlertHolder(lim wxalert.Limits) *wxAlertHolder {
	return &wxAlertHolder{
		tracker: wxalert.DefaultTracker(),
		limits:  lim,
	}
}

func (h *wxAlertHolder) snapshot() []wxalert.Alert {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]wxalert.Alert, len(h.active))
	copy(out, h.active)
	return out
}

func (h *wxAlertHolder) limitsCopy() wxalert.Limits {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.limits
}

// runWxAlerts polls the weather cache once a minute, evaluates rules,
// updates the hysteresis tracker, fires TTS on transitions, and
// refreshes the holder's published active-alert list.
//
// Cadence rationale: weather data refreshes every ~10 minutes, but
// we evaluate every minute so the hysteresis windows have minute-level
// granularity (5 min above to fire, 10 min below to clear).
func runWxAlerts(ctx context.Context, holder *wxAlertHolder, svc *weather.Service, player audio.Player) {
	if holder == nil || svc == nil {
		return
	}
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()

	// Run once immediately so first activation has a chance even on
	// fast weather refresh.
	evaluateOnce(holder, svc, player, time.Now())

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			evaluateOnce(holder, svc, player, now)
		}
	}
}

func evaluateOnce(holder *wxAlertHolder, svc *weather.Service, player audio.Player, now time.Time) {
	w, lat, lon, _, ok := svc.GetCurrent()
	if !ok {
		return // no weather data yet; can't evaluate
	}

	cond := buildConditions(w, lat, lon, now)
	holder.mu.Lock()
	limits := holder.limits
	alerts := wxalert.Evaluate(cond, limits)
	transitions := holder.tracker.Update(now, alerts)
	holder.active = holder.tracker.ActiveAlerts()
	holder.mu.Unlock()

	for _, tr := range transitions {
		announceTransition(tr, player)
	}
}

// buildConditions packs the data wxalert.Conditions needs from a
// weather.Weather snapshot plus astro at the current instant.
func buildConditions(w weather.Weather, lat, lon float64, now time.Time) wxalert.Conditions {
	cond := wxalert.Conditions{
		CurrentWindKmh: w.Current.WindSpeedKmh,
		CurrentGustKmh: w.Current.WindGustKmh,
		CurrentDirDeg:  w.Current.WindDirDeg,
		WeatherCode:    w.Current.WeatherCode,
		Now:            now.UTC(),
	}
	for _, h := range w.Hourly {
		cond.HourlyPrecipProb = append(cond.HourlyPrecipProb, h.PrecipProbability)
	}
	for _, lvl := range w.WindAloft {
		if lvl.HeightM == 80 {
			cond.WindAloft80mSpeedKmh = lvl.SpeedKmh
			cond.WindAloft80mDirDeg = lvl.DirDeg
		}
	}
	if lat != 0 || lon != 0 {
		sun := astro.Sun(now, lat, lon)
		pos := astro.SunPos(now, lat, lon)
		cond.SunsetUTC = sun.Sunset
		cond.SunElevationDeg = pos.ElevationDeg
		// SunFalling: post-solar-noon, pre-sunset (when both are
		// known). Avoids firing the camera-blinding warning during
		// the morning low-sun period.
		if !sun.SolarNoon.IsZero() && now.After(sun.SolarNoon) {
			cond.SunFalling = true
		}
	}
	return cond
}

// announceTransition fires a TTS announcement for an alert
// activation or clearing. Severity maps to audio level: notice for
// SeverityNotice, warning for SeverityWarning. Critical (reserved
// for future rules like lightning) maps to LevelCritical.
func announceTransition(tr wxalert.Transition, player audio.Player) {
	if player == nil {
		return
	}
	level := audio.LevelNotice
	switch tr.Alert.Severity {
	case wxalert.SeverityWarning:
		level = audio.LevelWarning
	case wxalert.SeverityCritical:
		level = audio.LevelCritical
	}

	var text string
	if tr.Activated {
		text = "Weather alert. " + tr.Alert.Message
	} else {
		text = "Weather alert cleared. " + tr.Alert.Name
		// Use a friendlier label than the raw rule name for known cases.
		if friendly, ok := alertClearedLabel[tr.Alert.Name]; ok {
			text = "Weather alert cleared. " + friendly
		}
		// Cleared messages drop a level - not as urgent as the activation.
		if level == audio.LevelWarning {
			level = audio.LevelNotice
		}
	}

	log.Printf("wxalert: transition %s activated=%v level=%s msg=%q",
		tr.Alert.Name, tr.Activated, level, text)
	player.Speak(text, level)
}

// alertClearedLabel maps rule identifiers to friendlier phrases for
// TTS in clearing announcements. Activations include the numeric
// values straight from Evaluate; clearings are intentionally brief.
var alertClearedLabel = map[string]string{
	"wind_gust_high":     "wind gusts back within limits",
	"wind_speed_high":    "wind speed back within limits",
	"wind_aloft_shear":   "wind shear aloft eased",
	"precip_imminent":    "precipitation no longer imminent",
	"low_visibility":     "visibility back to normal",
	"near_sunset":        "sunset window passed",
	"golden_hour_active": "sun no longer in critical band",
}
