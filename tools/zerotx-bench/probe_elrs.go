package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// elrsProbe verifies that the ELRS TX module on the other end of the
// CRSF cable is alive. The probe goes through the RP2040 because
// ELRS is not directly Pi-accessible: it sits on the RP2040's UART
// RX/TX pair (~5m cable to the operator panel). When the RP2040
// emits CRSF channel frames, the ELRS module responds with its own
// link-stats / telemetry frames. The RP2040 firmware forwards those
// upward as MSG_TELEMETRY frames over USB-CDC.
//
// Method: open the RP2040 IPC link, send heartbeats at 100ms cadence
// for the probe window (keeps the firmware's state machine in
// LINK_OK so it emits CRSF), count MSG_TELEMETRY frames received.
// At least one frame in the window = ELRS is responding. None =
// either ELRS is unpowered, the cable is broken, or the firmware
// isn't forwarding telemetry.
//
// CAUTION: while the probe runs, the RP2040 emits live CRSF on its
// UART at 50Hz with safe default channel values (sticks centered,
// throttle low, arm low). If an aircraft is bound to this ELRS link
// and powered on, the aircraft WILL receive those frames. The
// bench tool is bench-only by design but this is a particularly
// sharp edge.
type elrsProbe struct{}

const (
	elrsWindow            = 3 * time.Second
	elrsHeartbeatInterval = 100 * time.Millisecond
)

func (elrsProbe) ID() string        { return "elrs" }
func (elrsProbe) Name() string      { return "ELRS TX (via RP2040)" }
func (elrsProbe) Category() string  { return "RF" }
func (elrsProbe) WiringRef() string { return "" }

func (elrsProbe) Probe(ctx context.Context) Result {
	r := Result{Details: map[string]string{}}

	c, byID, err := openIPCClient(rp2040Timeout)
	if err != nil {
		r.Status = StatusFail
		r.Error = "RP2040 link required for ELRS check: " + err.Error()
		r.Notes = "the ELRS probe needs the RP2040 probe to pass first -- ELRS is not directly accessible from the Pi"
		return r
	}
	defer c.Close()
	r.Details["via"] = byID

	stats, err := runELRSCapture(ctx, c, elrsWindow)
	if err != nil {
		r.Status = StatusFail
		r.Error = err.Error()
		return r
	}

	r.Details["window"] = elrsWindow.String()
	r.Details["telemetry frames"] = fmt.Sprintf("%d", stats.telemetryCount)
	r.Details["heartbeats sent"] = fmt.Sprintf("%d", stats.heartbeatsSent)
	r.Details["other frames"] = fmt.Sprintf("hb=%d log=%d input=%d", stats.heartbeatsRx, stats.logCount, stats.inputCount)
	if len(stats.telemetryTypes) > 0 {
		r.Details["crsf types seen"] = stats.telemetryTypeSummary()
	}

	if stats.telemetryCount == 0 {
		r.Status = StatusFail
		r.Notes = "no telemetry frames received -- check that ELRS TX is powered, the CRSF cable is connected to the operator panel, and the RP2040 is emitting CRSF (link state should be LINK_OK -- visible in 'RP2040' probe details)"
		return r
	}
	r.Status = StatusPass
	if stats.telemetryCount < 5 {
		r.Notes = fmt.Sprintf("only %d telemetry frames in %s -- ELRS is responding but link quality may be marginal", stats.telemetryCount, elrsWindow)
	}
	return r
}

func (elrsProbe) Tests() []TestAction {
	return []TestAction{
		{
			ID:    "extended-capture",
			Label: "Extended capture (10s)",
			Description: "10-second observation window. BENCH ONLY: while this runs, the RP2040 emits live CRSF at 50Hz on its UART. " +
				"Make sure no aircraft is bound to this ELRS link before pressing.",
			Run: elrsExtendedCapture,
		},
	}
}

// elrsStats accumulates frame counters during a probe window.
type elrsStats struct {
	telemetryCount int
	heartbeatsSent int
	heartbeatsRx   int
	logCount       int
	inputCount     int
	// telemetryTypes maps the CRSF type byte (payload[1]) to count.
	// MSG_TELEMETRY payload layout: [addr:1][type:1][crsf_payload:N]
	// so type lives at index 1.
	telemetryTypes map[byte]int
}

func (s *elrsStats) telemetryTypeSummary() string {
	if len(s.telemetryTypes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.telemetryTypes))
	for t, c := range s.telemetryTypes {
		parts = append(parts, fmt.Sprintf("%#02x=%d", t, c))
	}
	return strings.Join(parts, " ")
}

