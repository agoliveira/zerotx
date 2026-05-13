// Package vfd drives the ZeroTX cool-glow diagnostic display: a
// 2x20 character VFD (Noritake CU20025ECPB-W1J) reached over USB-CDC.
//
// The VFD is driven by the Mega 2560 IO board (zerotx-io firmware),
// addressed via the structured protocol:
//   SET vfd.0 line <row> <text...>
//   SET vfd.0 clear
//   SET vfd.0 brightness <n>
//   SET vfd.0 tick [<n>]
//   SET vfd.0 arm <0|1>
//   SET vfd.0 fmmode <text>
//   SET vfd.0 lq <pct>
//   SET vfd.0 batt <text>
//   SET vfd.0 alarm <warn|critical|failsafe>
//   SET vfd.0 disarmed
//
// The VFD is purely aesthetic: it shows live daemon activity
// (IPC frames, CRSF telemetry, TTS events, API hits, boot init)
// scrolling terminal-style for the pure cool-glow-nerd factor.
// It is NOT a status surface; the HUD covers operational state.
//
// The package exposes a small Driver interface (WriteLines, Clear,
// Brightness, Event) with three implementations:
//
//   - HubDriver: real hardware. Sends VFD-specific command lines via
//     a shared iohub.Client (see internal/iohub) so the same Mega
//     connection can serve other subsystems too.
//   - LogDriver: writes to the daemon log so the firehose is
//     observable without hardware. Useful for development.
//   - NullDriver: no-op. Active when the -iohub-port flag is empty.
//
// The Firehose subscribes to the daemon's logbuf and pushes new
// lines onto the VFD at a configurable rate, formatting each line
// to fit the 20-column width. Row 1 = older line, Row 2 = newest
// (terminal-style: new lines arrive at the bottom, older lines
// scroll up and off the top).
package vfd

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/agoliveira/zerotx/pi/daemon/internal/iohub"
)

// Width is the VFD's character columns.
const Width = 20

// Rows is the VFD's character rows.
const Rows = 2

// Driver is the small interface the firehose uses to push content
// to the display. Implementations are responsible for any wire-
// protocol framing or hardware state management.
//
// All methods must be safe for concurrent use; the firehose may
// call them from multiple goroutines (it currently doesn't, but
// the contract leaves room).
type Driver interface {
	// WriteLines updates the visible content. row1 is the top line
	// (older), row2 is the bottom (newer). Strings are truncated
	// or padded to Width by the implementation.
	WriteLines(row1, row2 string) error

	// Clear blanks the display.
	Clear() error

	// Brightness sets the VFD's intensity. 0..3 (4 levels native
	// to this Noritake module). Out-of-range values are clamped.
	Brightness(level int) error

	// Event signals a semantic event to the firmware. Drives the
	// animation state machine (firmware-side) without the daemon
	// caring about glyphs or frames. kind is a short token (tick,
	// arm, mode, lq, batt, warn, critical, failsafe, disarmed)
	// optionally followed by args. The firmware tolerates unknown
	// kinds gracefully so daemon and firmware versions can drift.
	//
	// Example: drv.Event("tick", "12") -> writes "E tick 12\n".
	// Example: drv.Event("arm", "1")
	// Example: drv.Event("mode", "ANGL")
	Event(kind string, args ...string) error

	// Close releases any underlying resource.
	Close() error
}

// New returns a Driver for the given address. Special values:
//
//   - "" (empty)  -> NullDriver: every call is a no-op. Use when the
//     -iohub-port flag is empty (no display attached).
//   - "log"       -> LogDriver: writes to the daemon log so the
//     firehose can be observed without hardware.
//
// Anything else is treated as a serial device path (e.g.
// /dev/ttyACM2). The serial connection is opened lazily on first
// use; New itself never blocks.
//
// New constructs a private iohub.Client owned by the returned
// driver. The daemon's main wiring should use NewWithHub instead so
// the Mega connection is shared with other subsystems (indicator
// LED, indicator LEDs, etc.). Close on a New-constructed driver
// also closes its private hub.
func New(addr string) Driver {
	switch addr {
	case "":
		return &NullDriver{}
	case "log":
		return &LogDriver{logf: log.Printf}
	default:
		hub := iohub.New(addr)
		return &HubDriver{hub: hub, ownsHub: true}
	}
}

// padOrTruncate ensures s is exactly Width characters. Truncation
// is right-side; padding is right-side spaces.
func padOrTruncate(s string) string {
	if len(s) > Width {
		return s[:Width]
	}
	if len(s) < Width {
		return s + strings.Repeat(" ", Width-len(s))
	}
	return s
}

// === NullDriver: VFD disabled ===

type NullDriver struct{}

func (d *NullDriver) WriteLines(_, _ string) error { return nil }
func (d *NullDriver) Clear() error                 { return nil }
func (d *NullDriver) Brightness(_ int) error       { return nil }
func (d *NullDriver) Event(_ string, _ ...string) error { return nil }
func (d *NullDriver) Close() error                 { return nil }

// === LogDriver: scaffolding without hardware ===

// LogDriver writes the lines that would have gone to the VFD to
// the daemon log instead. Lets us validate the firehose end-to-end
// before hardware is wired. Safe for concurrent use.
type LogDriver struct {
	mu   sync.Mutex
	logf func(format string, args ...interface{})
	last [Rows]string
}

