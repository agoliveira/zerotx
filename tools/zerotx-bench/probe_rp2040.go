package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// rp2040Probe verifies the RP2040 CRSF generator via the binary
// IPC protocol. Opens the USB-CDC port, sends MSG_HELLO, expects
// MSG_HELLO_ACK with the firmware's protocol version + version
// string. Also captures any incidental HEARTBEAT / LOG frames
// during the probe window so the details pane shows ground truth
// for liveness.
//
// Requires daemon-stopped. The RP2040's link state machine
// transitions BOOT -> PENDING when USB enumerates, then -> OK on
// first heartbeat. Our HELLO doesn't count as a heartbeat for that
// transition, so this probe sends a heartbeat first to wake the
// link into emitting CRSF.
type rp2040Probe struct{}

const rp2040Timeout = 2 * time.Second

func (rp2040Probe) ID() string        { return "rp2040" }
func (rp2040Probe) Name() string      { return "RP2040 CRSF generator" }
func (rp2040Probe) Category() string  { return "MCU" }
func (rp2040Probe) WiringRef() string { return "" }

func (rp2040Probe) Probe(ctx context.Context) Result {
	r := Result{Details: map[string]string{}}

	c, byID, err := openIPCClient(rp2040Timeout)
	if err != nil {
		r.Status = StatusFail
		r.Error = err.Error()
		return r
	}
	defer c.Close()
	r.Details["device"] = byID

	// Send our HELLO. The firmware always responds with HELLO_ACK
	// regardless of whether it initiated the handshake first.
	if err := c.Send(ipcMsgHello, buildHelloPayload(ipcProtoVer, "zerotx-bench")); err != nil {
		r.Status = StatusFail
		r.Error = "send hello: " + err.Error()
		return r
	}

	// Also send one heartbeat so the firmware's state machine
	// progresses into LINK_OK (CRSF emission on). This is
	// asynchronous to the HELLO handshake but doesn't hurt to
	// pre-emit. seq = 0 is fine since the firmware doesn't
	// validate ordering.
	_ = c.Send(ipcMsgHeartbeat, []byte{0})

	deadline := time.Now().Add(rp2040Timeout)
	var (
		gotAck       bool
		ackProto     uint8
		ackVersion   string
		heartbeats   int
		logs         []string
		telemetries  int
		inputs       int
		unknown      int
	)
	for time.Now().Before(deadline) {
		f, err := c.ReadFrame()
		if err != nil {
			// Timeout / EOF -- stop collecting.
			break
		}
		switch f.Type {
		case ipcMsgHelloAck:
			gotAck = true
			ackProto, ackVersion = parseHelloPayload(f.Payload)
		case ipcMsgHeartbeat:
			heartbeats++
		case ipcMsgLog:
			s := strings.TrimSpace(string(f.Payload))
			if s != "" && len(logs) < 8 {
				logs = append(logs, s)
			}
		case ipcMsgTelemetry:
			telemetries++
		case ipcMsgInputEvent:
			inputs++
		default:
			unknown++
		}
	}

	r.Details["hello-ack"] = fmt.Sprintf("%t", gotAck)
	if gotAck {
		r.Details["fw proto version"] = fmt.Sprintf("%d", ackProto)
		r.Details["fw version string"] = ackVersion
	}
	r.Details["heartbeats seen"] = fmt.Sprintf("%d in %s", heartbeats, rp2040Timeout)
	r.Details["telemetry frames"] = fmt.Sprintf("%d", telemetries)
	r.Details["input events"] = fmt.Sprintf("%d", inputs)
	if unknown > 0 {
		r.Details["unknown frames"] = fmt.Sprintf("%d", unknown)
	}
	if len(logs) > 0 {
		r.Details["recent log lines"] = strings.Join(logs, " | ")
	}

	if !gotAck {
		r.Status = StatusFail
		r.Notes = "no HELLO_ACK received; firmware may not be running or may be on an incompatible protocol version"
		return r
	}
	if ackProto != ipcProtoVer {
		r.Status = StatusFail
		r.Notes = fmt.Sprintf("protocol version mismatch: firmware speaks v%d, bench tool expects v%d", ackProto, ipcProtoVer)
		return r
	}
	r.Status = StatusPass
	if heartbeats == 0 {
		r.Notes = "HELLO_ACK received but no heartbeats in window -- firmware is responding but its emission loop may be wedged"
	}
	return r
}

func (rp2040Probe) Tests() []TestAction {
	return []TestAction{
		{
			ID:          "capture-logs",
			Label:       "Capture 5s of firmware logs",
			Description: "Sends heartbeats to keep the link awake and records every MSG_LOG line the RP2040 emits.",
			Run:         rp2040CaptureLogs,
		},
	}
}

func rp2040CaptureLogs(ctx context.Context) (string, error) {
	c, _, err := openIPCClient(rp2040Timeout)
	if err != nil {
		return "", err
	}
	defer c.Close()

	// Send a heartbeat every 100ms so the firmware stays in LINK_OK.
	hbCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		seq := byte(0)
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				_ = c.Send(ipcMsgHeartbeat, []byte{seq})
				seq++
			}
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	var sb strings.Builder
	count := 0
	for time.Now().Before(deadline) {
		f, err := c.ReadFrame()
		if err != nil {
			break
		}
		if f.Type == ipcMsgLog {
			fmt.Fprintf(&sb, "%s\n", strings.TrimSpace(string(f.Payload)))
			count++
		}
	}
	cancel()
	if count == 0 {
		return "(no log lines emitted during the 5s window)", nil
	}
	return fmt.Sprintf("%d log lines:\n%s", count, sb.String()), nil
}
