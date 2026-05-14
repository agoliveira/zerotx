package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// esp32Probe talks the HUB75 panel driver's line-based protocol
// (see docs/protocols/display.md) over USB-CDC at 115200. The
// device announces itself with a `DISP READY ...` line on boot,
// emits `DISP HEARTBEAT uptime=...` periodically, and answers
// `DISP PING` with `DISP PONG`.
//
// Probe: opens the port, drains buffered output (catching any
// READY/HEARTBEAT lines), sends PING, waits for PONG. Pass if PONG
// arrives within timeout.
//
// Requires daemon-stopped (coexistence check).
type esp32Probe struct{}

const (
	esp32Baud    = 115200
	esp32Timeout = 1500 * time.Millisecond
)

// esp32USBPatterns covers the common ESP32 USB-serial chips. The
// project's specific board uses (TODO: confirm on bench); the
// list is permissive on purpose because ESP32 boards ship with
// many different USB-UART bridges.
var esp32USBPatterns = []string{
	"Silicon_Labs",     // CP210x family (very common on DevKit boards)
	"CP2102",           // explicit CP2102 line
	"Espressif",        // ESP32-S3 / S2 native USB CDC
	"QinHeng",          // CH340/CH341
	"USB_Serial",       // some CH340 clones
	"FT232",            // FTDI fallback
}

func (esp32Probe) ID() string        { return "esp32-display" }
func (esp32Probe) Name() string      { return "ESP32 HUB75 panel" }
func (esp32Probe) Category() string  { return "MCU" }
func (esp32Probe) WiringRef() string { return "" }

func (esp32Probe) Probe(ctx context.Context) Result {
	r := Result{Details: map[string]string{}}

	ports, err := findUSBSerial(esp32USBPatterns...)
	if err != nil {
		r.Status = StatusFail
		r.Error = err.Error()
		return r
	}
	if len(ports) == 0 {
		r.Status = StatusFail
		r.Notes = "no ESP32-like device found under /dev/serial/by-id/. " +
			"Expected USB-serial chips: CP210x, CH340, FT232, or native ESP32-S3. " +
			"Check USB cable and that the board is powered."
		return r
	}
	if len(ports) > 1 {
		// Could be RP2040 caught in a permissive pattern; that's
		// a problem for commit D2 (which uses different patterns).
		// For now flag and probe the first.
		r.Notes = fmt.Sprintf("multiple ESP32-like devices (%d) -- probing %s", len(ports), ports[0].ByID)
	}
	r.Details["device"] = ports[0].ByID
	r.Details["path"] = ports[0].Path

	port, err := openSerialAt(ports[0].Path, esp32Baud)
	if err != nil {
		r.Status = StatusFail
		r.Error = "open serial: " + err.Error()
		return r
	}
	defer port.Close()

	rw := newLineRW(port, esp32Timeout)

	// Drain with a longer window to catch READY/HEARTBEAT lines.
	// Capture any DISP READY for the details pane.
	readyLine := drainAndCaptureESP32(rw, 500*time.Millisecond)
	if readyLine != "" {
		r.Details["ready"] = strings.TrimPrefix(readyLine, "DISP READY ")
	}

	if err := rw.send("DISP PING"); err != nil {
		r.Status = StatusFail
		r.Error = "send PING: " + err.Error()
		return r
	}
	lines, err := rw.readUntil(func(l string) bool {
		return l == "DISP PONG" || strings.HasPrefix(l, "DISP ERROR")
	})
	if err != nil {
		r.Status = StatusFail
		r.Error = "no PONG within " + esp32Timeout.String()
		r.Notes = "device opened cleanly but did not answer PING. Firmware running? " +
			"(boards send a READY line at power-on; if that's missing too the firmware may have hung)"
		return r
	}
	last := lines[len(lines)-1]
	if strings.HasPrefix(last, "DISP ERROR") {
		r.Status = StatusFail
		r.Error = last
		return r
	}
	r.Details["ping rtt"] = "<" + esp32Timeout.String()
	r.Status = StatusPass
	return r
}

func (esp32Probe) Tests() []TestAction {
	return []TestAction{
		{
			ID:          "cycle-modes",
			Label:       "Cycle through display modes",
			Description: "IDLE → PREFLIGHT → FLIGHT → ALARM → IDLE, ~800ms each. Operator watches the HUB75 panel to confirm the renderer is alive.",
			Run:         esp32CycleModes,
		},
		{
			ID:          "brightness-sweep",
			Label:       "Brightness sweep (10 → 100 → 50)",
			Description: "Ramps brightness from 10% to 100% then settles at 50%.",
			Run:         esp32BrightnessSweep,
		},
	}
}

// drainAndCaptureESP32 reads buffered output for `window` and returns
// the most recent DISP READY line seen (or empty). Other lines
// (HEARTBEAT, etc.) are discarded -- the boot READY is the
// interesting one for the details pane.
func drainAndCaptureESP32(rw *lineRW, window time.Duration) string {
	_ = rw.port.SetReadTimeout(50 * time.Millisecond)
	defer rw.port.SetReadTimeout(rw.timeout)
	deadline := time.Now().Add(window)
	var lastReady string
	for time.Now().Before(deadline) {
		line, err := rw.br.ReadString('\n')
		if err != nil || line == "" {
			continue
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "DISP READY") {
			lastReady = line
		}
	}
	return lastReady
}

func withESP32(ctx context.Context, fn func(rw *lineRW) (string, error)) (string, error) {
	ports, err := findUSBSerial(esp32USBPatterns...)
	if err != nil {
		return "", err
	}
	if len(ports) == 0 {
		return "", fmt.Errorf("no ESP32 device under %s", serialByIDDir)
	}
	port, err := openSerialAt(ports[0].Path, esp32Baud)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", ports[0].Path, err)
	}
	defer port.Close()
	rw := newLineRW(port, esp32Timeout)
	rw.drain()
	return fn(rw)
}

func esp32CycleModes(ctx context.Context) (string, error) {
	return withESP32(ctx, func(rw *lineRW) (string, error) {
		modes := []string{"IDLE", "PREFLIGHT", "FLIGHT", "ALARM", "IDLE"}
		for _, m := range modes {
			if err := rw.send("DISP MODE " + m); err != nil {
				return "", err
			}
			select {
			case <-time.After(800 * time.Millisecond):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return fmt.Sprintf("cycled through %d modes; left at %s", len(modes), modes[len(modes)-1]), nil
	})
}

func esp32BrightnessSweep(ctx context.Context) (string, error) {
	return withESP32(ctx, func(rw *lineRW) (string, error) {
		steps := []int{10, 30, 60, 100, 50}
		for _, b := range steps {
			cmd := fmt.Sprintf("DISP BRIGHTNESS %d", b)
			if err := rw.send(cmd); err != nil {
				return "", err
			}
			select {
			case <-time.After(400 * time.Millisecond):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return fmt.Sprintf("brightness swept 10 -> 30 -> 60 -> 100 -> %d (final)", steps[len(steps)-1]), nil
	})
}
