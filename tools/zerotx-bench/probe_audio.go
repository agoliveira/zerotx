package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// audioProbe enumerates ALSA playback devices via `aplay -l` and
// offers a tone-test action. Matches what the daemon uses for
// audio output (shell out to aplay/paplay) -- no overlap when the
// daemon is stopped.
//
// Probe: parse `aplay -l` for "card N:" lines. Pass if at least
// one card is present. Doesn't distinguish USB vs builtin since
// both work; the operator can identify their interface by name.
//
// Test action: `speaker-test -t sine -f 440 -l 1` plays one cycle
// of a 440Hz sine wave to the default device. Takes about 3s.
// The default device is whatever ALSA is configured to route to
// (usually controllable via /etc/asound.conf or per-user
// ~/.asoundrc). Operator hears the tone -> audio output works
// end-to-end including the case speakers and amp.
type audioProbe struct{}

const audioToneDuration = 3 * time.Second

func (audioProbe) ID() string        { return "audio" }
func (audioProbe) Name() string      { return "USB audio interface" }
func (audioProbe) Category() string  { return "USB" }
func (audioProbe) WiringRef() string { return "" }

func (audioProbe) Probe(ctx context.Context) Result {
	r := Result{Details: map[string]string{}}

	out, err := runCmd(ctx, 2*time.Second, "aplay", "-l")
	if err != nil {
		r.Status = StatusFail
		r.Error = err.Error()
		r.Notes = "is alsa-utils installed? `sudo apt install alsa-utils`"
		return r
	}
	cards := parseAplayList(out)
	if len(cards) == 0 {
		r.Status = StatusFail
		r.Notes = "aplay -l reported no playback devices -- USB audio interface unplugged, or kernel hasn't enumerated it"
		return r
	}
	r.Details["count"] = fmt.Sprintf("%d", len(cards))
	for _, c := range cards {
		r.Details[fmt.Sprintf("card %d", c.card)] = c.description
	}
	r.Status = StatusPass
	return r
}

func (audioProbe) Tests() []TestAction {
	return []TestAction{
		{
			ID:          "tone",
			Label:       "Play 440Hz tone",
			Description: "One cycle of a 440Hz sine wave on the default ALSA device (~3 seconds). Operator should hear it from the case speakers.",
			Run: func(ctx context.Context) (string, error) {
				out, err := runCmd(ctx, audioToneDuration+3*time.Second,
					"speaker-test", "-t", "sine", "-f", "440", "-l", "1")
				if err != nil {
					return out, err
				}
				return "tone played on default ALSA device", nil
			},
		},
	}
}

// aplayCard is one entry parsed from `aplay -l` output.
type aplayCard struct {
	card        int
	description string // human-readable card identifier
}

// parseAplayList extracts card lines from aplay's output. Format:
//
//	card 0: Headphones [bcm2835 Headphones], device 0: bcm2835 Headphones [bcm2835 Headphones]
//	card 1: U24XL [iCON U24XL], device 0: USB Audio [USB Audio]
//
// We take the leading "card N: <description>" portion before the
// comma; that's the card-identifier we care about.
func parseAplayList(output string) []aplayCard {
	var cards []aplayCard
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "card ") {
			continue
		}
		// "card 0: Headphones [bcm2835 Headphones], device ..."
		comma := strings.Index(line, ",")
		head := line
		if comma > 0 {
			head = line[:comma]
		}
		// head = "card 0: Headphones [bcm2835 Headphones]"
		colon := strings.Index(head, ":")
		if colon < 0 {
			continue
		}
		var cardNum int
		if _, err := fmt.Sscanf(head[:colon], "card %d", &cardNum); err != nil {
			continue
		}
		desc := strings.TrimSpace(head[colon+1:])
		cards = append(cards, aplayCard{card: cardNum, description: desc})
	}
	return cards
}
