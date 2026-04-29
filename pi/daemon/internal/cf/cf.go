// Package cf implements EdgeTX custom function (special function) evaluation.
//
// Custom functions are model-defined actions triggered by switch expressions:
//
//	OVERRIDE_CHANNEL    force a channel to a fixed value while triggered
//	PLAY_TRACK          play an audio track (one-shot on rising edge)
//	PLAY_SOUND          play a system sound
//	RESET               reset a timer / telemetry sensor
//	INSTANT_TRIM        capture stick position as trim
//
// In ZeroTX, OVERRIDE_CHANNEL is the safety-critical case. Big Talon's
//
//	swtch: !L3, func: OVERRIDE_CHANNEL, def: 0,-100,1
//
// forces CH0 (throttle) to -100% (CRSF min) whenever L3 (the arm latch)
// is false. This guarantees the throttle channel emits min-position values
// to the FC any time the radio isn't in armed state, regardless of where
// the throttle stick is.
//
// Audio events (PLAY_TRACK / PLAY_SOUND) are emitted on a buffered channel
// so the daemon (or future audio backend) can consume them. Without a
// consumer they're dropped after the buffer fills, which is fine — audio
// events aren't safety-critical.
//
// RESET and INSTANT_TRIM are recognized but no-ops on ZeroTX. They get
// logged once when first triggered so the user knows their model contains
// a CF the daemon doesn't act on.
package cf

import (
	"log"
	"strconv"
	"strings"

	"github.com/agoliveira/zerotx/pi/daemon/internal/ipc"
	"github.com/agoliveira/zerotx/pi/daemon/internal/model"
	"github.com/agoliveira/zerotx/pi/daemon/internal/source"
)

// Override carries one OVERRIDE_CHANNEL action to apply.
type Override struct {
	Channel int
	Value   float64 // [-1, 1]
}

// AudioEvent represents a queued PLAY_TRACK or PLAY_SOUND.
type AudioEvent struct {
	Kind   string // "track" or "sound"
	Name   string
	Repeat int // -1 for "1x" (one-shot), N for repeat count, 0 for infinite
}

// parsedCF holds the decoded def fields. Functions populate the relevant
// fields and the rest stay zero.
type parsedCF struct {
	enabled bool

	// OVERRIDE_CHANNEL
	channel int
	value   float64

	// PLAY_TRACK / PLAY_SOUND
	name   string
	repeat int

	// RESET
	target string

	invalid bool
}

// Processor evaluates custom functions each tick. Holds previous-tick
// trigger state for edge detection on one-shot events.
type Processor struct {
	model    *model.ZeroTXModel
	resolver *source.Resolver

	parsed         []parsedCF
	prevTriggered  []bool
	noopWarned     []bool // logged-once flag for unsupported functions
	enabledIgnored []bool // logged-once flag for enabled=0 entries

	// Audio events drain through this channel. Buffered; full means drop.
	Audio chan AudioEvent
}

// New constructs a Processor. The Audio channel has a small buffer so a
// few queued events can survive backpressure without blocking the tick.
func New(m *model.ZeroTXModel, r *source.Resolver) *Processor {
	n := 0
	if m != nil {
		n = len(m.EdgeTX.CustomFn)
	}
	p := &Processor{
		model:          m,
		resolver:       r,
		parsed:         make([]parsedCF, n),
		prevTriggered:  make([]bool, n),
		noopWarned:     make([]bool, n),
		enabledIgnored: make([]bool, n),
		Audio:          make(chan AudioEvent, 16),
	}
	if m != nil {
		for i := 0; i < n; i++ {
			p.parsed[i] = parseCF(m.EdgeTX.CustomFn[i])
		}
	}
	return p
}

