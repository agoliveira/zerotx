// Package logbuf is a thread-safe ring buffer for daemon log lines.
// It implements io.Writer so it can be wired into Go's standard log
// package: log.SetOutput(io.MultiWriter(os.Stderr, buf)) preserves
// terminal output while capturing everything in memory for the API.
//
// Capacity is fixed at construction time. When full, oldest entries
// are overwritten. Entries are timestamped at write time, regardless
// of any timestamp prefix the logger may have added.
package logbuf

import (
	"io"
	"strings"
	"sync"
	"time"
)

// Entry is one captured log line.
type Entry struct {
	Time time.Time // when the line was written to the buffer
	Msg  string    // line contents, trailing newline stripped
}

// Buffer is a fixed-capacity ring buffer.
type Buffer struct {
	mu      sync.RWMutex
	entries []Entry
	head    int  // next slot to write
	full    bool // true once we've wrapped at least once
	cap     int
}

// New returns a buffer with the given capacity. cap <= 0 defaults to 1000.
func New(cap int) *Buffer {
	if cap <= 0 {
		cap = 1000
	}
	return &Buffer{
		entries: make([]Entry, cap),
		cap:     cap,
	}
}

// Write implements io.Writer. Each call is treated as one log entry,
// timestamped at the moment of the call. Trailing newlines are stripped
// because they're framing, not content.
func (b *Buffer) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	b.mu.Lock()
	b.entries[b.head] = Entry{Time: time.Now(), Msg: msg}
	b.head = (b.head + 1) % b.cap
	if b.head == 0 {
		b.full = true
	}
	b.mu.Unlock()
	return len(p), nil
}

// Snapshot returns entries with Time strictly after `since`. Pass zero
// time to get everything currently in the buffer. Results are in
// chronological order (oldest first).
func (b *Buffer) Snapshot(since time.Time) []Entry {
	b.mu.RLock()
	defer b.mu.RUnlock()

	n := b.cap
	start := 0
	if !b.full {
		n = b.head
	} else {
		start = b.head
	}

	out := make([]Entry, 0, n)
	for i := 0; i < n; i++ {
		idx := (start + i) % b.cap
		e := b.entries[idx]
		if e.Time.After(since) {
			out = append(out, e)
		}
	}
	return out
}

// TeeWriter returns an io.Writer that writes to both the buffer and w.
// Useful for log.SetOutput(buf.TeeWriter(os.Stderr)).
func (b *Buffer) TeeWriter(w io.Writer) io.Writer {
	return io.MultiWriter(b, w)
}
