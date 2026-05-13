// Package iohub is the daemon-side client of the Mega 2560 IO board
// (firmware/io). The Mega exposes a multi-subsystem structured
// protocol over USB-CDC: VFD, indicator LEDs,
// buttons, WS2813 strip, plus future LDR/buzzer all share one
// serial connection.
//
// This package owns the serial port and provides:
//
//   - Send(line): write a raw protocol command. Lazy connect, retry
//     once on transient I/O failure, never panics on missing device.
//   - OnEvent(handler): subscribe to unsolicited EVENT lines pushed
//     by the firmware (button presses, boot, ready signals, etc.).
//   - Run(ctx): start the read goroutine that parses incoming lines.
//
// Subsystem-specific helper packages (internal/vfd,
// etc.) take a *Client and call Send for
// their writes; they don't need to know about the wire syntax or
// reconnection logic.
//
// Three connection modes selected by the address string passed to
// New:
//   - "" (empty)    -> NullClient: every Send is a no-op, no read
//   - "log"         -> LogClient: writes go to the daemon log; no
//                      device opened, no events ever received
//   - any other     -> SerialClient: opens the named serial port
package iohub

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
)

// Client is the interface daemon code uses to talk to the Mega.
// All methods are safe for concurrent use.
type Client interface {
	// Send writes one newline-terminated command line. The line
	// must NOT include the trailing newline; this method appends
	// it. Returns nil on best-effort write success; an error if the
	// device was unreachable. A returned error is informational
	// only: callers should not treat it as fatal.
	Send(line string) error

	// OnEvent registers a handler for unsolicited EVENT lines.
	// Multiple handlers may be registered; all receive every event.
	// Handler signature: target is the subsystem identifier
	// ("button.0", "boot", "vfd.0", ...), payload is everything
	// after the target on the same line (may be empty).
	//
	// Handlers are called from the read goroutine; they should not
	// block. Spawn a goroutine if you need to do meaningful work.
	OnEvent(handler EventHandler)

	// Run the read goroutine for as long as ctx is alive. Reads from
	// the serial port and dispatches EVENT lines to subscribers.
	// Other line shapes (responses ">", errors "!") are logged but
	// otherwise ignored in this version. Run returns when ctx is
	// cancelled or when the underlying port permanently fails.
	Run(ctx context.Context) error

	// Close the underlying device.
	Close() error
}

// EventHandler is called for each unsolicited EVENT line.
type EventHandler func(target, payload string)

// New returns a Client appropriate for the given address.
// See package doc for the address conventions.
func New(addr string) Client {
	switch addr {
	case "":
		return &NullClient{}
	case "log":
		return &LogClient{}
	default:
		return &SerialClient{path: addr}
	}
}

// =============================================================================
// NullClient: no-op
// =============================================================================

type NullClient struct{}

func (*NullClient) Send(string) error            { return nil }
func (*NullClient) OnEvent(EventHandler)         {}
func (*NullClient) Run(context.Context) error    { return nil }
func (*NullClient) Close() error                 { return nil }

// =============================================================================
// LogClient: writes to daemon log; no device
// =============================================================================

type LogClient struct {
	mu       sync.Mutex
	handlers []EventHandler
}

func (c *LogClient) Send(line string) error {
	log.Printf("[iohub] %s", line)
	return nil
}

func (c *LogClient) OnEvent(h EventHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers = append(c.handlers, h)
}

func (c *LogClient) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (c *LogClient) Close() error { return nil }

// =============================================================================
// SerialClient: real hardware
// =============================================================================

// SerialClient opens a USB-CDC serial port and speaks the structured
// protocol. Lazy connect; reopens on transient failures so an
// unplugged Mega does NOT crash the daemon. Send returns an error
// when the device is currently unreachable; callers treat that as
// informational.
type SerialClient struct {
	path string

	mu     sync.Mutex
	port   serial.Port
	logged bool

	hMu      sync.RWMutex
	handlers []EventHandler
}

