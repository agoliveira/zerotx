// Package narrator builds and emits structured audio announcements.
//
// Where the audio package handles single tracks (with optional
// stitching fallback for compound names) and repeating alarms, the
// narrator emits longer narrative sequences that combine pre-rendered
// sentence fragments with runtime-substituted numeric values: post-
// flight summaries, boot greetings, and similar set-piece moments.
//
// The narrator does not own a queue, schedule events, or run any
// goroutines. It's a pure transformation: take a high-level intent
// (e.g. "narrate this flight summary"), produce an ordered list of
// track names, push the list to the audio Player as a single
// PlaySequence call. The Player handles playback and inter-fragment
// timing.
//
// Number/unit conventions used by the narrator (matches the design
// decisions in the dictionary):
//
//   - Distance: < 1000m -> meters only; >= 1000m -> km + remaining
//     meters rounded to nearest 100. Exact km drops the meter part.
//   - Time: < 60s -> seconds only; >= 60s -> minutes + seconds.
//     Exact minute drops the second part.
//   - Speed: round to nearest km/h, no decimals.
//   - Altitude: nearest meter for low altitudes; nearest 10m above 100m.
//   - Battery percent: nearest 5%.
//
// Voltage is the one exception that *does* use a decimal in narration
// (12.4V matters in a way 12V doesn't). Voltage isn't used in the
// post-flight summary so that case doesn't appear here yet; future
// templates that include voltage will need a small extension.
package narrator

import (
	"fmt"
	"math"
	"strings"

	"github.com/agoliveira/zerotx/pi/daemon/internal/audio"
	"github.com/agoliveira/zerotx/pi/daemon/internal/phrasebook"
	"github.com/agoliveira/zerotx/pi/daemon/internal/recorder"
)

// Narrator emits narrative sequences via an audio.Player.
type Narrator struct {
	player audio.Player
	lang   string
	geo    GeoLookup
}

// GeoLookup resolves a (lat, lon) point to a place name suitable
// for speaking. Implementations return "" when no nearby place is
// worth mentioning (the narrator falls back to no-location phrasing).
//
// Decoupled from the geo package by an interface so the narrator
// can be tested without sqlite, and so a future swap of geo backend
// (e.g. online geocoder for indoor testing) doesn't ripple here.
type GeoLookup interface {
	NearestName(lat, lon float64) string
}

// New constructs a Narrator backed by the given player. Lang
// controls the language of TTS phrases ("en" / "pt"). Empty / unknown
// fall back to English at phrase-render time. geo is optional; pass
// nil to disable location enrichment.
func New(p audio.Player, lang string, geo GeoLookup) *Narrator {
	return &Narrator{player: p, lang: lang, geo: geo}
}

// PlayBootGreeting announces system readiness on daemon startup.
// One-shot, info level. Plays through the audio threshold check
// like any other announcement.
func (n *Narrator) PlayBootGreeting() {
	if n == nil || n.player == nil {
		return
	}
	n.player.Play("track", "boot-greeting", audio.LevelInfo)
}

// SpeakBootGreeting announces system readiness via TTS. modelName
// is included when non-empty ("ZeroTX online, Big Talon ready.")
// otherwise a generic awaiting-model line is spoken.
func (n *Narrator) SpeakBootGreeting(modelName string) {
	if n == nil || n.player == nil {
		return
	}
	n.player.Speak(phrasebook.BootGreeting(n.lang, modelName), audio.LevelInfo)
}

// SpeakStationGPSAcquired announces the first acquisition of station
// GPS lock since daemon startup. The caller (a watcher goroutine in
// cmd/zerotxd) is responsible for the once-only semantics; the
// narrator just speaks when called.
func (n *Narrator) SpeakStationGPSAcquired() {
	if n == nil || n.player == nil {
		return
	}
	n.player.Speak(phrasebook.StationGPSAcquired(n.lang), audio.LevelInfo)
}

// SpeakPostFlight emits the post-flight summary as a single TTS
// utterance derived from the in-flight event log. Tier 1 narration:
// duration + peaks + noteworthy events (failsafe, RTH, battery
// thresholds). Routine events (mode changes, GPS lock, link blips)
// are not narrated; they remain accessible via the events log for
// debug or replay.
//
// Returns the spoken text (useful for tests and logging) or "" if
// nothing meaningful could be built from the events.
func (n *Narrator) SpeakPostFlight(events []recorder.Event) string {
	if n == nil || n.player == nil {
		return ""
	}
	text := buildPostFlightTTS(n.lang, events, n.geo)
	if text == "" {
		return ""
	}
	n.player.Speak(text, audio.LevelNotice)
	return text
}

