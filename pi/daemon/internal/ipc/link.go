package ipc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
)

// Link is a bidirectional connection to the RP2040 firmware over USB-CDC.
// It owns a goroutine that reads bytes off the wire and dispatches frames,
// and a goroutine that drives the periodic heartbeat. Channel intent is
// sent on demand by the daemon's mixer loop, gated on a successful
// protocol handshake (see MsgHello).
type Link struct {
	port serial.Port

	// OnFrame is invoked for every parsed frame except MsgLog (which goes to
	// OnLog instead) and the handshake messages (which are handled
	// internally). Set before calling Run.
	OnFrame func(Frame)
	// OnLog receives MCU log strings. If nil, MCU logs go to stdlib log.
	OnLog func(string)

	// LocalVersion is the daemon's human-readable version string sent in
	// the MsgHello payload. Optional; if empty, "" is sent.
	LocalVersion string

	mu     sync.Mutex
	txSeq  byte
	closed bool

	// Handshake state. Guarded by hsMu (separate mutex so a long-running
	// SendChannelIntent doesn't block dispatch).
	hsMu        sync.RWMutex
	hsOK        bool   // true once HelloAck received with matching proto
	hsLegacy    bool   // true once we've given up waiting and decided to proceed without ACK
	hsRemote    string // remote version string from HelloAck, for logging
	hsLoggedTx  bool   // tracks whether we've logged "intent dropped because handshake not done"
}

// Open opens the given serial device. Baud is mostly cosmetic for USB-CDC
// (the kernel ignores it) but the underlying library expects a value.
func Open(devPath string, baud int) (*Link, error) {
	mode := &serial.Mode{BaudRate: baud}
	port, err := serial.Open(devPath, mode)
	if err != nil {
		return nil, fmt.Errorf("ipc: open %s: %w", devPath, err)
	}
	if err := port.SetReadTimeout(50 * time.Millisecond); err != nil {
		port.Close()
		return nil, fmt.Errorf("ipc: set read timeout: %w", err)
	}
	return &Link{port: port}, nil
}

// Close stops the link and releases the serial port.
func (l *Link) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.port.Close()
}

func (l *Link) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

// Run reads bytes from the serial port, dispatches frames, and drives the
// heartbeat ticker until ctx is cancelled. Blocks; run in a goroutine.
//
// On startup, kicks off a handshake with the firmware (MsgHello). Channel
// intent is gated on the handshake completing successfully; until then,
// SendChannelIntent silently drops frames. Heartbeats and other traffic
// are unaffected so the firmware's watchdog stays satisfied.
func (l *Link) Run(ctx context.Context) error {
	go l.heartbeatLoop(ctx)
	go l.handshakeLoop(ctx)

	parser := NewParser()
	buf := make([]byte, 256)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if l.isClosed() {
			return nil
		}
		n, err := l.port.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) || l.isClosed() {
				return nil
			}
			// Read timeout reads return n=0, err=nil. Real errors land here.
			log.Printf("ipc: read: %v", err)
			return err
		}
		if n == 0 {
			continue
		}
		frames, perr := parser.Feed(buf[:n])
		if perr != nil {
			log.Printf("ipc: parser warning: %v", perr)
		}
		for _, f := range frames {
			l.dispatch(f)
		}
	}
}

func (l *Link) dispatch(f Frame) {
	switch f.Type {
	case MsgLog:
		s := string(f.Payload)
		if l.OnLog != nil {
			l.OnLog(s)
		} else {
			log.Printf("[mcu] %s", s)
		}
		return
	case MsgHello:
		// Firmware-initiated handshake. Reply with HelloAck and treat
		// the firmware's version as if we'd received an ack ourselves.
		// (Either side may initiate; the daemon usually gets there first
		// because it boots later, but this ensures correctness if the
		// firmware kicks off first after a daemon restart.)
		proto, ver := parseHelloPayload(f.Payload)
		l.recordHandshakeResult(proto, ver)
		if err := l.sendHello(MsgHelloAck); err != nil && !l.isClosed() {
			log.Printf("ipc: send HelloAck: %v", err)
		}
		return
	case MsgHelloAck:
		proto, ver := parseHelloPayload(f.Payload)
		l.recordHandshakeResult(proto, ver)
		return
	}
	if l.OnFrame != nil {
		l.OnFrame(f)
	}
}

func (l *Link) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(time.Duration(HeartbeatTxPeriodMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := l.sendFrame(MsgHeartbeat, []byte{0}); err != nil {
				if !l.isClosed() {
					log.Printf("ipc: heartbeat send: %v", err)
				}
				return
			}
		}
	}
}

// SendChannelIntent sends a 16-channel update to the MCU. Values are CRSF
// raw 11-bit (172..1811).
//
// Gated on the protocol handshake. Until the firmware acks our MsgHello
// (or we time out and decide to proceed in legacy mode), this method
// silently drops frames after logging once. Heartbeats continue
// independently; the MCU's watchdog stays satisfied during handshake.
func (l *Link) SendChannelIntent(channels [Channels]uint16) error {
	l.hsMu.RLock()
	gateOpen := l.hsOK || l.hsLegacy
	l.hsMu.RUnlock()
	if !gateOpen {
		l.hsMu.Lock()
		if !l.hsLoggedTx {
			log.Printf("ipc: dropping channel intent: protocol handshake not complete")
			l.hsLoggedTx = true
		}
		l.hsMu.Unlock()
		return nil
	}
	var buf [Channels * 2]byte
	for i, v := range channels {
		binary.LittleEndian.PutUint16(buf[i*2:], v)
	}
	return l.sendFrame(MsgChannelIntent, buf[:])
}

