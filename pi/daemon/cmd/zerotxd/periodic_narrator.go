package main

// periodic_narrator.go: while armed, every N seconds, speak a
// status report assembled from the current telemetry snapshot.
//
// Fully optional: when content is empty, the goroutine still runs
// but emits nothing. The operator opts in via -narrate-content
// (comma-separated field list) or -narrate-preset.
//
// Future work (noted in handover): expose this configuration in
// the GUI so the operator can toggle fields and adjust the
// interval without restarting the daemon.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/audio"
	"github.com/agoliveira/zerotx/pi/daemon/internal/telemetry"
)

// narrateField is one item that can appear in a periodic status
// announcement. Values are stable strings used in CLI flag parsing.
type narrateField string

const (
	fieldBattery   narrateField = "battery"
	fieldDistance  narrateField = "distance"
	fieldAltitude  narrateField = "altitude"
	fieldSpeed     narrateField = "speed"
	fieldLink      narrateField = "link"
	fieldMode      narrateField = "mode"
	fieldTimeAloft narrateField = "time-aloft"
)

// allNarrateFields is the canonical order in which fields appear in
// the spoken status. Keeping a fixed order means narration always
// flows the same way regardless of operator-flag ordering.
var allNarrateFields = []narrateField{
	fieldBattery,
	fieldDistance,
	fieldAltitude,
	fieldSpeed,
	fieldLink,
	fieldMode,
	fieldTimeAloft,
}

// narratePresets maps preset names to field sets.
var narratePresets = map[string][]narrateField{
	"compact": {fieldBattery, fieldDistance, fieldAltitude},
	"full":    allNarrateFields,
}

// resolveNarrateContent picks the active field set from CLI flags.
// Explicit -narrate-content wins over -narrate-preset. Unknown
// fields/presets are dropped with a log warning. Returns an empty
// slice when nothing is configured (the caller treats this as
// "narration disabled").
func resolveNarrateContent(content, preset string) []narrateField {
	content = strings.TrimSpace(content)
	preset = strings.TrimSpace(preset)
	if content != "" {
		return parseFieldList(content)
	}
	if preset != "" {
		fields, ok := narratePresets[preset]
		if !ok {
			log.Printf("narrate: unknown preset %q (valid: compact, full); narration disabled", preset)
			return nil
		}
		return fields
	}
	return nil
}

// parseFieldList splits a comma-separated list of field names into
// narrateField values, in the canonical order. Unknown names are
// logged and dropped.
func parseFieldList(s string) []narrateField {
	requested := map[narrateField]bool{}
	for _, raw := range strings.Split(s, ",") {
		name := narrateField(strings.TrimSpace(strings.ToLower(raw)))
		if name == "" {
			continue
		}
		valid := false
		for _, f := range allNarrateFields {
			if f == name {
				valid = true
				break
			}
		}
		if !valid {
			log.Printf("narrate: unknown field %q (valid: %s); skipped",
				name, strings.Join(narrateFieldNames(), ", "))
			continue
		}
		requested[name] = true
	}
	var out []narrateField
	for _, f := range allNarrateFields {
		if requested[f] {
			out = append(out, f)
		}
	}
	return out
}

func narrateFieldNames() []string {
	out := make([]string, len(allNarrateFields))
	for i, f := range allNarrateFields {
		out[i] = string(f)
	}
	return out
}