// open opens the serial port at c.path with USB-CDC defaults.
// Caller holds c.mu.
func (c *SerialClient) open() error {
	if c.port != nil {
		return nil
	}
	mode := &serial.Mode{
		BaudRate: 115200,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}
	p, err := serial.Open(c.path, mode)
	if err != nil {
		if !c.logged {
			log.Printf("iohub: open %s: %v (will retry on next write)", c.path, err)
			c.logged = true
		}
		return err
	}
	log.Printf("iohub: %s open at 115200", c.path)
	c.port = p
	c.logged = false
	return nil
}

func (c *SerialClient) closeLocked() {
	if c.port != nil {
		_ = c.port.Close()
		c.port = nil
	}
}

func (c *SerialClient) Send(line string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.open(); err != nil {
		return err
	}
	payload := []byte(line + "\n")
	if _, err := c.port.Write(payload); err != nil {
		c.closeLocked()
		// One retry; most common failure is the Mega being replugged.
		if openErr := c.open(); openErr != nil {
			return openErr
		}
		if _, retryErr := c.port.Write(payload); retryErr != nil {
			c.closeLocked()
			return retryErr
		}
	}
	return nil
}

func (c *SerialClient) OnEvent(h EventHandler) {
	c.hMu.Lock()
	defer c.hMu.Unlock()
	c.handlers = append(c.handlers, h)
}

func (c *SerialClient) dispatch(target, payload string) {
	c.hMu.RLock()
	defer c.hMu.RUnlock()
	for _, h := range c.handlers {
		h(target, payload)
	}
}

// Run the read goroutine. Reads lines from the port and dispatches
// EVENT lines to handlers. On port failure, sleeps briefly and tries
// to reopen. Loop exits only on ctx cancellation.
func (c *SerialClient) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// Make sure the port is open.
		c.mu.Lock()
		if err := c.open(); err != nil {
			c.mu.Unlock()
			// Backoff before next attempt; don't busy-spin.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}
		port := c.port
		c.mu.Unlock()

		// Read until the port fails or ctx cancels. The read is
		// blocking; we rely on ctx-driven Close() to unblock it on
		// shutdown (the port will return an error).
		reader := bufio.NewReader(port)
		c.readLoop(ctx, reader)

		// readLoop returned -> port broken or ctx done. Drop the
		// handle and loop will reopen.
		c.mu.Lock()
		c.closeLocked()
		c.mu.Unlock()
	}
}

func (c *SerialClient) readLoop(ctx context.Context, r *bufio.Reader) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line, err := r.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				log.Printf("iohub: read: %v", err)
			}
			return
		}
		c.handleLine(strings.TrimRight(line, "\r\n"))
	}
}

// handleLine parses one inbound line and dispatches.
func (c *SerialClient) handleLine(line string) {
	if line == "" {
		return
	}
	// EVENT <target> [payload...]
	if strings.HasPrefix(line, "EVENT ") {
		rest := strings.TrimPrefix(line, "EVENT ")
		target := rest
		payload := ""
		if i := strings.IndexByte(rest, ' '); i >= 0 {
			target = rest[:i]
			payload = rest[i+1:]
		}
		c.dispatch(target, payload)
		return
	}
	// Responses (>) and errors (!) are logged but not dispatched.
	// Future versions could plumb GET-response correlation here.
	if strings.HasPrefix(line, "> ") || strings.HasPrefix(line, "! ") {
		log.Printf("iohub: %s", line)
		return
	}
	// Anything else is unexpected; log to surface protocol drift.
	log.Printf("iohub: unexpected line: %q", line)
}

func (c *SerialClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked()
	return nil
}

// Compile-time assertion that all client variants satisfy Client.
var (
	_ Client = (*NullClient)(nil)
	_ Client = (*LogClient)(nil)
	_ Client = (*SerialClient)(nil)
)

// fmtLine is a tiny convenience for callers that want a Sprintf
// equivalent without importing fmt. Not strictly needed but reads
// cleaner at call sites.
func FmtLine(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}
