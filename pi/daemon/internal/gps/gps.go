package gps

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"go.bug.st/serial"
)

// Fix names the type of GPS fix. FixNone means no satellite lock; the
// position fields in State should not be trusted.
type Fix int

const (
	FixNone Fix = iota
	Fix2D
	Fix3D
)

// String renders Fix for logging.
func (f Fix) String() string {
	switch f {
	case Fix2D:
		return "2D"
	case Fix3D:
		return "3D"
	}
	return "none"
}

// State is a snapshot of the most recent GPS data. Zero values mean
// "not yet observed". Updated tracks the last time any field changed,
// which is the right metric for "is the GPS still alive?" callers.
type State struct {
	Time       time.Time // UTC, from RMC
	Fix        Fix       // FixNone / Fix2D / Fix3D, from GGA + sat heuristic
	Sats       int       // satellites in use, from GGA
	HDOP       float64   // horizontal dilution of precision, from GGA
	LatDeg     float64   // signed decimal degrees
	LonDeg     float64   // signed decimal degrees
	AltMeters  float64   // above mean sea level, from GGA
	SpeedKmh   float64   // ground speed, converted from RMC knots
	HeadingDeg float64   // course over ground, true degrees, from RMC
	Updated    time.Time // wall-clock when any field above last changed
}

// Reader continuously parses NMEA from an io.ReadCloser source. It is
// safe to call Get from any goroutine; Start/Close from one.
type Reader struct {
	src    io.ReadCloser
	state  atomic.Pointer[State]
	stop   chan struct{}
	wg     sync.WaitGroup
	closed atomic.Bool

	// errLogThrottle prevents flooding the log with the same parse
	// error from a stuck-bytes producer. We log at most one error per
	// errLogInterval.
	errLogMu       sync.Mutex
	lastErrLogged  time.Time
	errLogInterval time.Duration
}

// Open opens the named serial device at the given baud rate (standard
// values: 4800, 9600, 38400, 115200; consumer GPS modules ship at
// 9600 by default). Returns nil and an error if the device cannot be
// opened. Caller is expected to log and continue.
func Open(devPath string, baud int) (*Reader, error) {
	if devPath == "" {
		return nil, errors.New("gps: empty device path")
	}
	port, err := serial.Open(devPath, &serial.Mode{
		BaudRate: baud,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	})
	if err != nil {
		return nil, fmt.Errorf("gps: open %s @%d: %w", devPath, baud, err)
	}
	return New(port), nil
}

// New wraps an io.ReadCloser as a Reader. Used by Open and by tests.
// The returned Reader has a zero State; call Start to begin parsing.
func New(src io.ReadCloser) *Reader {
	r := &Reader{
		src:            src,
		stop:           make(chan struct{}),
		errLogInterval: 60 * time.Second,
	}
	r.state.Store(&State{})
	return r
}

// Start launches the parser goroutine. Returns an error only if the
// Reader has already been Closed.
func (r *Reader) Start(ctx context.Context) error {
	if r.closed.Load() {
		return errors.New("gps: reader closed")
	}
	r.wg.Add(1)
	go r.run(ctx)
	return nil
}

// Get returns a copy of the most recent State. Safe for concurrent use.
func (r *Reader) Get() State {
	return *r.state.Load()
}

// Close stops the parser goroutine and closes the source. Idempotent.
func (r *Reader) Close() error {
	if !r.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(r.stop)
	// Closing the source will unblock the goroutine's blocking read.
	err := r.src.Close()
	r.wg.Wait()
	return err
}

// run is the parser goroutine. Reads lines, parses, updates state.
// Returns on Close, ctx.Done, or unrecoverable read error.
func (r *Reader) run(ctx context.Context) {
	defer r.wg.Done()

	scanner := bufio.NewScanner(r.src)
	// NMEA sentences are tiny (max 82 chars) but allow some slack
	// for runs of garbage between sentences.
	scanner.Buffer(make([]byte, 0, 256), 1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		default:
		}
		line := scanner.Text()
		if line == "" {
			continue
		}
		sent, err := ParseSentence(line)
		if err != nil {
			r.maybeLog("parse: %v (line=%q)", err, line)
			continue
		}
		// Atomically swap in a new State only if the sentence
		// produced a change. We work on a copy to avoid sharing the
		// same object across readers and writers.
		cur := r.state.Load()
		next := *cur
		if applyToState(&next, sent, time.Now()) {
			r.state.Store(&next)
		}
	}
	if err := scanner.Err(); err != nil {
		// scanner.Err is nil on EOF and !nil on real errors. We may
		// also get here when Close() closes the source out from
		// under us; that surfaces as io.EOF or "use of closed".
		if !r.closed.Load() {
			log.Printf("gps: read error: %v", err)
		}
	}
}

// maybeLog rate-limits parse error logging.
func (r *Reader) maybeLog(format string, args ...any) {
	r.errLogMu.Lock()
	defer r.errLogMu.Unlock()
	if time.Since(r.lastErrLogged) < r.errLogInterval {
		return
	}
	r.lastErrLogged = time.Now()
	log.Printf("gps: "+format, args...)
}
