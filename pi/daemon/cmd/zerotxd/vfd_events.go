package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/telemetry"
	"github.com/agoliveira/zerotx/pi/daemon/internal/vfd"
)

// vfdEventEmitter drives the VFD's animation state machine by
// translating daemon-side state changes into "E ..." commands.
//
// The firmware owns animation; the daemon's job here is to surface
// semantic events at sensible rates. Specifically:
//
//   - tick: 10Hz batched count of CRSF frames received in the last
//     100ms window. Drives the activity bar.
//   - mode: emitted on edges (string change).
//   - lq: rate-limited to 1Hz.
//   - batt: rate-limited to 1Hz.
//   - arm 0|1: emitted on edges; armed state set externally via
//     setArmed() callback hook.
//   - warn / critical / failsafe: emitted by the audio threshold
//     hook (not from this loop).
//
// All emissions are best-effort; serial errors are not propagated
// because the firehose's WriteLines path also writes to the same
// driver and reports its own failures.
type vfdEventEmitter struct {
	drv vfd.Driver
	tel *telemetry.State

	// Atomic counter incremented by the telemetry hook on each
	// inbound CRSF frame; sampled and zeroed every 100ms.
	frameTicks int64
}

func newVFDEventEmitter(drv vfd.Driver, tel *telemetry.State) *vfdEventEmitter {
	return &vfdEventEmitter{drv: drv, tel: tel}
}

// CountFrame increments the per-window frame counter. Called from
// the telemetry payload handler on every frame received from the FC.
// Cheap (atomic add); safe to call from hot paths.
func (e *vfdEventEmitter) CountFrame() {
	if e == nil {
		return
	}
	atomic.AddInt64(&e.frameTicks, 1)
}

// SetArmed pushes an arm-edge event. Called from the arm-state
// callback in main.go.
func (e *vfdEventEmitter) SetArmed(armed bool) {
	if e == nil || e.drv == nil {
		return
	}
	v := "0"
	if armed {
		v = "1"
	}
	_ = e.drv.Event("arm", v)
}

// Run blocks until ctx is cancelled. Polls telemetry state at 10Hz
// for tick batching, and at 1Hz for mode/lq/batt change detection.
func (e *vfdEventEmitter) Run(ctx context.Context) {
	if e == nil || e.drv == nil {
		return
	}

	tickTimer := time.NewTicker(100 * time.Millisecond) // 10Hz
	defer tickTimer.Stop()
	slowTimer := time.NewTicker(1 * time.Second)
	defer slowTimer.Stop()

	var (
		lastMode string
		lastLQ   uint8
		haveLQ   bool
		lastBatt float64
		haveBatt bool
	)

	for {
		select {
		case <-ctx.Done():
			return

		case <-tickTimer.C:
			n := atomic.SwapInt64(&e.frameTicks, 0)
			if n > 0 {
				_ = e.drv.Event("tick", fmt.Sprintf("%d", n))
			}

		case <-slowTimer.C:
			snap := e.tel.Snapshot()
			if snap.FlightMode != nil && !snap.FlightMode.Stale {
				m := snap.FlightMode.Data.Mode
				if m != "" && m != lastMode {
					lastMode = m
					_ = e.drv.Event("mode", m)
				}
			}
			if snap.Link != nil && !snap.Link.Stale {
				lq := uint8(snap.Link.Data.UplinkLQ)
				if !haveLQ || lq != lastLQ {
					haveLQ = true
					lastLQ = lq
					_ = e.drv.Event("lq", fmt.Sprintf("%d", lq))
				}
			}
			if snap.Battery != nil && !snap.Battery.Stale {
				v := snap.Battery.Data.Volts
				// Emit on >= 0.1V change; below that it's noise.
				if !haveBatt || absF(v-lastBatt) >= 0.1 {
					haveBatt = true
					lastBatt = v
					_ = e.drv.Event("batt", fmt.Sprintf("%.1fV", v))
				}
			}
		}
	}
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
