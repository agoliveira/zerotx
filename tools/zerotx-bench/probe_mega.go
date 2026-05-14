package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// megaProbe talks the Mega 2560 IO board's line-based ASCII protocol
// over USB-CDC at 115200 baud. The protocol (see firmware/io/README)
// is the same one tools/zerotx-iohal-config and the daemon's iohub
// package speak; the bench probe uses a thin slice: GET version,
// GET caps, and a few per-subsystem SET/GET commands as test actions.
//
// Requires daemon-stopped (enforced by the bench tool's coexistence
// check). When daemon is running it owns the Mega's serial port
// exclusively.
type megaProbe struct{}

const (
	megaBaud    = 115200
	megaTimeout = 1500 * time.Millisecond
)

// megaUSBPatterns are case-insensitive substrings to match against
// /dev/serial/by-id/ entries. Stock Arduino Mega 2560 R3 boards
// enumerate with various forms; cover the common ones.
var megaUSBPatterns = []string{
	"Arduino_LLC_Mega_2560",
	"www.arduino.cc__0042",
	"Mega_2560",
}

func (megaProbe) ID() string        { return "mega" }
func (megaProbe) Name() string      { return "Mega 2560 IO board" }
func (megaProbe) Category() string  { return "MCU" }
func (megaProbe) WiringRef() string { return "mega-pin-allocation" }

func (megaProbe) Probe(ctx context.Context) Result {
	r := Result{Details: map[string]string{}}

	ports, err := findUSBSerial(megaUSBPatterns...)
	if err != nil {
		r.Status = StatusFail
		r.Error = err.Error()
		return r
	}
	if len(ports) == 0 {
		r.Status = StatusFail
		r.Notes = "no Mega 2560 found under /dev/serial/by-id/ -- check USB cable and that the board is powered"
		return r
	}
	if len(ports) > 1 {
		r.Notes = fmt.Sprintf("multiple Mega-like devices found (%d); probing the first: %s", len(ports), ports[0].ByID)
	}
	r.Details["device"] = ports[0].ByID
	r.Details["path"] = ports[0].Path

	port, err := openSerialAt(ports[0].Path, megaBaud)
	if err != nil {
		r.Status = StatusFail
		r.Error = "open serial: " + err.Error()
		r.Notes = "is your user in the dialout group? `sudo usermod -aG dialout $USER` then log out/in"
		return r
	}
	defer port.Close()

	rw := newLineRW(port, megaTimeout)
	rw.drain() // flush any boot banner / async EVENT lines

	// GET version: a one-line ">"  reply identifies firmware version.
	if err := rw.send("GET version"); err != nil {
		r.Status = StatusFail
		r.Error = "send GET version: " + err.Error()
		return r
	}
	versionLines, err := rw.readUntil(func(l string) bool { return strings.HasPrefix(l, "> version") || strings.HasPrefix(l, "! ") })
	if err != nil {
		r.Status = StatusFail
		r.Error = "GET version: " + err.Error()
		return r
	}
	if len(versionLines) > 0 {
		r.Details["fw"] = strings.TrimPrefix(versionLines[len(versionLines)-1], "> version ")
	}

	// GET caps: lists subsystems present. The line starts with
	// "> caps " followed by space-separated subsystem identifiers.
	if err := rw.send("GET caps"); err != nil {
		r.Status = StatusFail
		r.Error = "send GET caps: " + err.Error()
		return r
	}
	capsLines, err := rw.readUntil(func(l string) bool { return strings.HasPrefix(l, "> caps") || strings.HasPrefix(l, "! ") })
	if err != nil {
		r.Status = StatusFail
		r.Error = "GET caps: " + err.Error()
		return r
	}
	if len(capsLines) == 0 {
		r.Status = StatusFail
		r.Notes = "no caps response from Mega -- is the firmware running? (LED on the board should be steady or blinking)"
		return r
	}
	capsLine := capsLines[len(capsLines)-1]
	subsystems := strings.Fields(strings.TrimPrefix(capsLine, "> caps"))
	r.Details["subsystems"] = strings.Join(subsystems, " ")
	r.Details["subsystem count"] = fmt.Sprintf("%d", len(subsystems))

	r.Status = StatusPass
	return r
}

