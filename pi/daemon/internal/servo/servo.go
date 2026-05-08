// Package servo is the daemon-side helper for the four servo
// channels on the Mega IO board (firmware/io subsystem "servo").
// The firmware exposes servo.0 .. servo.3; this package addresses
// them through one Controller indexed by instance.
//
// Lazy attach matches the firmware semantics: the firmware only
// grabs a servo pin (and activates Timer 5) on first command. Send
// Detach to release the pin when a servo isn't needed for a while.
//
// Three implementations match the iohub.Client constructor pattern:
//
//   - HubController: real hardware via a shared iohub.Client.
//   - LogController: writes commands to the daemon log.
//   - NullController: no-op.
package servo

import (
	"fmt"
	"log"
	"sync"

	"github.com/agoliveira/zerotx/pi/daemon/internal/iohub"
)

// Count is the number of servo channels exposed by the firmware
// (firmware/io kInstanceCount in subsystems/servo.h).
const Count = 4

// AngleMin and AngleMax bound the range of Angle.
const (
	AngleMin = 0
	AngleMax = 180
)

// PulseMin and PulseMax bound the range of Microseconds. Matches
// the firmware's accepted range.
const (
	PulseMin = 500
	PulseMax = 2500
)

// Controller is the interface for driving any of the four servo
// channels. instance must be 0..Count-1; out-of-range instance
// values return an error without contacting the hub.
//
// All methods are safe for concurrent use.
type Controller interface {
	// Angle commands a standard servo angle (0..180 degrees).
	// Out-of-range values return an error; the firmware would
	// reject them anyway, but checking here avoids the round trip.
	Angle(instance uint8, deg int) error

	// Microseconds commands a raw pulse width (500..2500 us).
	// Useful for servos with non-standard travel ranges or for
	// fine-grained positioning beyond 1-degree increments.
	Microseconds(instance uint8, us int) error

	// Detach releases the servo's pin. The Servo library's pulse
	// generation stops; the servo's position becomes mechanical-
	// load-dependent. A subsequent Angle/Microseconds call re-
	// attaches.
	Detach(instance uint8) error

	// Close releases any underlying resource.
	Close() error
}

// New returns a Controller for the given address. Special values:
//
//   - "" (empty)  -> NullController
//   - "log"       -> LogController writing to the daemon log
//
// Anything else is a serial device path; the controller constructs
// a private iohub.Client. Close on a New-constructed controller
// also closes its private hub.
func New(addr string) Controller {
	switch addr {
	case "":
		return &NullController{}
	case "log":
		return &LogController{logf: log.Printf}
	default:
		hub := iohub.New(addr)
		return &HubController{hub: hub, ownsHub: true}
	}
}

// NewWithHub returns a Controller bound to the given iohub.Client.
// The client lifecycle is managed by the caller; Close on the
// returned controller is a no-op.
func NewWithHub(hub iohub.Client) Controller {
	return &HubController{hub: hub, ownsHub: false}
}

// validateInstance returns an error if instance is outside 0..Count-1.
func validateInstance(instance uint8) error {
	if instance >= Count {
		return fmt.Errorf("servo: instance %d out of range (0..%d)", instance, Count-1)
	}
	return nil
}

// === NullController ===

type NullController struct{}

func (*NullController) Angle(uint8, int) error        { return nil }
func (*NullController) Microseconds(uint8, int) error { return nil }
func (*NullController) Detach(uint8) error            { return nil }
func (*NullController) Close() error                  { return nil }

// === LogController ===

// LogController writes commands to the daemon log instead of a
// wire. Useful for development without hardware.
type LogController struct {
	logf func(format string, args ...interface{})

	mu       sync.Mutex
	lastUs   [Count]int
	attached [Count]bool
}

func (c *LogController) Angle(instance uint8, deg int) error {
	if err := validateInstance(instance); err != nil {
		return err
	}
	if deg < AngleMin || deg > AngleMax {
		return fmt.Errorf("servo: angle %d out of range (%d..%d)", deg, AngleMin, AngleMax)
	}
	c.logf("[servo.%d] angle %d", instance, deg)
	c.mu.Lock()
	c.attached[instance] = true
	c.mu.Unlock()
	return nil
}

func (c *LogController) Microseconds(instance uint8, us int) error {
	if err := validateInstance(instance); err != nil {
		return err
	}
	if us < PulseMin || us > PulseMax {
		return fmt.Errorf("servo: us %d out of range (%d..%d)", us, PulseMin, PulseMax)
	}
	c.logf("[servo.%d] us %d", instance, us)
	c.mu.Lock()
	c.attached[instance] = true
	c.lastUs[instance] = us
	c.mu.Unlock()
	return nil
}

func (c *LogController) Detach(instance uint8) error {
	if err := validateInstance(instance); err != nil {
		return err
	}
	c.logf("[servo.%d] detach", instance)
	c.mu.Lock()
	c.attached[instance] = false
	c.mu.Unlock()
	return nil
}

func (c *LogController) Close() error { return nil }

// === HubController ===

// HubController speaks the firmware's servo.<n> protocol via an
// iohub.Client.
type HubController struct {
	hub     iohub.Client
	ownsHub bool
}

func (c *HubController) Angle(instance uint8, deg int) error {
	if err := validateInstance(instance); err != nil {
		return err
	}
	if deg < AngleMin || deg > AngleMax {
		return fmt.Errorf("servo: angle %d out of range (%d..%d)", deg, AngleMin, AngleMax)
	}
	return c.hub.Send(fmt.Sprintf("SET servo.%d angle %d", instance, deg))
}

func (c *HubController) Microseconds(instance uint8, us int) error {
	if err := validateInstance(instance); err != nil {
		return err
	}
	if us < PulseMin || us > PulseMax {
		return fmt.Errorf("servo: us %d out of range (%d..%d)", us, PulseMin, PulseMax)
	}
	return c.hub.Send(fmt.Sprintf("SET servo.%d us %d", instance, us))
}

func (c *HubController) Detach(instance uint8) error {
	if err := validateInstance(instance); err != nil {
		return err
	}
	return c.hub.Send(fmt.Sprintf("SET servo.%d detach", instance))
}

func (c *HubController) Close() error {
	if c.ownsHub && c.hub != nil {
		return c.hub.Close()
	}
	return nil
}
