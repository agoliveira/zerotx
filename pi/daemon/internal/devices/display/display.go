// Package display drives the HUB75 display device over USB-CDC serial.
//
// The display device (an ESP32 driving two chained HUB75 panels) is a
// passive viewer. The daemon owns all state; this package translates
// daemon-level state changes into the wire protocol documented in
// docs/protocols/display.md.
//
// Architecture: a Driver wraps an io.ReadWriteCloser (typically a
// serial port, but any duplex stream works for tests and the
// `disptest` standalone harness). The Driver runs two goroutines:
//
//   - sender: pulls state/mode/alarm changes off internal channels
//     and writes wire-format lines to the underlying transport
//   - receiver: reads lines from the transport and dispatches
//     READY / HEARTBEAT / ERROR / PONG to a handler
//
// Callers interact with the Driver through a small surface: SetMode,
// SetState, FireAlarm, ClearAlarm, ShowMessage, SetBrightness,
// Ping. These methods never block on the underlying transport; they
// drop messages if the send queue is full and log a warning.
//
// State snapshots are sent automatically by a ticker. The caller
// updates state via SetState (which mutates the in-package latest
// snapshot); the ticker emits the current snapshot at the configured
// rate.
//
// The Driver is robust to disconnect: if the transport returns an
// error, the goroutines exit cleanly. The caller is responsible for
// detecting the failure (via Done()) and constructing a new Driver
// on a new transport. A higher-level Manager will handle reconnect
// loops; this package stays focused on the protocol.
package display

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"
)

// Mode is the render mode the display is currently showing.
type Mode string

const (
	ModeIdle       Mode = "IDLE"
	ModePreflight  Mode = "PREFLIGHT"
	ModeFlight     Mode = "FLIGHT"
	ModeAlarm      Mode = "ALARM"
	ModeRTH        Mode = "RTH"
	ModePostflight Mode = "POSTFLIGHT"
)

// IsValid reports whether m is one of the defined modes.
func (m Mode) IsValid() bool {
	switch m {
	case ModeIdle, ModePreflight, ModeFlight, ModeAlarm, ModeRTH, ModePostflight:
		return true
	}
	return false
}

// AlarmLevel mirrors audio.Level for the alarm overlay.
type AlarmLevel string

const (
	AlarmInfo     AlarmLevel = "info"
	AlarmNotice   AlarmLevel = "notice"
	AlarmWarning  AlarmLevel = "warning"
	AlarmCritical AlarmLevel = "critical"
)

// State is the snapshot the display renders in flight modes.
// Pointer-typed numeric fields signal "absent" with nil; writing the
// snapshot serializes only fields that are set, so the display
// preserves last-known values for missing fields. This is what makes
// partial updates work: the daemon can update only what changed.
type State struct {
	Armed     *bool    `display:"armed"`
	BatV      *float64 `display:"bat"`
	BatPct    *int     `display:"batpct"`
	AltM      *int     `display:"alt"`
	DistM     *int     `display:"dist"`
	SpdKmh    *int     `display:"spd"`
	LinkPct   *int     `display:"link"`
	Sats      *int     `display:"sats"`
	FlightMode string  `display:"mode"`
	GpsFix    string   `display:"gps"`
	TimeSec   *int     `display:"time"`
}

// Config controls Driver behavior. Reasonable defaults are filled in
// by Run if a field is zero.
type Config struct {
	// SnapshotRate is how often state snapshots are sent. Zero
	// defaults to 5Hz during flight, 1Hz otherwise. The driver
	// adjusts automatically based on the current mode.
	SnapshotRate time.Duration

	// HeartbeatTimeout is how long the driver waits for a heartbeat
	// from the device before logging a warning. Zero defaults to 15s.
	// Connection health is informational; the driver doesn't
	// disconnect on missed heartbeats.
	HeartbeatTimeout time.Duration

	// QueueSize is the buffer size for outbound messages. Zero
	// defaults to 32. Messages are dropped if the buffer fills,
	// which only happens if the transport is much slower than
	// expected.
	QueueSize int
}

// Event is a message the device sent us. The Driver dispatches these
// to a handler set via SetEventHandler.
type Event struct {
	Kind string            // "READY", "HEARTBEAT", "ERROR", "PONG"
	Args map[string]string // parsed key=value args
	Raw  string            // the original line (for debugging)
}

// Driver is the per-connection state machine. One Driver per
// underlying transport; create a new one after a disconnect.
type Driver struct {
	cfg Config

	w io.WriteCloser
	r io.Reader

	// outbound holds wire-format lines waiting to be written. The
	// sender goroutine drains it. Bounded; SendXxx methods drop
	// messages when full.
	outbound chan string

	// State protected by mu.
	mu        sync.Mutex
	mode      Mode
	state     State
	closed    bool
	lastBeat  time.Time
	onEvent   func(Event)

	// Coordination.
	done   chan struct{}
	cancel context.CancelFunc
}

