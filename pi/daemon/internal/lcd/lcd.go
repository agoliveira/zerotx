// Package lcd is the daemon-side helper for the I2C-attached HD44780
// character LCD on the Mega IO board (firmware/io subsystem "lcd").
// Targets the LCM2002 (20x2) by default; SetGeom reconfigures for
// other geometries (16x2, 20x4, etc.).
//
// The LCD has no animation engine in firmware: it's a flat character
// display. Daemon code addresses it directly by row-and-text. There
// is no semantic event translation here; if you want behavior tied
// to daemon state, drive the LCD from the consumer's own logic.
//
// Three implementations match the iohub.Client constructor pattern:
//
//   - HubDriver: real hardware via a shared iohub.Client.
//   - LogDriver: writes commands to the daemon log; useful for
//     development and CI where no Mega is connected.
//   - NullDriver: no-op. Use when no LCD is configured.
//
// Single instance for now (lcd.0). The firmware's I2cLcd subsystem
// could be extended to multi-instance later; the package surface
// stays the same with an instance argument added at that point.
package lcd

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/agoliveira/zerotx/pi/daemon/internal/iohub"
)

// CursorMode names the firmware's cursor modes.
type CursorMode string

const (
	CursorOff   CursorMode = "off"
	CursorOn    CursorMode = "on"
	CursorBlink CursorMode = "blink"
)

// Driver is the interface the daemon uses to drive the LCD.
// All methods must be safe for concurrent use.
type Driver interface {
	// WriteLine writes text to the given row. Text longer than the
	// LCD's column count is truncated; shorter text is padded with
	// spaces so leftover characters from previous longer lines are
	// erased.
	WriteLine(row int, text string) error

	// Clear blanks the display.
	Clear() error

	// Backlight switches the backlight on or off.
	Backlight(on bool) error

	// Cursor selects the cursor display mode (off, on, blink).
	Cursor(mode CursorMode) error

	// SetGeom reconfigures the LCD for a non-default size. cols
	// must be 8..32, rows must be 1..4. The firmware re-runs init
	// on the underlying hd44780 driver.
	SetGeom(cols, rows int) error

	// SetAddr forces the I2C address. Use to disambiguate when two
	// PCF8574 backpacks share the bus. addr 0 means "auto-detect"
	// (the firmware default at boot).
	SetAddr(addr int) error

	// Close releases any underlying resource.
	Close() error
}

// New returns a Driver for the given address. Special values:
//
//   - "" (empty)  -> NullDriver
//   - "log"       -> LogDriver writing to the daemon log
//
// Anything else is a serial device path; the driver constructs a
// private iohub.Client. Close on a New-constructed driver also
// closes its private hub. For shared-hub use, prefer NewWithHub.
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

// NewWithHub returns a Driver bound to the given iohub.Client. The
// client lifecycle is managed by the caller; Close on the returned
// driver is a no-op.
func NewWithHub(hub iohub.Client) Driver {
	return &HubDriver{hub: hub, ownsHub: false}
}

// === NullDriver ===

type NullDriver struct{}

func (*NullDriver) WriteLine(int, string) error  { return nil }
func (*NullDriver) Clear() error                 { return nil }
func (*NullDriver) Backlight(bool) error         { return nil }
func (*NullDriver) Cursor(CursorMode) error      { return nil }
func (*NullDriver) SetGeom(int, int) error       { return nil }
func (*NullDriver) SetAddr(int) error            { return nil }
func (*NullDriver) Close() error                 { return nil }

// === LogDriver ===

// LogDriver writes commands to the daemon log instead of a wire.
// Identical writes are deduped to avoid spamming the log when the
// daemon refreshes the same content repeatedly.
type LogDriver struct {
	logf func(format string, args ...interface{})

	mu       sync.Mutex
	lastLine [4]string // up to 4 rows of dedup
}

func (d *LogDriver) WriteLine(row int, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if row >= 0 && row < len(d.lastLine) && d.lastLine[row] == text {
		return nil
	}
	if row >= 0 && row < len(d.lastLine) {
		d.lastLine[row] = text
	}
	d.logf("[lcd] line %d %q", row, text)
	return nil
}

func (d *LogDriver) Clear() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.lastLine {
		d.lastLine[i] = ""
	}
	d.logf("[lcd] clear")
	return nil
}

func (d *LogDriver) Backlight(on bool) error {
	v := 0
	if on {
		v = 1
	}
	d.logf("[lcd] backlight %d", v)
	return nil
}

func (d *LogDriver) Cursor(mode CursorMode) error {
	d.logf("[lcd] cursor %s", string(mode))
	return nil
}

func (d *LogDriver) SetGeom(cols, rows int) error {
	d.logf("[lcd] geom %d %d", cols, rows)
	return nil
}

func (d *LogDriver) SetAddr(addr int) error {
	d.logf("[lcd] addr 0x%02X", addr)
	return nil
}

func (d *LogDriver) Close() error { return nil }

// === HubDriver ===

// HubDriver speaks the firmware's lcd.0 protocol via an iohub.Client.
type HubDriver struct {
	hub     iohub.Client
	ownsHub bool
}

const target = "lcd.0"

func (d *HubDriver) WriteLine(row int, text string) error {
	// Firmware truncates and pads on its side (driven by SET geom),
	// so we don't need to re-implement that here. We do guard
	// against newline injection that would break the line protocol.
	clean := strings.ReplaceAll(text, "\n", " ")
	clean = strings.ReplaceAll(clean, "\r", " ")
	return d.hub.Send(fmt.Sprintf("SET %s line %d %s", target, row, clean))
}

func (d *HubDriver) Clear() error {
	return d.hub.Send("SET " + target + " clear")
}

func (d *HubDriver) Backlight(on bool) error {
	v := 0
	if on {
		v = 1
	}
	return d.hub.Send(fmt.Sprintf("SET %s backlight %d", target, v))
}

func (d *HubDriver) Cursor(mode CursorMode) error {
	switch mode {
	case CursorOff, CursorOn, CursorBlink:
		return d.hub.Send(fmt.Sprintf("SET %s cursor %s", target, string(mode)))
	default:
		return fmt.Errorf("lcd: invalid cursor mode %q", string(mode))
	}
}

func (d *HubDriver) SetGeom(cols, rows int) error {
	if cols < 8 || cols > 32 || rows < 1 || rows > 4 {
		return fmt.Errorf("lcd: geom out of range cols=%d rows=%d", cols, rows)
	}
	return d.hub.Send(fmt.Sprintf("SET %s geom %d %d", target, cols, rows))
}

func (d *HubDriver) SetAddr(addr int) error {
	if addr < 0x08 || addr > 0x77 {
		return fmt.Errorf("lcd: addr out of range 0x%02X", addr)
	}
	return d.hub.Send(fmt.Sprintf("SET %s addr 0x%02X", target, addr))
}

func (d *HubDriver) Close() error {
	if d.ownsHub && d.hub != nil {
		return d.hub.Close()
	}
	return nil
}
