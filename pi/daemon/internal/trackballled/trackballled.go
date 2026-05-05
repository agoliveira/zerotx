// Package trackballled drives the bicolor trackball ring on the
// Mega IO board (firmware/io subsystem "led.trackball") based on
// the daemon's operational state.
//
// Mapping from daemon state to canonical LED state:
//
//   off          - driver not running yet, or shutdown
//   green-solid  - disarmed, no active alerts
//   green-pulse  - armed (any sub-state including ARMING_REQUESTED),
//                  no active alerts
//   red-solid    - any active wxalert at warning severity
//   red-blink    - any active wxalert at critical severity
//
// The driver polls at 1Hz, computes the desired state, and only
// pushes a SET led.trackball command to the iohub when the state
// changes. No hard real-time requirement; LED transitions at human
// scale are perfectly acceptable.
//
// Future extensions (not in v1): link-loss/failsafe -> red-blink,
// FC-not-ready warnings, audio-active criticals, etc. The mapping
// table is centralized in computeState; adding new inputs is
// straightforward.
package trackballled

import (
	"context"
	"log"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/arm"
	"github.com/agoliveira/zerotx/pi/daemon/internal/iohub"
	"github.com/agoliveira/zerotx/pi/daemon/internal/wxalert"
)

// AlertProvider is the read-only view of weather alerts the driver
// needs. Implemented in production by the daemon's wxAlertHolder;
// unit tests can substitute a fake.
type AlertProvider interface {
	Snapshot() []wxalert.Alert
}

// Driver derives a canonical trackball LED state from the daemon's
// operational state and pushes commands to the iohub when state
// changes.
type Driver struct {
	hub    iohub.Client
	arm    *arm.Machine
	alerts AlertProvider

	pollInterval time.Duration
	current      string
}

// New constructs a Driver. Default poll interval is 1 second.
// hub may be any iohub.Client (Null, Log, Serial); the driver tolerates
// failed sends silently because the LED is presentational.
func New(hub iohub.Client, m *arm.Machine, alerts AlertProvider) *Driver {
	return &Driver{
		hub:          hub,
		arm:          m,
		alerts:       alerts,
		pollInterval: time.Second,
		current:      "", // forces first send
	}
}

// SetPollInterval overrides the default. Use only in tests.
func (d *Driver) SetPollInterval(dt time.Duration) {
	if dt > 0 {
		d.pollInterval = dt
	}
}

// Run polls until ctx is done. On exit it sends "off" to leave the
// LED in a clean visual state. Run blocks; the caller should
// goroutine it.
func (d *Driver) Run(ctx context.Context) {
	// Ensure clean state at startup; explicit "off" so we don't
	// inherit whatever the firmware was showing previously.
	d.send("off")

	t := time.NewTicker(d.pollInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			d.send("off")
			return
		case <-t.C:
			d.tick()
		}
	}
}

func (d *Driver) tick() {
	state := d.computeState()
	if state == d.current {
		return
	}
	if err := d.send(state); err != nil {
		// Sending failed - log once at this transition, but DON'T
		// update d.current so the next tick will retry. iohub
		// already throttles open-error logs internally.
		log.Printf("trackballled: send %q: %v", state, err)
		return
	}
	d.current = state
}

// computeState evaluates the policy. Pure function over the inputs
// the driver has access to. Tests pump synthetic states through
// here directly.
func (d *Driver) computeState() string {
	// Alerts dominate over arm state because they signal "operator
	// should look at the situation."
	if d.alerts != nil {
		hasWarning := false
		for _, a := range d.alerts.Snapshot() {
			if a.Severity == wxalert.SeverityCritical {
				return "red-blink"
			}
			if a.Severity == wxalert.SeverityWarning {
				hasWarning = true
			}
		}
		if hasWarning {
			return "red-solid"
		}
	}
	// No alerts: green, with pulse meaning armed (or about to be).
	if d.arm != nil {
		s := d.arm.Snapshot()
		if s.State == arm.StateArmed || s.State == arm.StateArmingRequested {
			return "green-pulse"
		}
	}
	return "green-solid"
}

func (d *Driver) send(state string) error {
	return d.hub.Send("SET led.trackball " + state)
}
