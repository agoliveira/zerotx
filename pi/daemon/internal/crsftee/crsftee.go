// Package crsftee fans out CRSF telemetry frames to TCP clients.
//
// Used by ZeroTX to feed mwptools (which runs as a separate process
// on the left LCD) the live telemetry stream from the aircraft.
// mwp connects as a TCP client (tcp://host:port) with protocol
// dropdown set to "CRSF".
//
// The daemon's IPC link delivers CRSF frames in a stripped form:
//
//	[crsf_addr:1][crsf_type:1][crsf_payload:N]
//
// (The RP2040 firmware drops the sync byte's length companion and
// the CRC8 trailer because the IPC frame already has its own
// framing.) To produce a frame mwp can parse, the tee reconstructs
// the on-the-wire shape:
//
//	[addr:1][length:1][type:1][payload:N][crc8:1]
//
// where length = type + payload + crc, and crc8 covers
// [type, payload] using the CRSF polynomial (0xD5, init 0x00).
//
// The tee is read-only by design (see HANDOVER.md). Mission
// upload, parameter changes, and other commands from mwp would
// conflict with the daemon's own channel intent loop and are
// deferred to a future bidirectional design.
package crsftee

import (
	"context"
	"errors"
	"log"
	"net"
	"sync"
	"time"
)

// CRSF sync byte for frames addressed to the flight controller
// (and from FC for telemetry replies that ride the same bus).
const crsfSync byte = 0xC8

// Tee accepts TCP connections and forwards reconstructed CRSF
// frames to each connected client. Slow clients are dropped frames
// (per-client buffered channel with non-blocking send) so the link
// reader is never stalled by a sluggish consumer.
type Tee struct {
	addr     string
	listener net.Listener
	logf     func(format string, args ...interface{})

	mu      sync.Mutex
	clients map[*client]struct{}
	closed  bool
}

type client struct {
	conn net.Conn
	ch   chan []byte
}

// New constructs a Tee bound to addr. An empty addr disables the
// tee (Run returns nil immediately, Forward is a no-op). logf is
// the logger to use; nil falls back to log.Printf.
func New(addr string, logf func(format string, args ...interface{})) *Tee {
	if logf == nil {
		logf = log.Printf
	}
	return &Tee{
		addr:    addr,
		logf:    logf,
		clients: make(map[*client]struct{}),
	}
}

// Run starts the accept loop and blocks until ctx is cancelled or
// a fatal error occurs. Returns nil on graceful shutdown. If addr
// is empty (tee disabled), returns nil immediately.
func (t *Tee) Run(ctx context.Context) error {
	if t.addr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", t.addr)
	if err != nil {
		return err
	}
	t.listener = ln
	t.logf("crsftee: listening on %s (mwp: tcp://%s, protocol=CRSF)", t.addr, t.addr)

	// Closer goroutine: cancellation drops the listener, which
	// unblocks Accept below with an error we can recognise.
	go func() {
		<-ctx.Done()
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		t.handleClient(ctx, conn)
	}
}

// handleClient registers a new connection and spawns its writer
// goroutine. The writer reads from a buffered channel and pushes
// to the socket; on any error or context cancellation the client
// is removed and the connection closed.
func (t *Tee) handleClient(ctx context.Context, conn net.Conn) {
	cl := &client{
		conn: conn,
		ch:   make(chan []byte, 64),
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		conn.Close()
		return
	}
	t.clients[cl] = struct{}{}
	count := len(t.clients)
	t.mu.Unlock()

	t.logf("crsftee: client connected from %s (total=%d)", conn.RemoteAddr(), count)

	go func() {
		defer func() {
			conn.Close()
			t.mu.Lock()
			delete(t.clients, cl)
			remaining := len(t.clients)
			t.mu.Unlock()
			t.logf("crsftee: client disconnected from %s (remaining=%d)", conn.RemoteAddr(), remaining)
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case frame, ok := <-cl.ch:
				if !ok {
					return
				}
				conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
				if _, err := conn.Write(frame); err != nil {
					return
				}
			}
		}
	}()
}

// Forward takes a stripped IPC telemetry payload and pushes a
// reconstructed CRSF frame to every connected client. Non-blocking:
// a client whose buffer is full has the frame dropped silently.
//
// Safe to call from any goroutine. No-op if the tee is disabled
// or has no clients.
func (t *Tee) Forward(stripped []byte) {
	frame := buildFrame(stripped)
	if frame == nil {
		return
	}

	// Snapshot the client list under lock, then send outside the
	// lock so a slow socket can't stall other clients or the
	// caller's hot path.
	t.mu.Lock()
	if len(t.clients) == 0 {
		t.mu.Unlock()
		return
	}
	channels := make([]chan []byte, 0, len(t.clients))
	for c := range t.clients {
		channels = append(channels, c.ch)
	}
	t.mu.Unlock()

	for _, ch := range channels {
		select {
		case ch <- frame:
		default:
			// Slow client; drop this frame. Dropping is preferable
			// to blocking the link reader.
		}
	}
}

// buildFrame reconstructs an on-the-wire CRSF frame from the
// daemon's IPC-stripped form ([addr][type][payload]). Returns nil
// if input is too short to be a valid frame.
//
// On the wire:
//
//	[addr:1][length:1][type:1][payload:N][crc8:1]
//
// length = N + 2 (type + payload + crc). crc8 covers [type, payload].
func buildFrame(stripped []byte) []byte {
	if len(stripped) < 2 {
		return nil
	}
	addr := stripped[0]
	inner := stripped[1:] // type + payload
	crc := crsfCRC(inner)
	out := make([]byte, 0, len(stripped)+2)
	out = append(out, addr)
	out = append(out, byte(len(inner)+1)) // +1 for CRC byte
	out = append(out, inner...)
	out = append(out, crc)
	return out
}

// crsfCRC computes the CRSF CRC8 (polynomial 0xD5, init 0x00) over
// the input bytes. Used for the trailing byte of every CRSF frame.
func crsfCRC(data []byte) byte {
	var crc byte = 0
	for _, b := range data {
		crc ^= b
		for i := 0; i < 8; i++ {
			if crc&0x80 != 0 {
				crc = (crc << 1) ^ 0xD5
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
