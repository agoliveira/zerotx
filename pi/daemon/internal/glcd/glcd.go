// Package glcd drives the 128x64 graphic LCD attached to the Mega IO
// board. Pushes attitude (pitch, roll, heading) from the daemon's
// telemetry state to the LCD at a fixed cadence; the LCD firmware
// re-renders an artificial-horizon widget on each new frame.
//
// Wire protocol on the IO bus (one command per push):
//
//   SET glcd attitude <pitch> <roll> <hdg>
//
// The firmware tracks its own staleness timer (defaults ~1.5s); when
// the driver stops sending, the LCD falls back to a "NO LINK" screen
// automatically. No explicit teardown command is required.
//
// Push cadence: the daemon's telemetry attitude updates arrive at
// ~10 Hz from CRSF. The driver matches that rate. Higher rates waste
// USB-CDC bandwidth and CPU on the Mega without visual benefit;
// lower rates make the horizon jittery.
package glcd

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/iohub"
	"github.com/agoliveira/zerotx/pi/daemon/internal/telemetry"
)

// PushInterval is the cadence at which the daemon resends attitude
// to the LCD. Matches the nominal CRSF attitude frame rate.
const PushInterval = 100 * time.Millisecond

// Driver owns the push goroutine. Single-instance; the Mega has one
// GLCD slot.
type Driver struct {
	hub  iohub.Client
	tele *telemetry.State

	// metrics
	pushed  uint64
	skipped uint64
}

// New constructs a driver bound to the given iohub client and
// telemetry state. The driver does not start until Run is called.
// Pass a Null or Log iohub.Client when no Mega is connected; the
// driver becomes effectively a no-op.
func New(hub iohub.Client, tele *telemetry.State) *Driver {
	return &Driver{hub: hub, tele: tele}
}

// Run blocks until ctx is cancelled, pushing the current attitude
// snapshot to the GLCD every PushInterval. Snapshots without fresh
// attitude data are silently skipped -- the firmware will fall back
// to its NO LINK screen on its own when no commands arrive for a
// while.
func (d *Driver) Run(ctx context.Context) {
	t := time.NewTicker(PushInterval)
	defer t.Stop()
	log.Printf("glcd: push loop started (interval=%s)", PushInterval)
	for {
		select {
		case <-ctx.Done():
			log.Printf("glcd: push loop exiting (pushed=%d skipped=%d)", d.pushed, d.skipped)
			return
		case <-t.C:
			d.pushOnce(time.Now())
		}
	}
}

func (d *Driver) pushOnce(now time.Time) {
	snap := d.tele.Snapshot(now)
	if snap.Attitude == nil || snap.Attitude.Stale {
		d.skipped++
		return
	}
	a := snap.Attitude.Data
	// Heading: CRSF doesn't carry heading directly; yaw is the
	// nearest analogue. Yaw is reported in degrees [-180, +180] by
	// the CRSF attitude frame; convert to compass heading [0, 360).
	hdg := int(a.YawDeg)
	for hdg < 0 {
		hdg += 360
	}
	for hdg >= 360 {
		hdg -= 360
	}
	line := fmt.Sprintf("SET glcd attitude %.1f %.1f %d", a.PitchDeg, a.RollDeg, hdg)
	if err := d.hub.Send(line); err != nil {
		// Iohub already logs transport errors. We don't escalate;
		// next tick we try again.
		d.skipped++
		return
	}
	d.pushed++
}

// Pushed returns the number of successful attitude pushes since
// driver start. Useful for tests and the /api/v1/state endpoint if
// we ever want to surface driver health.
func (d *Driver) Pushed() uint64 { return d.pushed }

// Skipped returns the number of push intervals where no fresh
// attitude was available, or the iohub Send failed.
func (d *Driver) Skipped() uint64 { return d.skipped }