func (d *LogDriver) WriteLines(row1, row2 string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	r1 := padOrTruncate(row1)
	r2 := padOrTruncate(row2)
	if r1 == d.last[0] && r2 == d.last[1] {
		return nil
	}
	d.last[0] = r1
	d.last[1] = r2
	d.logf("[vfd] |%s|", r1)
	d.logf("[vfd] |%s|", r2)
	return nil
}

func (d *LogDriver) Clear() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.last[0] = ""
	d.last[1] = ""
	d.logf("[vfd] (clear)")
	return nil
}

func (d *LogDriver) Brightness(level int) error {
	if level < 0 {
		level = 0
	}
	if level > 3 {
		level = 3
	}
	d.logf("[vfd] brightness=%d", level)
	return nil
}

func (d *LogDriver) Close() error { return nil }

func (d *LogDriver) Event(kind string, args ...string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(args) == 0 {
		d.logf("[vfd] E %s", kind)
	} else {
		d.logf("[vfd] E %s %s", kind, strings.Join(args, " "))
	}
	return nil
}

// === HubDriver: real hardware via iohub.Client ===

// HubDriver speaks the structured VFD protocol via a shared
// iohub.Client. The client owns the serial port; this driver just
// formats VFD-specific command lines and passes them to Send.
//
// Two ways to construct: New(addr) creates a private iohub.Client
// for backward compatibility (callers that don't know about iohub
// can keep using the old API). NewWithHub(client) takes a shared
// client so the same Mega connection can be used for VFD and other
// subsystems (indicator LEDs, etc).
//
// The Mega firmware exposes two VFD instances (vfd.0 and vfd.1).
// NewWithHub selects vfd.0 by default; NewInstanceWithHub addresses
// either explicitly.
type HubDriver struct {
	hub      iohub.Client
	instance uint8

	// ownsHub is true when this driver constructed its own hub (via
	// New). Close on such drivers also closes the hub. When the hub
	// is shared (via NewWithHub), Close is a no-op so the shared
	// hub stays alive for other consumers.
	ownsHub bool
}

// NewWithHub returns a Driver that sends commands to vfd.0 via the
// given iohub.Client. The client lifecycle is managed by the caller;
// the returned driver's Close is a no-op.
func NewWithHub(hub iohub.Client) Driver {
	return NewInstanceWithHub(hub, 0)
}

// NewInstanceWithHub returns a Driver targeting vfd.<instance>.
// Instance must be 0 or 1 (the firmware's kInstanceCount); higher
// values are accepted at construction but Send will produce errors
// from the firmware ("unknown-target") at runtime.
func NewInstanceWithHub(hub iohub.Client, instance uint8) Driver {
	return &HubDriver{hub: hub, instance: instance, ownsHub: false}
}

// target formats the protocol target token for this driver's
// instance, e.g. "vfd.0" or "vfd.1".
func (d *HubDriver) target() string {
	return fmt.Sprintf("vfd.%d", d.instance)
}

func (d *HubDriver) WriteLines(row1, row2 string) error {
	r1 := padOrTruncate(row1)
	r2 := padOrTruncate(row2)
	t := d.target()
	if err := d.hub.Send("SET " + t + " line 0 " + r1); err != nil {
		return err
	}
	return d.hub.Send("SET " + t + " line 1 " + r2)
}

func (d *HubDriver) Clear() error {
	return d.hub.Send("SET " + d.target() + " clear")
}

func (d *HubDriver) Brightness(level int) error {
	if level < 0 {
		level = 0
	}
	if level > 3 {
		level = 3
	}
	return d.hub.Send(fmt.Sprintf("SET %s brightness %d", d.target(), level))
}

func (d *HubDriver) Close() error {
	if d.ownsHub && d.hub != nil {
		return d.hub.Close()
	}
	return nil
}

// Event translates the legacy semantic-event vocabulary into the
// Mega firmware's structured commands. The Driver interface still
// accepts strings for the kind so call sites don't need updating
// when the wire protocol changes; the translation lives here.
//
// Recognized kinds:
//   tick, arm, fmmode (or "mode" for backward compat), lq, batt
//   warn, critical, failsafe -> alarm subcommand
//   disarmed
//
// Unknown kinds are dropped silently with a debug log; the firmware
// would reject them anyway, no need to flood logs in steady state.
func (d *HubDriver) Event(kind string, args ...string) error {
	t := d.target()
	var cmd string
	switch kind {
	case "tick", "arm", "lq", "batt":
		// Direct param mapping: SET vfd.<n> <kind> <args...>
		cmd = "SET " + t + " " + kind
		if len(args) > 0 {
			cmd += " " + strings.Join(args, " ")
		}
	case "mode", "fmmode":
		// Old name was "mode"; firmware exposes "fmmode" since "mode"
		// is reserved for display-mode SET. Translate.
		cmd = "SET " + t + " fmmode"
		if len(args) > 0 {
			cmd += " " + strings.Join(args, " ")
		}
	case "warn", "critical", "failsafe":
		// Alarm subcommand on the firmware.
		cmd = "SET " + t + " alarm " + kind
	case "disarmed":
		cmd = "SET " + t + " disarmed"
	default:
		// Drop unknown silently; firmware would error and we don't
		// want event drops to flood the daemon log.
		return nil
	}
	return d.hub.Send(cmd)
}

// Sprint formats two rows for diagnostic dumping. Used by tests
// and ad-hoc debugging; not for the wire.
func Sprint(row1, row2 string) string {
	return fmt.Sprintf("|%s|\n|%s|", padOrTruncate(row1), padOrTruncate(row2))
}