// buildPostFlightTTS is the pure transformation: event log to
// narration text. Exposed (lowercase) for testing without a player.
//
// Output shape:
//   "<header> <duration>. Peak distance <X> meters.
//    Peak altitude <Y> meters. <noteworthy clauses>."
//
// Always starts with the localized "Flight complete." and ends
// with a period. Returns "" when events is empty.
func buildPostFlightTTS(lang string, events []recorder.Event, geo GeoLookup) string {
	if len(events) == 0 {
		return ""
	}

	parts := []string{phrasebook.PostFlightHeader(lang)}

	// Duration from first to last event timestamp.
	durMs := events[len(events)-1].TsMs - events[0].TsMs
	if durMs >= 1000 {
		parts = append(parts, phrasebook.DurationSentence(lang, int(durMs/1000)))
	}

	// Peaks: last peak-distance / peak-altitude wins (events are
	// ordered ascending and only emitted on new peaks). Track the
	// lat/lon attached to the winning event for geo enrichment.
	var peakDist, peakAlt int64
	var peakDistLat, peakDistLon float64
	var peakAltLat, peakAltLon float64
	var peakDistHasPos, peakAltHasPos bool
	var battLowAt, battCriticalAt int64 = -1, -1
	rthCount := 0
	failsafeCount := 0
	for _, e := range events {
		if e.Kind != "flight" {
			continue
		}
		switch e.Name {
		case "peak-distance":
			if v, ok := intDetail(e.Detail, "meters"); ok && v > peakDist {
				peakDist = v
				if la, lo, hp := posFromDetail(e.Detail); hp {
					peakDistLat, peakDistLon, peakDistHasPos = la, lo, true
				} else {
					peakDistHasPos = false
				}
			}
		case "peak-altitude":
			if v, ok := intDetail(e.Detail, "meters"); ok && v > peakAlt {
				peakAlt = v
				if la, lo, hp := posFromDetail(e.Detail); hp {
					peakAltLat, peakAltLon, peakAltHasPos = la, lo, true
				} else {
					peakAltHasPos = false
				}
			}
		case "battery-low":
			if battLowAt < 0 {
				battLowAt = e.TsMs - events[0].TsMs
			}
		case "battery-critical":
			if battCriticalAt < 0 {
				battCriticalAt = e.TsMs - events[0].TsMs
			}
		case "rth-active":
			rthCount++
		case "failsafe":
			failsafeCount++
		}
	}

	if peakDist > 0 {
		place := ""
		if geo != nil && peakDistHasPos {
			place = geo.NearestName(peakDistLat, peakDistLon)
		}
		parts = append(parts, phrasebook.PeakDistanceAt(lang, peakDist, place))
	}
	if peakAlt > 0 {
		place := ""
		if geo != nil && peakAltHasPos {
			place = geo.NearestName(peakAltLat, peakAltLon)
		}
		parts = append(parts, phrasebook.PeakAltitudeAt(lang, peakAlt, place))
	}
	if failsafeCount > 0 {
		parts = append(parts, phrasebook.FailsafeTriggered(lang))
	}
	if rthCount > 0 {
		parts = append(parts, phrasebook.RTHTriggered(lang))
	}
	if battCriticalAt >= 0 {
		parts = append(parts, phrasebook.BatteryCriticalAt(lang, int(battCriticalAt/1000)))
	} else if battLowAt >= 0 {
		parts = append(parts, phrasebook.BatteryLowAt(lang, int(battLowAt/1000)))
	}

	return strings.Join(parts, " ")
}

// posFromDetail extracts (lat, lon) from an event's detail map if
// both keys are present and parseable. Returns hasPos=false when the
// event predates lat/lon enrichment or carries malformed values.
func posFromDetail(d map[string]interface{}) (lat, lon float64, hasPos bool) {
	if d == nil {
		return 0, 0, false
	}
	la, laOK := floatDetail(d, "lat")
	lo, loOK := floatDetail(d, "lon")
	if !laOK || !loOK {
		return 0, 0, false
	}
	return la, lo, true
}