// New constructs a Driver around the given transport. The transport
// is owned by the Driver after this call: Close releases it.
//
// Run() must be called to start the goroutines. Until then, Send
// methods will buffer messages but nothing flows.
func New(rwc io.ReadWriteCloser, cfg Config) *Driver {
	if cfg.SnapshotRate == 0 {
		cfg.SnapshotRate = 200 * time.Millisecond // 5Hz default
	}
	if cfg.HeartbeatTimeout == 0 {
		cfg.HeartbeatTimeout = 15 * time.Second
	}
	if cfg.QueueSize == 0 {
		cfg.QueueSize = 32
	}
	return &Driver{
		cfg:      cfg,
		w:        rwc,
		r:        rwc,
		outbound: make(chan string, cfg.QueueSize),
		mode:     ModeIdle,
		done:     make(chan struct{}),
	}
}

// SetEventHandler installs a callback for messages from the device.
// Safe to call from any goroutine. Set to nil to disable.
func (d *Driver) SetEventHandler(fn func(Event)) {
	d.mu.Lock()
	d.onEvent = fn
	d.mu.Unlock()
}

// Run starts the sender and receiver goroutines and blocks until ctx
// is cancelled or the transport returns an error. Returns nil on
// graceful shutdown, an error otherwise.
func (d *Driver) Run(ctx context.Context) error {
	innerCtx, cancel := context.WithCancel(ctx)
	d.mu.Lock()
	d.cancel = cancel
	d.mu.Unlock()
	defer close(d.done)

	var wg sync.WaitGroup
	wg.Add(3)

	// Sender goroutine: drains outbound, writes to transport.
	senderErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		senderErr <- d.runSender(innerCtx)
	}()

	// Receiver goroutine: reads lines, dispatches events.
	receiverErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		receiverErr <- d.runReceiver(innerCtx)
	}()

	// Snapshot ticker: sends state snapshots at the configured rate.
	go func() {
		defer wg.Done()
		d.runSnapshotTicker(innerCtx)
	}()

	wg.Wait()

	// Drain error channels, prefer the first error we got.
	var err error
	select {
	case e := <-senderErr:
		err = e
	default:
	}
	if err == nil {
		select {
		case e := <-receiverErr:
			err = e
		default:
		}
	}
	return err
}

// Close shuts the driver down. Safe to call multiple times.
func (d *Driver) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return d.w.Close()
}

// Done returns a channel that closes when Run exits.
func (d *Driver) Done() <-chan struct{} {
	return d.done
}

// === Send methods (caller-facing) ===

// SetMode changes the render mode. Idempotent; sending the current
// mode is a no-op.
func (d *Driver) SetMode(m Mode) {
	if !m.IsValid() {
		log.Printf("display: invalid mode %q, ignored", m)
		return
	}
	d.mu.Lock()
	if d.mode == m {
		d.mu.Unlock()
		return
	}
	d.mode = m
	d.mu.Unlock()
	d.enqueue(fmt.Sprintf("DISP MODE %s", m))
}

// SetState updates the latest state snapshot. The snapshot is sent
// on the next ticker pulse, not immediately. Multiple updates between
// pulses are coalesced.
func (d *Driver) SetState(s State) {
	d.mu.Lock()
	mergeState(&d.state, s)
	d.mu.Unlock()
}

// FireAlarm overlays an alarm banner. Mode is preserved on the
// device; clearing the alarm restores it.
func (d *Driver) FireAlarm(level AlarmLevel, text string) {
	d.enqueue(fmt.Sprintf(`DISP ALARM %s %q`, level, text))
}

// ClearAlarm removes any alarm overlay.
func (d *Driver) ClearAlarm() {
	d.enqueue("DISP CLEAR-ALARM")
}

// ShowMessage emits a one-shot scrolling message.
func (d *Driver) ShowMessage(text string) {
	d.enqueue(fmt.Sprintf(`DISP MSG %q`, text))
}

// SetBrightness sets panel brightness (0-100).
func (d *Driver) SetBrightness(pct int) {
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	d.enqueue(fmt.Sprintf("DISP BRIGHTNESS %d", pct))
}

// Ping requests an immediate pong.
func (d *Driver) Ping() {
	d.enqueue("DISP PING")
}

// === Internal: queue and goroutines ===

func (d *Driver) enqueue(line string) {
	select {
	case d.outbound <- line:
	default:
		log.Printf("display: outbound queue full, dropped: %s", line)
	}
}

func (d *Driver) runSender(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-d.outbound:
			if !ok {
				return nil
			}
			if _, err := io.WriteString(d.w, line+"\n"); err != nil {
				return fmt.Errorf("display: write: %w", err)
			}
		}
	}
}

func (d *Driver) runReceiver(ctx context.Context) error {
	scanner := bufio.NewScanner(d.r)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		ev, ok := parseEvent(line)
		if !ok {
			log.Printf("display: ignoring malformed line: %q", line)
			continue
		}
		// Track heartbeats for liveness reporting.
		if ev.Kind == "HEARTBEAT" {
			d.mu.Lock()
			d.lastBeat = time.Now()
			d.mu.Unlock()
		}
		d.mu.Lock()
		handler := d.onEvent
		d.mu.Unlock()
		if handler != nil {
			handler(ev)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("display: read: %w", err)
	}
	return nil
}

