package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// gpsProbe verifies the u-blox GPS module on UART3. The Pi 400's
// UART3 surfaces as /dev/ttyAMA1 with the standard pre-Pi5
// device-tree binding (dtoverlay=uart3). Module speaks NMEA-0183
// at 9600 baud by default; we don't reconfigure it.
//
// Probing strategy:
//
//	1. Stat the device path. If missing, fail with a hint about
//	   dtoverlay.
//	2. Open at 9600 baud, read for ~1.5s, count NMEA sentences.
//	3. Parse the most recent GGA frame to extract fix state.
//
// "Pass" here means "the GPS is wired up and chattering". It does
// not mean a fix is present -- a brand-new module indoors with no
// satellite view still emits NMEA, just with empty fix fields.
// Drift between "wired" and "useful" is captured in the details.
type gpsProbe struct{}

const (
	gpsDevice = "/dev/ttyAMA1"
	gpsBaud   = 9600
	// probeWindow is how long we read NMEA during the basic probe.
	// Sentences arrive at 1 Hz nominally; 1.5s is enough to see at
	// least one of each common sentence type.
	gpsProbeWindow = 1500 * time.Millisecond
	// captureWindow is the duration of the "Capture NMEA" test
	// action. Longer because it doubles as a "show me the GPS is
	// alive over time" diagnostic.
	gpsCaptureWindow = 10 * time.Second
)

func (gpsProbe) ID() string        { return "gps-ublox" }
func (gpsProbe) Name() string      { return "u-blox GPS" }
func (gpsProbe) Category() string  { return "UART" }
func (gpsProbe) WiringRef() string { return "u-blox-gps-module" }

func (gpsProbe) Probe(ctx context.Context) Result {
	r := Result{Details: map[string]string{
		"device": gpsDevice,
		"baud":   strconv.Itoa(gpsBaud),
	}}

	if _, err := os.Stat(gpsDevice); err != nil {
		r.Status = StatusFail
		r.Error = err.Error()
		r.Notes = "device path missing -- check that `dtoverlay=uart3` is in /boot/config.txt and the Pi rebooted, or that no other process owns the port"
		return r
	}

	stats, err := captureNMEA(ctx, gpsProbeWindow)
	if err != nil {
		r.Status = StatusFail
		r.Error = err.Error()
		return r
	}

	r.Details["sentences"] = strconv.Itoa(stats.total)
	r.Details["sentence types"] = stats.typesSummary()
	r.Details["latest fix"] = stats.fixDescription()

	if stats.total == 0 {
		r.Status = StatusFail
		r.Notes = "device opened cleanly but no NMEA received in " + gpsProbeWindow.String() +
			" -- check TX/RX direction (UART3 TXD pin 7 -> module RX, UART3 RXD pin 29 <- module TX) and power"
		return r
	}
	r.Status = StatusPass
	if stats.fixQuality == 0 {
		r.Notes = "GPS is chattering but no fix yet -- normal indoors or with poor sky view; outdoors it should acquire within 30-90s"
	}
	return r
}

func (gpsProbe) Tests() []TestAction {
	return []TestAction{
		{
			ID:          "capture",
			Label:       "Capture 10 seconds of NMEA",
			Description: "Counts sentences by type, reports fix quality. Longer window than the auto-probe.",
			Run: func(ctx context.Context) (string, error) {
				stats, err := captureNMEA(ctx, gpsCaptureWindow)
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "Window:     %s\n", gpsCaptureWindow)
				fmt.Fprintf(&sb, "Total:      %d sentences\n", stats.total)
				fmt.Fprintf(&sb, "By type:\n")
				for _, kt := range stats.sortedTypes() {
					fmt.Fprintf(&sb, "  %s: %d\n", kt.kind, kt.count)
				}
				fmt.Fprintf(&sb, "Fix:        %s\n", stats.fixDescription())
				if stats.lastGGA != "" {
					fmt.Fprintf(&sb, "Latest GGA: %s\n", stats.lastGGA)
				}
				return sb.String(), nil
			},
		},
	}
}

