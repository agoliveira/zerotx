package vfd

// Firehose subscribes to the daemon's log buffer and pushes new
// lines onto the VFD as they arrive. Two-row terminal-style scroll:
// new lines arrive on row 2 (bottom), the previous row 2 moves up
// to row 1 (top), and the previous row 1 falls off.
//
// Formatting strips the timestamp prefix ("YYYY/MM/DD HH:MM:SS.fff
// ") that Go's standard logger prepends, then truncates to the
// VFD width so each event fits in 20 characters. The result is
// the start of the message which is usually the most informative
// part ("ipc: handshake OK", "crsftee: client conn", "fc-ready: ").
//
// The firehose polls the buffer at PollHz (5Hz default). Polling
// is cheap (logbuf.Snapshot is in-memory, RWLocked) and at 5Hz
// the operator perceives smooth scroll without overwhelming the
// VFD's redraw rate. Bursts of log lines within one poll are
// drained in order, each becoming a tick on the scroll.

import (
	"context"
	"strings"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/logbuf"
)

// PollHz controls how often Firehose checks the logbuf for new
// entries. Higher values are smoother for fast bursts; lower
// values save CPU. 5Hz is a comfortable default.
const PollHz = 5

// MinPerLineMs is the minimum time a line stays on the bottom row
// before being scrolled up by the next one. Ensures the operator
// can read each line even when many arrive in a single poll.
const MinPerLineMs = 200

// Firehose drives a Driver from a logbuf source. Stops when ctx
// is cancelled.
type Firehose struct {
	driver Driver
	buf    *logbuf.Buffer

	// Display state.
	row1 string // top (older)
	row2 string // bottom (newer)
}

// NewFirehose constructs a Firehose backed by the given driver and
// log source.
func NewFirehose(driver Driver, buf *logbuf.Buffer) *Firehose {
	return &Firehose{
		driver: driver,
		buf:    buf,
	}
}

// Run blocks until ctx is cancelled. Polls the logbuf at PollHz,
// scrolls each new entry onto the display.
func (f *Firehose) Run(ctx context.Context) error {
	if f.driver == nil || f.buf == nil {
		return nil
	}
	since := time.Now()
	tick := time.NewTicker(time.Second / PollHz)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-tick.C:
			entries := f.buf.Snapshot(since)
			if len(entries) == 0 {
				continue
			}
			since = entries[len(entries)-1].Time
			for _, e := range entries {
				line := FormatLine(e.Msg)
				if line == "" {
					continue
				}
				f.scroll(line)
				_ = f.driver.WriteLines(f.row1, f.row2)
				// If many entries arrive in one tick, give the
				// operator time to read each one before the
				// next overrides the bottom row.
				if len(entries) > 1 {
					time.Sleep(MinPerLineMs * time.Millisecond)
				}
			}
			_ = now
		}
	}
}

// scroll moves the bottom row up to the top and places the new
// line on the bottom. Terminal-style.
func (f *Firehose) scroll(line string) {
	f.row1 = f.row2
	f.row2 = line
}

// FormatLine takes a raw log message, strips Go's standard
// timestamp prefix, and truncates the remainder to Width chars.
//
// Returns "" for entries the firehose should NOT scroll:
//   - empty / whitespace-only input
//   - LogDriver output ("[vfd] ...") to break the obvious feedback
//     loop where LogDriver writes to log → logbuf → firehose →
//     LogDriver
//
// Examples:
//
//	"2026/05/02 15:50:25.481 crsftee: listening on 127.0.0.1..."
//	  -> "crsftee: listening o"
//
//	"2026/05/02 15:51:43.414 fc-ready: mode=\"ANGL\" ready=true"
//	  -> "fc-ready: mode=\"ANGL"
func FormatLine(msg string) string {
	s := stripTimestamp(msg)
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "[vfd]") {
		return ""
	}
	if len(s) > Width {
		s = s[:Width]
	}
	return s
}

// stripTimestamp removes Go's default log timestamp prefix:
//
//	"2026/05/02 15:50:25.481234 ..."  -> "..."
//	"2026/05/02 15:50:25 ..."         -> "..."
//
// Leaves the rest unchanged. If no timestamp prefix is detected,
// returns the input verbatim.
func stripTimestamp(msg string) string {
	// Minimum length for "YYYY/MM/DD HH:MM:SS " is 20 chars.
	if len(msg) < 20 {
		return msg
	}
	// Cheap shape check: digits at positions 0..3 (year), '/' at
	// 4, 7; ' ' at 10; ':' at 13, 16. We don't validate ranges,
	// just structure.
	if msg[4] != '/' || msg[7] != '/' || msg[10] != ' ' ||
		msg[13] != ':' || msg[16] != ':' {
		return msg
	}
	// Skip past seconds (position 19), then optional fractional
	// part starting with '.'.
	i := 19
	if i < len(msg) && msg[i] == '.' {
		i++
		for i < len(msg) && msg[i] >= '0' && msg[i] <= '9' {
			i++
		}
	}
	// Skip the trailing space(s) between timestamp and message.
	for i < len(msg) && msg[i] == ' ' {
		i++
	}
	return msg[i:]
}