// HandshakeComplete reports whether the protocol handshake has reached a
// terminal state (either successful ack or legacy-mode fallback). Returns
// the firmware's version string if known and a flag indicating legacy mode.
func (l *Link) HandshakeComplete() (ok bool, legacy bool, remoteVersion string) {
	l.hsMu.RLock()
	defer l.hsMu.RUnlock()
	return l.hsOK, l.hsLegacy, l.hsRemote
}

// handshakeLoop sends MsgHello on link open and retries every 500ms until
// either an ack arrives (hsOK true) or 5 seconds elapse (legacy mode,
// hsLegacy true with a loud warning logged).
func (l *Link) handshakeLoop(ctx context.Context) {
	const (
		retryEvery = 500 * time.Millisecond
		giveUpAt   = 5 * time.Second
	)
	deadline := time.Now().Add(giveUpAt)
	t := time.NewTicker(retryEvery)
	defer t.Stop()
	// Send the first hello immediately, don't wait for the first tick.
	if err := l.sendHello(MsgHello); err != nil && !l.isClosed() {
		log.Printf("ipc: send Hello: %v", err)
	}
	for {
		l.hsMu.RLock()
		done := l.hsOK || l.hsLegacy
		l.hsMu.RUnlock()
		if done {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if l.isClosed() {
				return
			}
			if time.Now().After(deadline) {
				// Timeout: firmware didn't respond. Most likely an older
				// firmware that doesn't know about MsgHello. Proceed in
				// legacy mode rather than refuse to fly entirely.
				l.hsMu.Lock()
				if !l.hsOK {
					l.hsLegacy = true
					log.Printf("ipc: WARNING firmware did not respond to handshake within %v; " +
						"proceeding in legacy mode (compatibility unverified). Update firmware.",
						giveUpAt)
				}
				l.hsMu.Unlock()
				return
			}
			if err := l.sendHello(MsgHello); err != nil && !l.isClosed() {
				log.Printf("ipc: send Hello (retry): %v", err)
			}
		}
	}
}

// sendHello sends a MsgHello or MsgHelloAck (caller chooses) carrying the
// daemon's protocol version and version string.
func (l *Link) sendHello(msgType byte) error {
	payload := buildHelloPayload(ProtoVersion, l.LocalVersion)
	return l.sendFrame(msgType, payload)
}

// recordHandshakeResult is called when a Hello or HelloAck arrives. If
// the protocol versions match, the gate opens. If they mismatch, log
// loudly and leave the gate closed (channel intent stays gated; FC
// failsafe takes over once the MCU's watchdog times out).
func (l *Link) recordHandshakeResult(remoteProto uint8, remoteVersion string) {
	l.hsMu.Lock()
	defer l.hsMu.Unlock()
	if l.hsOK {
		return // already done; ignore stragglers
	}
	l.hsRemote = remoteVersion
	if remoteProto == ProtoVersion {
		l.hsOK = true
		log.Printf("ipc: handshake OK (proto=%d, firmware=%q)", remoteProto, remoteVersion)
		return
	}
	log.Printf("ipc: PROTOCOL MISMATCH: daemon proto=%d (%q), firmware proto=%d (%q). " +
		"Channel intent will not be emitted; update one side. " +
		"FC failsafe will take over once MCU watchdog times out.",
		ProtoVersion, l.LocalVersion, remoteProto, remoteVersion)
}

// buildHelloPayload constructs the wire payload for MsgHello / MsgHelloAck.
// Layout: [proto:1][reserved:3 zeros][version_str:N].
func buildHelloPayload(proto uint8, version string) []byte {
	v := []byte(version)
	if len(v) > MaxPayload-4 {
		v = v[:MaxPayload-4]
	}
	out := make([]byte, 4+len(v))
	out[0] = proto
	// out[1..3] reserved zeros
	copy(out[4:], v)
	return out
}

// parseHelloPayload extracts the protocol version and version string from
// a Hello/HelloAck payload. Tolerant of short payloads (returns 0, "" if
// the payload is malformed; the caller treats that as a mismatch).
func parseHelloPayload(payload []byte) (proto uint8, version string) {
	if len(payload) < 4 {
		return 0, ""
	}
	return payload[0], string(payload[4:])
}

func (l *Link) sendFrame(msgType byte, payload []byte) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return errors.New("ipc: link closed")
	}
	seq := l.txSeq
	l.txSeq++
	l.mu.Unlock()

	frame, err := BuildFrame(msgType, seq, payload)
	if err != nil {
		return err
	}
	_, err = l.port.Write(frame)
	return err
}

// AutoDetectPort picks a likely RP2040 USB-CDC device. Returns the first
// /dev/ttyACM* if any, otherwise the first /dev/ttyUSB*. Empty string + nil
// if none found.
func AutoDetectPort() (string, error) {
	ports, err := serial.GetPortsList()
	if err != nil {
		return "", err
	}
	var acm, usb []string
	for _, p := range ports {
		switch {
		case strings.HasPrefix(p, "/dev/ttyACM"):
			acm = append(acm, p)
		case strings.HasPrefix(p, "/dev/ttyUSB"):
			usb = append(usb, p)
		}
	}
	if len(acm) > 0 {
		return acm[0], nil
	}
	if len(usb) > 0 {
		return usb[0], nil
	}
	return "", nil
}