// nmeaStats accumulates what we learn from a read window.
type nmeaStats struct {
	total      int
	byType     map[string]int
	fixQuality int    // GGA field 6: 0=invalid, 1=GPS, 2=DGPS, ...
	numSats    int    // GGA field 7
	lastGGA    string // raw last GGA line for the capture action
}

func (s *nmeaStats) typesSummary() string {
	if len(s.byType) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(s.byType))
	for _, kt := range s.sortedTypes() {
		parts = append(parts, fmt.Sprintf("%s=%d", kt.kind, kt.count))
	}
	return strings.Join(parts, " ")
}

type kindCount struct {
	kind  string
	count int
}

func (s *nmeaStats) sortedTypes() []kindCount {
	out := make([]kindCount, 0, len(s.byType))
	for k, c := range s.byType {
		out = append(out, kindCount{k, c})
	}
	// Bubble sort by kind for deterministic display; tiny list.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].kind < out[i].kind {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func (s *nmeaStats) fixDescription() string {
	switch s.fixQuality {
	case 0:
		return "no fix"
	case 1:
		return fmt.Sprintf("GPS fix (%d sats)", s.numSats)
	case 2:
		return fmt.Sprintf("DGPS fix (%d sats)", s.numSats)
	case 4:
		return fmt.Sprintf("RTK fixed (%d sats)", s.numSats)
	case 5:
		return fmt.Sprintf("RTK float (%d sats)", s.numSats)
	default:
		return fmt.Sprintf("quality=%d (%d sats)", s.fixQuality, s.numSats)
	}
}

// captureNMEA opens the GPS port, reads for `window`, and returns
// summary stats. Uses raw os.Open since the daemon's serial-port
// library would pull in CGo for tcsetattr; for 9600 baud read-only
// the default kernel termios is fine for our purposes.
func captureNMEA(ctx context.Context, window time.Duration) (*nmeaStats, error) {
	f, err := os.OpenFile(gpsDevice, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", gpsDevice, err)
	}
	defer f.Close()

	// Read deadline so we don't block indefinitely on a silent
	// device. SetReadDeadline on /dev/ttyAMA* works since Linux 3.x.
	if err := f.SetReadDeadline(time.Now().Add(window + 500*time.Millisecond)); err != nil {
		// Some kernels reject SetDeadline on tty devices. Fall back
		// to a goroutine-driven close on the parent context.
		go func() {
			select {
			case <-ctx.Done():
				_ = f.Close()
			case <-time.After(window + 500*time.Millisecond):
				_ = f.Close()
			}
		}()
	}

	stats := &nmeaStats{byType: map[string]int{}}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024), 1024) // NMEA lines are <80 bytes
	deadline := time.Now().Add(window)
	for scanner.Scan() {
		if time.Now().After(deadline) {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "$") {
			continue
		}
		// Sentence type is chars 3-6: "$GP" or "$GN" prefix, then 3-letter type.
		if len(line) < 7 {
			continue
		}
		kind := line[3:6]
		stats.byType[kind]++
		stats.total++

		if kind == "GGA" {
			stats.lastGGA = line
			parseGGA(line, stats)
		}
	}
	if err := scanner.Err(); err != nil && stats.total == 0 {
		// Surfacing read errors when no data arrived helps diagnose
		// permission and termios issues. With even one sentence we
		// assume the connection works and the read just ended via
		// deadline.
		return stats, fmt.Errorf("read %s: %w", gpsDevice, err)
	}
	return stats, nil
}

// parseGGA pulls fix quality and satellite count from a GGA sentence.
// Field order:
//
//	$xxGGA,time,lat,N/S,lon,E/W,quality,numSV,hdop,alt,M,geoidSep,M,age,stationID*chk
//
// We only need fields 6 (quality, 0-based index 6) and 7 (numSats).
// Robust to malformed sentences -- silently skips on parse error.
func parseGGA(line string, stats *nmeaStats) {
	parts := strings.Split(line, ",")
	if len(parts) < 8 {
		return
	}
	if q, err := strconv.Atoi(parts[6]); err == nil {
		stats.fixQuality = q
	}
	if n, err := strconv.Atoi(parts[7]); err == nil {
		stats.numSats = n
	}
}