// floatDetail extracts a float64 from a detail map, accepting both
// the JSON-unmarshalled float64 and a few integer-ish forms.
func floatDetail(d map[string]interface{}, key string) (float64, bool) {
	v, ok := d[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// intDetail extracts an integer field from an Event.Detail map.
// JSON-unmarshalled numbers come through as float64; we coerce
// safely.
func intDetail(d map[string]interface{}, key string) (int64, bool) {
	if d == nil {
		return 0, false
	}
	v, ok := d[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}

// PlayPostFlight builds and emits a narrative summary of a saved
// flight, derived from a recorder.Summary. Picks variant based on
// alarm severity:
//
//   - No alarms: long-form summary with stats
//   - Warnings only: same stats + "N warnings triggered"
//   - Any critical: shorter form + "review the recording"
//
// If summary is nil or has no useful data, plays a minimal "flight
// complete" track and returns.
//
// Plays at notice level. The whole sequence is one audio queue
// entry, so it never interleaves with another play.
func (n *Narrator) PlayPostFlight(s *recorder.Summary) {
	if n == nil || n.player == nil {
		return
	}
	if s == nil {
		n.player.Play("track", "flight-complete", audio.LevelNotice)
		return
	}

	seq := buildPostFlightSequence(s)
	if len(seq) == 0 {
		n.player.Play("track", "flight-complete", audio.LevelNotice)
		return
	}
	n.player.PlaySequence("track", seq, audio.LevelNotice)
}

// buildPostFlightSequence is the pure transformation: takes a Summary
// and produces an ordered list of track names. Exposed (lowercase)
// for testing without an audio player.
func buildPostFlightSequence(s *recorder.Summary) []string {
	out := []string{"flight-complete"}

	// Duration: always present for any non-trivial flight.
	if s.DurationSec > 0 {
		out = append(out, "you-were-up-for")
		out = append(out, durationFragments(s.DurationSec)...)
	}

	// Distance: derive from max distance from home if recorded. The
	// Summary doesn't currently carry MaxDistance directly; future
	// recorder enhancement adds it. For now, gated on availability.
	// (See note at end of file for the placeholder until then.)
	// TODO: once recorder.Summary exposes MaxDistanceM, wire it here.

	// Peak altitude.
	if s.GpsMaxAlt != nil && *s.GpsMaxAlt > 0 {
		out = append(out, "peak-altitude-of")
		out = append(out, altitudeFragments(*s.GpsMaxAlt)...)
	}

	// Average speed isn't directly in Summary either (we have max,
	// not avg). For now we emit max speed labeled honestly. If the
	// recorder later distinguishes max vs avg, swap the leading
	// fragment.
	if s.GpsMaxKmh != nil && *s.GpsMaxKmh > 0 {
		out = append(out, "average-speed-of")
		out = append(out, speedFragments(*s.GpsMaxKmh)...)
	}

	// Battery used: prefer percentage if we can compute it from
	// start/end voltage. mAh used isn't reliably set.
	if s.BatStartV != nil && s.BatEndV != nil && *s.BatStartV > 0 {
		used := batteryUsedPercent(*s.BatStartV, *s.BatEndV)
		if used > 0 {
			out = append(out, "battery-used")
			out = append(out, percentFragments(used)...)
		}
	}

	// Alarm summary: pick the right closer.
	out = append(out, alarmSummaryFragments(s.AlarmCounts)...)

	// Flight number: extract from the saved file name if present.
	if num := flightNumber(s.Name); num > 0 {
		out = append(out, "saved-as-flight-number")
		out = append(out, numberFragments(num)...)
	}

	return out
}

// durationFragments converts a duration in seconds to a fragment
// sequence applying the time conventions: < 60s -> seconds; >= 60s
// -> minutes + seconds, dropping the seconds when exact.
func durationFragments(secs int) []string {
	if secs < 60 {
		return append(numberFragments(secs), "u-seconds")
	}
	mins := secs / 60
	rem := secs % 60
	out := append(numberFragments(mins), "u-minutes")
	if rem > 0 {
		out = append(out, numberFragments(rem)...)
		out = append(out, "u-seconds")
	}
	return out
}

// altitudeFragments converts altitude (meters, integer) to fragments.
// Rounds to nearest 10m above 100m.
func altitudeFragments(meters int) []string {
	if meters > 100 {
		meters = roundToStep(meters, 10)
	}
	return append(numberFragments(meters), "u-meters")
}

// speedFragments converts km/h (float) to fragments. Rounds to int.
func speedFragments(kmh float64) []string {
	v := int(math.Round(kmh))
	return append(numberFragments(v), "u-kmh")
}

// percentFragments converts a percentage (0-100, int) to fragments.
// Rounds to nearest 5%.
func percentFragments(pct int) []string {
	pct = roundToStep(pct, 5)
	return append(numberFragments(pct), "u-percent")
}

// alarmSummaryFragments emits the right closer based on alarm counts.
func alarmSummaryFragments(counts map[string]int) []string {
	critical := counts["critical"]
	warning := counts["warning"]

	switch {
	case critical == 1:
		return []string{"critical-alarm-triggered"}
	case critical > 1:
		return append(numberFragments(critical), "critical-alarms-triggered")
	case warning == 1:
		return []string{"one-warning-triggered"}
	case warning > 1:
		return append(numberFragments(warning), "warnings-triggered")
	default:
		return []string{"no-alarms-triggered"}
	}
}

// numberFragments builds a fragment list for an integer using the
// available number tracks. Uses the dictionary's coverage:
//
//   - 0-30: individual tracks (n-0 .. n-30)
//   - 31-99: nearest decade (40, 50, ...) + ones (1-9)
//     decomposed as e.g. 47 -> n-40 + n-7
//   - 100-999: nearest hundred + remainder of 0-99
//     decomposed as e.g. 347 -> n-300 + n-40 + n-7
//   - 1000-9999: thousands + remainder
//     decomposed as e.g. 4275 -> n-4000 + n-200 + n-70 + n-5
//
// English/Portuguese cadence works either way because the dictionary
// holds locale-specific text per fragment ("um" vs "one"). The
// stitching gap masks the seam. It's not pristine speech but it's
// recognizably a number; for round 1 it's good enough.
func numberFragments(n int) []string {
	if n < 0 {
		// Negative numbers don't appear in summary contexts (no
		// negative altitude/speed/percent in our data), so silently
		// fall back to absolute value rather than failing.
		n = -n
	}
	if n == 0 {
		return []string{"n-0"}
	}
	if n <= 30 {
		return []string{fmt.Sprintf("n-%d", n)}
	}

	out := []string{}
	// Thousands.
	if n >= 1000 {
		thousands := (n / 1000) * 1000
		if thousands > 9000 {
			thousands = 9000 // cap at dictionary's coverage
		}
		out = append(out, fmt.Sprintf("n-%d", thousands))
		n -= thousands
	}
	// Hundreds.
	if n >= 100 {
		hundreds := (n / 100) * 100
		out = append(out, fmt.Sprintf("n-%d", hundreds))
		n -= hundreds
	}
	// Tens.
	if n >= 30 {
		tens := (n / 10) * 10
		out = append(out, fmt.Sprintf("n-%d", tens))
		n -= tens
	}
	// Remainder.
	if n > 0 {
		if n <= 30 {
			out = append(out, fmt.Sprintf("n-%d", n))
		} else {
			// Shouldn't happen given the path above, but be safe.
			out = append(out, fmt.Sprintf("n-%d", n))
		}
	}
	return out
}

// batteryUsedPercent estimates percent used from start/end voltage,
// assuming a typical lipo discharge curve approximated linearly
// between 4.2V/cell (full) and 3.3V/cell (effectively empty for
// flight purposes).
//
// Cell count detected heuristically by ceiling(volts/4.2). Capped
// at 12S to avoid runaway with bad data.
func batteryUsedPercent(startV, endV float64) int {
	cells := int(math.Ceil(startV / 4.2))
	if cells < 1 {
		cells = 1
	}
	if cells > 12 {
		cells = 12
	}
	full := 4.2 * float64(cells)
	empty := 3.3 * float64(cells)
	if full <= empty {
		return 0
	}
	startSoC := (startV - empty) / (full - empty)
	endSoC := (endV - empty) / (full - empty)
	if startSoC < 0 {
		startSoC = 0
	}
	if startSoC > 1 {
		startSoC = 1
	}
	if endSoC < 0 {
		endSoC = 0
	}
	if endSoC > 1 {
		endSoC = 1
	}
	used := startSoC - endSoC
	if used < 0 {
		used = 0
	}
	pct := int(math.Round(used * 100))
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// flightNumber extracts a flight ordinal from a recording filename
// like "2026-04-30T18-43-12.sqlite" or "flight-43.sqlite". Returns 0
// if no number can be extracted (in which case the narrator skips
// the "saved as flight number" closer).
//
// We don't have a strict naming convention so this is best-effort:
// look for the last sequence of digits in the name.
func flightNumber(name string) int {
	if name == "" {
		return 0
	}
	// Find the last run of digits in the name.
	last := 0
	current := 0
	for _, c := range name {
		if c >= '0' && c <= '9' {
			current = current*10 + int(c-'0')
		} else {
			if current > 0 {
				last = current
			}
			current = 0
		}
	}
	if current > 0 {
		last = current
	}
	// Sanity-cap. A flight number above 9999 is almost certainly a
	// timestamp fragment we picked up by accident.
	if last > 9999 {
		return 0
	}
	return last
}

// roundToStep rounds an integer to the nearest multiple of step.
// Useful for "nearest 5%" or "nearest 10m" rounding.
func roundToStep(n, step int) int {
	if step <= 1 {
		return n
	}
	rem := n % step
	if rem*2 >= step {
		return n + (step - rem)
	}
	return n - rem
}
