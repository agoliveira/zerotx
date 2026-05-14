package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// ledProbe verifies the heartbeat LED on GPIO 17 (Pi header pin 11).
// Active-high, 1k series resistor to LED cathode at pin 9 (GND).
//
// On Pi 400 (BCM2711) the 40-pin header lives on /dev/gpiochip0.
// Pi 5 moved it to /dev/gpiochip4 (RP1-driven); we default to 0 and
// fall back to 4 if 0 doesn't expose line 17. Future Pi revisions
// would need another fallback.
//
// Probing: confirm the gpiochip is openable and that GPIO 17 isn't
// already claimed by another process (daemon should be stopped per
// the coexistence check, but a stale claim is possible).
//
// Test actions:
//   - "Blink 3 times": pulses on/off three times at ~250ms.
//   - "Solid on 2s": holds high for 2 seconds.
//
// Both test actions release the line cleanly when done -- the
// daemon picks up where it left off when restarted. Uses gpioset
// from libgpiod (apt install gpiod) rather than a Go gpiocdev
// dependency; matches what the operator would type manually and
// keeps the bench tool's dep surface small.
type ledProbe struct{}

const (
	ledGPIO          = 17 // BCM numbering
	ledHeaderPin     = 11
	ledDefaultChip   = "/dev/gpiochip0"
	ledFallbackChip  = "/dev/gpiochip4"
	ledPulseInterval = 250 * time.Millisecond
)

func (ledProbe) ID() string        { return "led-heartbeat" }
func (ledProbe) Name() string      { return "Heartbeat LED (GPIO 17)" }
func (ledProbe) Category() string  { return "GPIO" }
func (ledProbe) WiringRef() string { return "heartbeat-led" }

func (ledProbe) Probe(ctx context.Context) Result {
	r := Result{Details: map[string]string{
		"bcm gpio":   fmt.Sprintf("%d", ledGPIO),
		"header pin": fmt.Sprintf("%d", ledHeaderPin),
	}}

	chip, err := findGPIOChip()
	if err != nil {
		r.Status = StatusFail
		r.Error = err.Error()
		r.Notes = "could not find a usable gpiochip on this system -- is libgpiod-bin installed? `sudo apt install gpiod`"
		return r
	}
	r.Details["gpiochip"] = chip

	// `gpioinfo` lists each line on the chip; we check that line 17
	// exists and isn't consumed by another process. A consumed line
	// shows "[used]" with the consumer name.
	out, err := runCmd(ctx, 2*time.Second, "gpioinfo", chip)
	if err != nil {
		r.Status = StatusFail
		r.Error = err.Error()
		return r
	}
	lineEntry := findGPIOLine(out, ledGPIO)
	if lineEntry == "" {
		r.Status = StatusFail
		r.Notes = fmt.Sprintf("line %d not present on %s", ledGPIO, chip)
		return r
	}
	r.Details["line"] = strings.TrimSpace(lineEntry)
	if strings.Contains(lineEntry, "[used]") {
		r.Status = StatusFail
		r.Notes = "GPIO 17 is currently held by another process -- stop whatever is using it (daemon? gpiomon?) before running test actions"
		return r
	}

	r.Status = StatusPass
	r.Notes = "ready -- use the test actions to confirm the LED actually blinks"
	return r
}

func (ledProbe) Tests() []TestAction {
	return []TestAction{
		{
			ID:          "blink",
			Label:       "Blink 3 times",
			Description: "Pulses the LED on/off three times at 250ms intervals.",
			Run: func(ctx context.Context) (string, error) {
				return runBlink(ctx, 3)
			},
		},
		{
			ID:          "solid",
			Label:       "Solid on for 2 seconds",
			Description: "Holds GPIO 17 high for 2 seconds, then releases.",
			Run: func(ctx context.Context) (string, error) {
				return runSolid(ctx, 2*time.Second)
			},
		},
	}
}

// findGPIOChip returns the first available gpiochip that we recognize
// as carrying the 40-pin header. Default to gpiochip0 (Pi 4 / Pi 400);
// fall back to gpiochip4 (Pi 5).
func findGPIOChip() (string, error) {
	for _, c := range []string{ledDefaultChip, ledFallbackChip} {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("neither %s nor %s exist", ledDefaultChip, ledFallbackChip)
}

// findGPIOLine extracts the gpioinfo line for the given BCM number.
// gpioinfo lines look like:
//
//	line  17:      unnamed       unused   input  active-high
//	line  17:      unnamed   "heartbeat"  output active-high [used]
//
// We return the raw line (whitespace-trimmed) or "" if not found.
func findGPIOLine(gpioinfoOutput string, line int) string {
	prefix := fmt.Sprintf("line %3d:", line)
	for _, l := range strings.Split(gpioinfoOutput, "\n") {
		if strings.Contains(l, prefix) {
			return l
		}
	}
	// gpioinfo formats are slightly version-dependent; try without
	// padded width.
	prefix2 := fmt.Sprintf("line %d:", line)
	for _, l := range strings.Split(gpioinfoOutput, "\n") {
		if strings.Contains(l, prefix2) {
			return l
		}
	}
	return ""
}

// runBlink pulses the LED N times via gpioset. Each pulse is one
// gpioset invocation with --mode=time to hold for ledPulseInterval,
// then implicit release on exit.
func runBlink(ctx context.Context, times int) (string, error) {
	chip, err := findGPIOChip()
	if err != nil {
		return "", err
	}
	for i := 0; i < times; i++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// Modern gpioset: `gpioset --mode=time --sec=0 --usec=250000 chip 17=1`
		// Older: `gpioset --mode=time -s 0 -u 250000 chip 17=1`
		// The --mode flag is consistent across both.
		_, err := runCmd(ctx, ledPulseInterval+1*time.Second,
			"gpioset",
			"--mode=time",
			fmt.Sprintf("--usec=%d", ledPulseInterval.Microseconds()),
			chip,
			fmt.Sprintf("%d=1", ledGPIO),
		)
		if err != nil {
			return "", fmt.Errorf("pulse %d/%d: %w", i+1, times, err)
		}
		// Off interval between pulses.
		if i < times-1 {
			select {
			case <-time.After(ledPulseInterval):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	return fmt.Sprintf("blinked %d times (%s on / %s off)", times, ledPulseInterval, ledPulseInterval), nil
}

func runSolid(ctx context.Context, duration time.Duration) (string, error) {
	chip, err := findGPIOChip()
	if err != nil {
		return "", err
	}
	_, err = runCmd(ctx, duration+1*time.Second,
		"gpioset",
		"--mode=time",
		fmt.Sprintf("--usec=%d", duration.Microseconds()),
		chip,
		fmt.Sprintf("%d=1", ledGPIO),
	)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("held GPIO %d high for %s, then released", ledGPIO, duration), nil
}