// runPeriodicNarrator is the goroutine that ticks every interval
// while armed and speaks a status report. Stops when ctx is cancelled.
//
// Armed state is provided via the isArmed callback to decouple the
// goroutine from the arm state machine. The arm transitions reset
// the tick: on arm we wait one full interval before the first
// announcement (so it doesn't overlap with the pre-flight summary).
func runPeriodicNarrator(
	ctx context.Context,
	player audio.Player,
	tel *telemetry.State,
	isArmed func() bool,
	armEvents <-chan struct{}, // pings when arm transitions occur
	interval time.Duration,
	fields []narrateField,
) {
	if len(fields) == 0 || interval <= 0 {
		return
	}

	var armStart time.Time
	timer := time.NewTimer(interval)
	defer timer.Stop()
	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(interval)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-armEvents:
			// Arm state changed. If newly armed, mark start time
			// and restart the timer so the first announcement
			// comes one full interval after arm. If disarmed,
			// just reset (next arm starts the cycle clean).
			if isArmed() {
				armStart = time.Now()
			} else {
				armStart = time.Time{}
			}
			resetTimer()
		case <-timer.C:
			timer.Reset(interval)
			if !isArmed() {
				continue
			}
			snap := tel.Snapshot()
			text := buildPeriodicStatus(snap, fields, time.Since(armStart))
			if text == "" {
				continue
			}
			player.Speak(text, audio.LevelInfo)
		}
	}
}

// buildPeriodicStatus assembles a status sentence from the current
// snapshot, including only the requested fields. Missing telemetry
// for a requested field is silently dropped (we narrate what we
// know, not what we don't). Returns "" if nothing meaningful is
// available — the caller skips the announcement entirely.
//
// timeAloft is passed in (rather than read from the snapshot) so
// the caller controls the clock; this also makes the function
// trivially testable.
func buildPeriodicStatus(snap telemetry.Snapshot, fields []narrateField, timeAloft time.Duration) string {
	var parts []string
	for _, f := range fields {
		switch f {
		case fieldBattery:
			if snap.Battery == nil || snap.Battery.Stale {
				continue
			}
			b := snap.Battery.Data
			frag := "Battery"
			if b.Percent > 0 {
				frag += fmt.Sprintf(" %d percent", b.Percent)
			}
			if b.Volts > 0 {
				frag += fmt.Sprintf(", %.1f volts", b.Volts)
			}
			if frag == "Battery" {
				continue
			}
			parts = append(parts, frag+".")
		case fieldDistance:
			if snap.Home == nil {
				continue
			}
			parts = append(parts, fmt.Sprintf("Distance %d meters.", snap.Home.Data.DistanceM))
		case fieldAltitude:
			if snap.GPS == nil || snap.GPS.Stale {
				continue
			}
			parts = append(parts, fmt.Sprintf("Altitude %d meters.", snap.GPS.Data.AltMeters))
		case fieldSpeed:
			if snap.GPS == nil || snap.GPS.Stale {
				continue
			}
			parts = append(parts, fmt.Sprintf("Speed %d kilometers per hour.", int(snap.GPS.Data.GroundKmh+0.5)))
		case fieldLink:
			if snap.Link == nil || snap.Link.Stale {
				continue
			}
			parts = append(parts, fmt.Sprintf("Link %d percent.", snap.Link.Data.UplinkLQ))
		case fieldMode:
			if snap.FlightMode == nil {
				continue
			}
			m := snap.FlightMode.Data.Mode
			if m == "" {
				continue
			}
			parts = append(parts, "Mode "+humanizeMode(m)+".")
		case fieldTimeAloft:
			sec := int(timeAloft.Seconds())
			if sec <= 0 {
				continue
			}
			parts = append(parts, "Aloft "+spokenDuration(sec)+".")
		}
	}
	return strings.Join(parts, " ")
}

// spokenDuration produces "X minutes Y seconds" / "X seconds" for
// inline narration. Local copy of the same logic used by the
// post-flight narrator; keeping it here avoids the cmd package
// importing the narrator package just for one helper.
func spokenDuration(sec int) string {
	if sec < 60 {
		if sec == 1 {
			return "1 second"
		}
		return fmt.Sprintf("%d seconds", sec)
	}
	mins := sec / 60
	rem := sec % 60
	out := fmt.Sprintf("%d minutes", mins)
	if mins == 1 {
		out = "1 minute"
	}
	if rem > 0 {
		if rem == 1 {
			out += " 1 second"
		} else {
			out += fmt.Sprintf(" %d seconds", rem)
		}
	}
	return out
}