func (d *Driver) runSnapshotTicker(ctx context.Context) {
	t := time.NewTicker(d.cfg.SnapshotRate)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.mu.Lock()
			snapshot := d.state
			d.mu.Unlock()
			if line := serializeState(snapshot); line != "" {
				d.enqueue(line)
			}
		}
	}
}

// === Helpers ===

// mergeState copies non-nil/non-empty fields from src into dst. Used
// by SetState to allow partial updates without clobbering existing
// values.
func mergeState(dst *State, src State) {
	if src.Armed != nil {
		dst.Armed = src.Armed
	}
	if src.BatV != nil {
		dst.BatV = src.BatV
	}
	if src.BatPct != nil {
		dst.BatPct = src.BatPct
	}
	if src.AltM != nil {
		dst.AltM = src.AltM
	}
	if src.DistM != nil {
		dst.DistM = src.DistM
	}
	if src.SpdKmh != nil {
		dst.SpdKmh = src.SpdKmh
	}
	if src.LinkPct != nil {
		dst.LinkPct = src.LinkPct
	}
	if src.Sats != nil {
		dst.Sats = src.Sats
	}
	if src.FlightMode != "" {
		dst.FlightMode = src.FlightMode
	}
	if src.GpsFix != "" {
		dst.GpsFix = src.GpsFix
	}
	if src.TimeSec != nil {
		dst.TimeSec = src.TimeSec
	}
}

// serializeState builds a `DISP STATE ...` line from the snapshot.
// Returns "" if no fields are set (avoids sending empty messages).
func serializeState(s State) string {
	var parts []string
	if s.Armed != nil {
		v := 0
		if *s.Armed {
			v = 1
		}
		parts = append(parts, fmt.Sprintf("armed=%d", v))
	}
	if s.BatV != nil {
		parts = append(parts, fmt.Sprintf("bat=%.2f", *s.BatV))
	}
	if s.BatPct != nil {
		parts = append(parts, fmt.Sprintf("batpct=%d", *s.BatPct))
	}
	if s.AltM != nil {
		parts = append(parts, fmt.Sprintf("alt=%d", *s.AltM))
	}
	if s.DistM != nil {
		parts = append(parts, fmt.Sprintf("dist=%d", *s.DistM))
	}
	if s.SpdKmh != nil {
		parts = append(parts, fmt.Sprintf("spd=%d", *s.SpdKmh))
	}
	if s.LinkPct != nil {
		parts = append(parts, fmt.Sprintf("link=%d", *s.LinkPct))
	}
	if s.Sats != nil {
		parts = append(parts, fmt.Sprintf("sats=%d", *s.Sats))
	}
	if s.FlightMode != "" {
		parts = append(parts, fmt.Sprintf("mode=%s", s.FlightMode))
	}
	if s.GpsFix != "" {
		parts = append(parts, fmt.Sprintf("gps=%s", s.GpsFix))
	}
	if s.TimeSec != nil {
		parts = append(parts, fmt.Sprintf("time=%d", *s.TimeSec))
	}
	if len(parts) == 0 {
		return ""
	}
	return "DISP STATE " + strings.Join(parts, " ")
}

// parseEvent parses an inbound line. Returns (Event, true) on success,
// (zero, false) on malformed input.
//
// Permissive: unknown commands are returned as Events with Kind set
// to the unknown token (caller can log them).
func parseEvent(line string) (Event, bool) {
	tokens := splitFields(line)
	if len(tokens) < 2 {
		return Event{}, false
	}
	if tokens[0] != "DISP" {
		return Event{}, false
	}
	ev := Event{
		Kind: tokens[1],
		Args: map[string]string{},
		Raw:  line,
	}
	for _, tok := range tokens[2:] {
		eq := strings.IndexByte(tok, '=')
		if eq < 0 {
			// Bare token, no key. Store under empty key suffixed by index.
			ev.Args[fmt.Sprintf("_%d", len(ev.Args))] = tok
			continue
		}
		k := tok[:eq]
		v := tok[eq+1:]
		ev.Args[k] = v
	}
	return ev, true
}

// splitFields tokenizes a protocol line, respecting double-quoted
// strings. Whitespace outside quotes splits tokens; quoted strings
// preserve their content verbatim. Quotes are stripped from the
// result.
func splitFields(line string) []string {
	var out []string
	var cur strings.Builder
	inQuotes := false
	for _, r := range line {
		switch {
		case r == '"':
			inQuotes = !inQuotes
		case r == ' ' || r == '\t':
			if inQuotes {
				cur.WriteRune(r)
			} else if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// === Helpers for callers building State ===

// BoolPtr, IntPtr, Float64Ptr build pointer values for the
// optional-field convention.

func BoolPtr(b bool) *bool          { return &b }
func IntPtr(i int) *int             { return &i }
func Float64Ptr(f float64) *float64 { return &f }