func (megaProbe) Tests() []TestAction {
	return []TestAction{
		{
			ID:          "led-blink",
			Label:       "Blink led.0 three times",
			Description: "Toggles led.0 on/off three times at 250ms intervals. Requires led.0 to be wired; on a board with the scaffold-only LED, the test will run but no LED visibly lights.",
			Run:         megaBlinkLED,
		},
		{
			ID:          "buzzer-beep",
			Label:       "Buzzer 1000Hz / 200ms",
			Description: "Plays one short beep on the buzzer subsystem.",
			Run:         megaBeepBuzzer,
		},
		{
			ID:          "vfd-write",
			Label:       "Write test message to vfd.0",
			Description: "Sends 'BENCH OK' to vfd.0 line 0, then reverts to 'IDLE'.",
			Run:         megaVFDWrite,
		},
		{
			ID:          "buttons-snapshot",
			Label:       "Read button states",
			Description: "Queries every button.0..9 once and reports which are pressed.",
			Run:         megaButtonStates,
		},
	}
}

// withMega is a helper that handles the open/setup/cleanup boilerplate
// for one-shot Mega test actions. The fn receives a ready lineRW.
func withMega(ctx context.Context, fn func(rw *lineRW) (string, error)) (string, error) {
	ports, err := findUSBSerial(megaUSBPatterns...)
	if err != nil {
		return "", err
	}
	if len(ports) == 0 {
		return "", fmt.Errorf("no Mega found under %s", serialByIDDir)
	}
	port, err := openSerialAt(ports[0].Path, megaBaud)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", ports[0].Path, err)
	}
	defer port.Close()
	rw := newLineRW(port, megaTimeout)
	rw.drain()
	return fn(rw)
}

func megaBlinkLED(ctx context.Context) (string, error) {
	return withMega(ctx, func(rw *lineRW) (string, error) {
		for i := 0; i < 3; i++ {
			if err := rw.send("SET led.0 on"); err != nil {
				return "", err
			}
			select {
			case <-time.After(250 * time.Millisecond):
			case <-ctx.Done():
				return "", ctx.Err()
			}
			if err := rw.send("SET led.0 off"); err != nil {
				return "", err
			}
			select {
			case <-time.After(250 * time.Millisecond):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "led.0 cycled on/off 3 times", nil
	})
}

func megaBeepBuzzer(ctx context.Context) (string, error) {
	return withMega(ctx, func(rw *lineRW) (string, error) {
		if err := rw.send("SET buzzer beep 1000 200"); err != nil {
			return "", err
		}
		// Wait long enough for the beep to finish before closing the port.
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "buzzer beeped at 1000Hz for 200ms", nil
	})
}

func megaVFDWrite(ctx context.Context) (string, error) {
	return withMega(ctx, func(rw *lineRW) (string, error) {
		if err := rw.send(`SET vfd.0 mode "BENCH OK"`); err != nil {
			return "", err
		}
		select {
		case <-time.After(800 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if err := rw.send(`SET vfd.0 mode "IDLE"`); err != nil {
			return "", err
		}
		return "vfd.0 displayed 'BENCH OK', then reverted to 'IDLE'", nil
	})
}

func megaButtonStates(ctx context.Context) (string, error) {
	return withMega(ctx, func(rw *lineRW) (string, error) {
		var sb strings.Builder
		for i := 0; i < 10; i++ {
			cmd := fmt.Sprintf("GET button.%d", i)
			if err := rw.send(cmd); err != nil {
				return sb.String(), err
			}
			lines, err := rw.readUntil(func(l string) bool {
				return strings.HasPrefix(l, fmt.Sprintf("> button.%d", i)) || strings.HasPrefix(l, "! ")
			})
			if err != nil {
				// Treat as "not wired" rather than an error.
				fmt.Fprintf(&sb, "button.%d: not responding (likely not wired)\n", i)
				continue
			}
			if len(lines) > 0 {
				last := lines[len(lines)-1]
				if strings.HasPrefix(last, "! ") {
					fmt.Fprintf(&sb, "button.%d: %s\n", i, last)
				} else {
					state := strings.TrimPrefix(last, fmt.Sprintf("> button.%d ", i))
					fmt.Fprintf(&sb, "button.%d: %s\n", i, state)
				}
			}
		}
		return sb.String(), nil
	})
}
