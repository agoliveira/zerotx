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
// sent on demand by the daemon's mixer loop.
type Link struct {
	port serial.Port

	// OnFrame is invoked for every parsed frame except MsgLog (which goes to
	// OnLog instead). Set before calling Run.
	OnFrame func(Frame)
	// OnLog receives MCU log strings. If nil, MCU logs go to stdlib log.
	OnLog func(string)

	mu     sync.Mutex
	txSeq  byte
	closed bool
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
func (l *Link) Run(ctx context.Context) error {
	go l.heartbeatLoop(ctx)

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
	if f.Type == MsgLog {
		s := string(f.Payload)
		if l.OnLog != nil {
			l.OnLog(s)
		} else {
			log.Printf("[mcu] %s", s)
		}
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
func (l *Link) SendChannelIntent(channels [Channels]uint16) error {
	var buf [Channels * 2]byte
	for i, v := range channels {
		binary.LittleEndian.PutUint16(buf[i*2:], v)
	}
	return l.sendFrame(MsgChannelIntent, buf[:])
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