// runELRSCapture pumps heartbeats on the client while reading frames
// for the given window. Returns counters. Errors only on hard read
// failures; an empty telemetry count is a valid result the caller
// surfaces as fail+notes.
func runELRSCapture(ctx context.Context, c *ipcClient, window time.Duration) (*elrsStats, error) {
	stats := &elrsStats{telemetryTypes: map[byte]int{}}

	// Heartbeat pump goroutine.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go func() {
		t := time.NewTicker(elrsHeartbeatInterval)
		defer t.Stop()
		seq := byte(0)
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				if err := c.Send(ipcMsgHeartbeat, []byte{seq}); err == nil {
					stats.heartbeatsSent++
				}
				seq++
			}
		}
	}()

	// Wake the firmware into LINK_OK before counting telemetry.
	// Without this the first ~200ms of the window are wasted while
	// the firmware transitions from LINK_PENDING.
	_ = c.Send(ipcMsgHeartbeat, []byte{0})
	stats.heartbeatsSent++
	time.Sleep(150 * time.Millisecond)

	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		f, err := c.ReadFrame()
		if err != nil {
			break
		}
		switch f.Type {
		case ipcMsgTelemetry:
			stats.telemetryCount++
			if len(f.Payload) >= 2 {
				stats.telemetryTypes[f.Payload[1]]++
			}
		case ipcMsgHeartbeat:
			stats.heartbeatsRx++
		case ipcMsgLog:
			stats.logCount++
		case ipcMsgInputEvent:
			stats.inputCount++
		}
	}
	return stats, nil
}

func elrsExtendedCapture(ctx context.Context) (string, error) {
	c, _, err := openIPCClient(rp2040Timeout)
	if err != nil {
		return "", err
	}
	defer c.Close()

	stats, err := runELRSCapture(ctx, c, 10*time.Second)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Window:           10s\n")
	fmt.Fprintf(&sb, "Heartbeats sent:  %d\n", stats.heartbeatsSent)
	fmt.Fprintf(&sb, "Telemetry frames: %d (~%.1f/s)\n", stats.telemetryCount, float64(stats.telemetryCount)/10.0)
	if len(stats.telemetryTypes) > 0 {
		fmt.Fprintf(&sb, "CRSF types:       %s\n", stats.telemetryTypeSummary())
		// Highlight the most common type by count.
		var topType byte
		topCount := -1
		for t, c := range stats.telemetryTypes {
			if c > topCount {
				topType = t
				topCount = c
			}
		}
		fmt.Fprintf(&sb, "Dominant type:    %#02x (%s)\n", topType, crsfTypeName(topType))
	}
	fmt.Fprintf(&sb, "Heartbeats RX:    %d\n", stats.heartbeatsRx)
	fmt.Fprintf(&sb, "Log lines:        %d\n", stats.logCount)
	fmt.Fprintf(&sb, "Input events:     %d\n", stats.inputCount)
	if stats.telemetryCount == 0 {
		sb.WriteString("\nNo telemetry -- check ELRS power and CRSF cable.\n")
	}
	return sb.String(), nil
}

// crsfTypeName maps the most common CRSF type bytes to human names.
// The list is intentionally small -- the bench tool isn't a CRSF
// decoder, it just helps the operator recognize what they're seeing.
// Full type table lives in the daemon's telemetry decoder.
func crsfTypeName(t byte) string {
	switch t {
	case 0x14:
		return "LinkStatistics"
	case 0x08:
		return "BatterySensor"
	case 0x02:
		return "GPS"
	case 0x09:
		return "Baro/VarioSensor"
	case 0x1E:
		return "AttitudeSensor"
	case 0x21:
		return "FlightMode"
	case 0x29:
		return "DeviceInfo"
	case 0x32:
		return "Command"
	default:
		return "unknown"
	}
}

// unused: kept here to document the safe-default channel layout
// in case a future commit wants to send MsgChannelIntent instead
// of relying on the firmware's boot defaults. TAER, throttle low,
// arm low, others centered.
var _ = func() []byte {
	buf := make([]byte, 32)
	for i := 0; i < ipcChannels; i++ {
		var v uint16
		switch i {
		case 0, 4: // throttle slot (TAER) and arm slot
			v = ipcCrsfChMin
		default:
			v = ipcCrsfChMid
		}
		binary.LittleEndian.PutUint16(buf[i*2:], v)
	}
	return buf
}