// Tick evaluates every CF and returns the active channel overrides for
// this tick. Audio events are pushed to the Audio channel as a side
// effect.
//
// OVERRIDE_CHANNEL: returns an Override entry for every CF whose switch
// is currently true (level-triggered).
//
// PLAY_TRACK / PLAY_SOUND: emits an AudioEvent only on a rising edge of
// the trigger switch (one-shot).
func (p *Processor) Tick() []Override {
	if p.model == nil || len(p.model.EdgeTX.CustomFn) == 0 {
		return nil
	}
	var overrides []Override

	for i := 0; i < len(p.model.EdgeTX.CustomFn); i++ {
		cf := p.model.EdgeTX.CustomFn[i]
		pd := p.parsed[i]
		if pd.invalid {
			continue
		}

		triggered := false
		if cf.Swtch != "" && cf.Swtch != "NONE" {
			b, ok := p.resolver.ResolveBool(cf.Swtch)
			triggered = ok && b
		}

		switch cf.Func {
		case "OVERRIDE_CHANNEL":
			if !pd.enabled {
				if !p.enabledIgnored[i] {
					log.Printf("cf: CF%d OVERRIDE_CHANNEL disabled (enabled=0), skipped", i)
					p.enabledIgnored[i] = true
				}
				break
			}
			if triggered {
				overrides = append(overrides, Override{
					Channel: pd.channel,
					Value:   pd.value,
				})
			}

		case "PLAY_TRACK":
			if triggered && !p.prevTriggered[i] {
				p.emitAudio(AudioEvent{Kind: "track", Name: pd.name, Repeat: pd.repeat})
			}

		case "PLAY_SOUND":
			if triggered && !p.prevTriggered[i] {
				p.emitAudio(AudioEvent{Kind: "sound", Name: pd.name, Repeat: pd.repeat})
			}

		case "RESET", "INSTANT_TRIM":
			// No-op on ZeroTX (no internal timers/trims to reset). Log once
			// on first trigger so the user knows the daemon saw the CF but
			// has no action mapped.
			if triggered && !p.noopWarned[i] {
				log.Printf("cf: CF%d %s triggered but unsupported on ZeroTX (no action taken)", i, cf.Func)
				p.noopWarned[i] = true
			}

		case "", "FUNC_NONE":
			// Unconfigured slot.

		default:
			if !p.noopWarned[i] {
				log.Printf("cf: CF%d uses unsupported function %q (no action taken)", i, cf.Func)
				p.noopWarned[i] = true
			}
		}

		p.prevTriggered[i] = triggered
	}

	return overrides
}

// emitAudio pushes an event onto the audio channel, dropping if full.
func (p *Processor) emitAudio(ev AudioEvent) {
	select {
	case p.Audio <- ev:
	default:
		log.Printf("cf: audio channel full, dropped %s/%s", ev.Kind, ev.Name)
	}
}

// ApplyOverrides writes the overrides into the channel array. Out-of-
// range channel indices are skipped silently.
func ApplyOverrides(ch *[ipc.Channels]uint16, overrides []Override) {
	for _, o := range overrides {
		if o.Channel < 0 || o.Channel >= ipc.Channels {
			continue
		}
		ch[o.Channel] = normToCRSF(o.Value)
	}
}

// normToCRSF converts a normalized [-1, 1] value to a CRSF channel value
// in [CrsfChMin, CrsfChMax]. Identical to the mapper helper.
func normToCRSF(v float64) uint16 {
	if v > 1 {
		v = 1
	}
	if v < -1 {
		v = -1
	}
	span := float64(ipc.CrsfChMax) - float64(ipc.CrsfChMin)
	return uint16(float64(ipc.CrsfChMin) + (v+1.0)*0.5*span + 0.5)
}

// parseCF decodes a CustomFunction's def field per its function type.
// Trailing nulls in def strings (common for PLAY_TRACK names) are stripped.
func parseCF(cf model.CustomFunction) parsedCF {
	var pd parsedCF
	def := strings.TrimRight(cf.Def, "\x00")
	parts := splitAndTrim(def)

	switch cf.Func {
	case "OVERRIDE_CHANNEL":
		// channel(0-indexed), value(percent), enabled(0|1)
		if len(parts) < 3 {
			pd.invalid = true
			return pd
		}
		ch, err1 := strconv.Atoi(parts[0])
		val, err2 := strconv.ParseFloat(parts[1], 64)
		en, err3 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil || err3 != nil {
			pd.invalid = true
			return pd
		}
		pd.channel = ch
		pd.value = val / 100.0
		if pd.value > 1.0 {
			pd.value = 1.0
		}
		if pd.value < -1.0 {
			pd.value = -1.0
		}
		pd.enabled = en == 1

	case "PLAY_TRACK", "PLAY_SOUND":
		// name, repeat-id, "1x" / "Nx" / "0x"
		if len(parts) >= 1 {
			pd.name = strings.TrimRight(parts[0], "\x00")
		}
		// Repeat: "1x" = one-shot, "0x" = infinite, "Nx" = N times.
		// EdgeTX's exact semantics aren't documented; we treat "1x" as
		// once-only (which is the common case) and any "Nx" the same.
		// More elaborate behavior can come if/when audio backend lands.
		pd.repeat = 1

	case "RESET":
		// target sensor / timer name + mode int
		if len(parts) >= 1 {
			pd.target = parts[0]
		}

	case "INSTANT_TRIM":
		// Single int param (mode), no-op on ZeroTX.

	case "", "FUNC_NONE":
		// Unconfigured.

	default:
		// Unknown but not necessarily invalid; we log when triggered.
	}

	return pd
}

// splitAndTrim splits on commas and strips whitespace.
func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
