// Package vfd drives the ZeroTX cool-glow diagnostic display: a
// 2x20 character VFD (Noritake CU20025ECPB-W1J) on a dedicated
// Pro Micro 5V/16MHz, reached over USB-CDC.
//
// The VFD is purely aesthetic: it shows live daemon activity
// (IPC frames, CRSF telemetry, TTS events, API hits, boot init)
// scrolling terminal-style for the pure cool-glow-nerd factor.
// It is NOT a status surface; the HUD covers operational state.
//
// The package exposes a small Driver interface (WriteLines, Clear,
// Brightness) with three implementations:
//
//   - SerialDriver: real hardware. Opens the configured serial
//     port and pushes a line-based ASCII protocol the Pro Micro
//     firmware parses.
//   - LogDriver: writes to the daemon log so the firehose is
//     observable without hardware. Useful for development.
//   - NullDriver: no-op. Active when the -vfd-port flag is empty.
//
// The Firehose subscribes to the daemon's logbuf and pushes new
// lines onto the VFD at a configurable rate, formatting each line
// to fit the 20-column width. Row 1 = older line, Row 2 = newest
// (terminal-style: new lines arrive at the bottom, older lines
// scroll up and off the top).
package vfd

import (
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
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

	// Close releases any underlying resource.
	Close() error
}

// New returns a Driver for the given address. Special values:
//
//	""    -> NullDriver (VFD disabled, no-op)
//	"log" -> LogDriver (writes to the daemon log)
//
// Anything else is treated as a serial device path (e.g.
// /dev/ttyACM2). The serial connection is opened lazily on first
// use; New itself never blocks.
func New(addr string) Driver {
	switch addr {
	case "":
		return &NullDriver{}
	case "log":
		return &LogDriver{logf: log.Printf}
	default:
		return &SerialDriver{path: addr}
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
func (d *NullDriver) Close() error                 { return nil }

// === LogDriver: scaffolding without hardware ===

// LogDriver writes the lines that would have gone to the VFD to
// the daemon log instead. Lets us validate the firehose end-to-end
// before the Pro Micro is wired. Safe for concurrent use.
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

// === SerialDriver: real hardware (stub for now) ===

// SerialDriver pushes a line-based ASCII protocol over USB-CDC to
// the Pro Micro running the VFD firmware. Wire protocol is one
// command per newline-terminated line:
//
//	L row content...    -- write content (truncated to 20 chars)
//	C                   -- clear
//	B level             -- brightness 0..3
//
// Connection is opened lazily on first use. Open errors are
// suppressed (logged once) so a missing/unplugged display does
// not break the daemon; the driver retries on subsequent writes.
//
// Implementation completes when bench hardware lands. For now this
// is a stub that logs intent and returns success so the daemon
// can be configured with -vfd-port=/dev/ttyACMX without crashing.
type SerialDriver struct {
	path string
	mu   sync.Mutex
	port io.ReadWriteCloser
}

func (d *SerialDriver) WriteLines(row1, row2 string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	// TODO(bench): open d.path with go.bug.st/serial, write
	//   "L 0 <row1>\nL 1 <row2>\n", retry-on-error logic.
	_ = padOrTruncate(row1)
	_ = padOrTruncate(row2)
	return nil
}

func (d *SerialDriver) Clear() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	// TODO(bench): write "C\n"
	return nil
}

func (d *SerialDriver) Brightness(level int) error {
	if level < 0 {
		level = 0
	}
	if level > 3 {
		level = 3
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// TODO(bench): write "B <level>\n"
	return nil
}

func (d *SerialDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.port != nil {
		err := d.port.Close()
		d.port = nil
		return err
	}
	return nil
}

// Sprint formats two rows for diagnostic dumping. Used by tests
// and ad-hoc debugging; not for the wire.
func Sprint(row1, row2 string) string {
	return fmt.Sprintf("|%s|\n|%s|", padOrTruncate(row1), padOrTruncate(row2))
}
