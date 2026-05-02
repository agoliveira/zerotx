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

	"go.bug.st/serial"
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

// === SerialDriver: real hardware ===

// SerialDriver pushes a line-based ASCII protocol over USB-CDC to
// the Pro Micro running the VFD firmware (firmware/vfd/). Wire
// protocol matches the firmware's processLine() command set:
//
//	L<row><sp><content>\n   write content (row=0 or 1).
//	C\n                     clear.
//	B<sp><level>\n          brightness 0..3 (0 = max).
//
// Connection is opened lazily on first use and re-opened on the
// next write after any I/O failure. A missing/unplugged display
// must NOT crash the daemon; the firehose continues to drive a
// device that simply doesn't exist yet, and recovers when the
// Pro Micro reappears.
type SerialDriver struct {
	path string

	mu     sync.Mutex
	port   io.WriteCloser
	logged bool // suppress repeated open-error logs
}

// open opens the serial port at d.path with reasonable USB-CDC
// defaults (115200 8N1, no flow control). Caller must hold d.mu.
// Returns nil on success; any error leaves d.port == nil so the
// next call retries.
func (d *SerialDriver) open() error {
	if d.port != nil {
		return nil
	}
	mode := &serial.Mode{
		BaudRate: 115200,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}
	p, err := serial.Open(d.path, mode)
	if err != nil {
		if !d.logged {
			log.Printf("vfd: open %s: %v (will retry on next write)", d.path, err)
			d.logged = true
		}
		return err
	}
	log.Printf("vfd: %s open at 115200", d.path)
	d.port = p
	d.logged = false
	return nil
}

// close drops the serial handle. Caller must hold d.mu.
func (d *SerialDriver) closeLocked() {
	if d.port != nil {
		_ = d.port.Close()
		d.port = nil
	}
}

// writeCmd writes one newline-terminated command line. Re-opens
// the port once on transient failure; the second failure surfaces
// as the returned error and the caller treats it as "device not
// available right now" without escalating.
func (d *SerialDriver) writeCmd(line string) error {
	if err := d.open(); err != nil {
		return err
	}
	if _, err := d.port.Write([]byte(line + "\n")); err != nil {
		d.closeLocked()
		// One retry: the most common failure is the Pro Micro
		// having been replugged since the last open.
		if openErr := d.open(); openErr != nil {
			return openErr
		}
		if _, retryErr := d.port.Write([]byte(line + "\n")); retryErr != nil {
			d.closeLocked()
			return retryErr
		}
	}
	return nil
}

func (d *SerialDriver) WriteLines(row1, row2 string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	r1 := padOrTruncate(row1)
	r2 := padOrTruncate(row2)
	if err := d.writeCmd("L0 " + r1); err != nil {
		return err
	}
	return d.writeCmd("L1 " + r2)
}

func (d *SerialDriver) Clear() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writeCmd("C")
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
	return d.writeCmd(fmt.Sprintf("B %d", level))
}

func (d *SerialDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closeLocked()
	return nil
}

// Sprint formats two rows for diagnostic dumping. Used by tests
// and ad-hoc debugging; not for the wire.
func Sprint(row1, row2 string) string {
	return fmt.Sprintf("|%s|\n|%s|", padOrTruncate(row1), padOrTruncate(row2))
}
