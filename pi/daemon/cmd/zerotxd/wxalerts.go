package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/arm"
	"github.com/agoliveira/zerotx/pi/daemon/internal/astro"
	"github.com/agoliveira/zerotx/pi/daemon/internal/audio"
	"github.com/agoliveira/zerotx/pi/daemon/internal/devices/display"
	"github.com/agoliveira/zerotx/pi/daemon/internal/weather"
	"github.com/agoliveira/zerotx/pi/daemon/internal/wxalert"
)

// wxAlertHolder owns the alert tracker and exposes the active alert
// list to the API. Single goroutine writes via runWxAlerts; readers
// take the snapshot pointer atomically (a cheap mutex is fine).
//
// The holder also maintains the LED-panel projection: which alert,
// if any, is currently displayed as a DISP ALARM. The LED projection
// is governed by these rules:
//
//   - Skipped entirely when armed. Flight data must remain visible
//     on the panel during flight; TTS still announces. Operator sees
//     in-flight alerts on HUD/Map pages, not on LED.
//
//   - Highest-severity active alert wins. Ties broken alphabetically
//     by rule name.
//
//   - The daemon owns the DISP ALARM channel for now (no other code
//     paths fire alarms). When that changes, this needs a proper
//     priority abstraction.
type wxAlertHolder struct {
	mu      sync.RWMutex
	tracker *wxalert.Tracker
	limits  wxalert.Limits
	active  []wxalert.Alert

	// ledShown is the rule name of the alert currently displayed on
	// the LED panel via DISP ALARM, or "" if no alarm is shown.
	// Driven only from runWxAlerts.
	ledShown string
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
// updates the hysteresis tracker, fires TTS on transitions, refreshes
// the holder's published active-alert list, and projects the
// highest-severity alert to the LED panel (when not armed).
//
// Cadence rationale: weather data refreshes every ~10 minutes, but
// we evaluate every minute so the hysteresis windows have minute-level
// granularity (5 min above to fire, 10 min below to clear).
func runWxAlerts(ctx context.Context, holder *wxAlertHolder, svc *weather.Service, player audio.Player, dispMgr *display.Manager, armMachine *arm.Machine) {
	if holder == nil || svc == nil {
		return
	}
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()

	// Run once immediately so first activation has a chance even on
	// fast weather refresh.
	evaluateOnce(holder, svc, player, dispMgr, armMachine, time.Now())

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			evaluateOnce(holder, svc, player, dispMgr, armMachine, now)
		}
	}
}

func evaluateOnce(holder *wxAlertHolder, svc *weather.Service, player audio.Player, dispMgr *display.Manager, armMachine *arm.Machine, now time.Time) {
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

	// LED projection: pick the highest-severity active alert (ties
	// broken alphabetically), unless armed. Compare to the last-shown
	// rule name; only emit DISP ALARM/CLEAR-ALARM on transitions.
	updateLEDProjection(holder, dispMgr, armMachine)
}

// updateLEDProjection compares the desired LED state against the
// last-emitted state and issues DISP ALARM / DISP CLEAR-ALARM to
// produce the desired state. Skipped when armed (FLIGHT mode keeps
// rendering flight data; TTS already announces in-flight alerts).
func updateLEDProjection(holder *wxAlertHolder, dispMgr *display.Manager, armMachine *arm.Machine) {
	if dispMgr == nil {
		return
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()

	armed := armMachine != nil && armMachine.State() == arm.StateArmed
	desired := pickHighestSeverity(holder.active)

	switch {
	case armed:
		// In flight: never preempt the panel. If we previously had
		// an alarm shown (e.g. operator armed during pre-flight with
		// a wind alert active), clear it now so flight data is
		// visible. The HUD/Map chips still show the alert visually.
		if holder.ledShown != "" {
			dispMgr.ClearAlarm()
			log.Printf("wxalert: LED cleared (armed; was showing %q)", holder.ledShown)
			holder.ledShown = ""
		}
	case desired == nil:
		if holder.ledShown != "" {
			dispMgr.ClearAlarm()
			log.Printf("wxalert: LED cleared (no active alerts; was showing %q)", holder.ledShown)
			holder.ledShown = ""
		}
	case holder.ledShown != desired.Name:
		level := alarmLevelFromSeverity(desired.Severity)
		text := ledBannerText(*desired)
		dispMgr.FireAlarm(level, text)
		log.Printf("wxalert: LED showing %q (%s) text=%q",
			desired.Name, level, text)
		holder.ledShown = desired.Name
	}
}

// pickHighestSeverity returns the alert with the highest severity from
// the slice. Ties broken alphabetically by rule name. Returns nil for
// empty input. (ActiveAlerts is already sorted by name, so we just
// pick the highest severity and the alphabetical-tie-break is implicit.)
func pickHighestSeverity(alerts []wxalert.Alert) *wxalert.Alert {
	if len(alerts) == 0 {
		return nil
	}
	best := &alerts[0]
	for i := range alerts[1:] {
		a := &alerts[1+i]
		if a.Severity > best.Severity {
			best = a
		}
	}
	return best
}

// alarmLevelFromSeverity maps wxalert severities to the display
// package's alarm levels. The display level governs banner color.
func alarmLevelFromSeverity(s wxalert.Severity) display.AlarmLevel {
	switch s {
	case wxalert.SeverityCritical:
		return display.AlarmCritical
	case wxalert.SeverityWarning:
		return display.AlarmWarning
	}
	return display.AlarmNotice
}

// ledBannerText produces the text shown on the LED panel for an
// active alert. Panel is 128x32 - room for ~16 chars at 16px or so.
// We send a short label per rule and let the display firmware deal
// with rendering.
func ledBannerText(a wxalert.Alert) string {
	switch a.Name {
	case "wind_gust_high":
		return "WX: GUSTS HIGH"
	case "wind_speed_high":
		return "WX: WIND HIGH"
	case "wind_aloft_shear":
		return "WX: SHEAR ALOFT"
	case "precip_imminent":
		return "WX: RAIN SOON"
	case "low_visibility":
		return "WX: LOW VIS"
	case "near_sunset":
		return "WX: NEAR SUNSET"
	case "golden_hour_active":
		return "WX: SUN LOW"
	}
	return "WX: " + a.Name
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

// wxAlertProviderAdapter exposes wxAlertHolder via the
// trackballled.AlertProvider interface (which requires Snapshot()
// uppercase rather than the holder's lowercase snapshot()). Pure
// adapter; no state.
type wxAlertProviderAdapter struct {
	h *wxAlertHolder
}

func (a wxAlertProviderAdapter) Snapshot() []wxalert.Alert {
	if a.h == nil {
		return nil
	}
	return a.h.snapshot()
}
