// Package sitl is the daemon's bench-test transport: speaks raw CRSF
// over TCP to an INAV SITL instance. Used in place of the RP2040 IPC
// link when -fc-tcp-addr is set.
//
// INAV SITL maps each configured UART to an ascending TCP port
// (UART1=5760 default MSP, UART2=5761 default MSP, UART3=5762 if
// configured as Serial RX, etc.). With UART3 configured as
// "Serial RX, CRSF" inside the SITL eeprom.bin, port 5762 acts as
// the CRSF receiver port: it expects CRSF channel frames inbound
// and emits CRSF telemetry frames outbound.
//
// What we send:
//
//	[0xC8 addr] [0x18 type=0x16 packed-channels frame] ...
//	  - addr 0xC8 = "Flight Controller"
//	  - type 0x16 = RC_CHANNELS_PACKED
//	  - payload: 16 channels x 11 bits packed (22 bytes)
//
// What we receive:
//
//	Standard CRSF telemetry frames with addr 0xC8: GPS, ATTITUDE,
//	BATTERY, FLIGHT_MODE, LINK_STATISTICS, etc.
//
// The sitl.Conn type mirrors the surface of *ipc.Link that the
// daemon actually uses: OnTelemetry callback, SendChannelIntent
// method, Run goroutine. The handshake / heartbeat / log forwarding
// of the IPC protocol is omitted (SITL has no concept of those).
package sitl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/agoliveira/zerotx/pi/daemon/internal/ipc"
)

// CRSF protocol constants.
const (
	crsfAddrFC          byte = 0xC8 // Flight Controller
	crsfTypeRCChannels  byte = 0x16
	rcChannelsPayloadSz      = 22 // 16 channels x 11 bits = 176 bits = 22 bytes
)

// ChannelRateHz controls how often we push channel updates to SITL.
// Real CRSF over ELRS runs at 100-500Hz; for SITL we don't need to
// match those rates, the simulator integrates whatever we send. 50Hz
// is plenty smooth and easy on the daemon's mixer loop.
const ChannelRateHz = 50

// Conn is a daemon-side proxy for INAV SITL's CRSF receiver port.
type Conn struct {
	addr string

	// OnTelemetry receives stripped CRSF payloads in the same shape
	// the IPC link forwards them: [addr:1][type:1][payload:N]. The
	// daemon's existing telemetry decoder doesn't care whether the
	// bytes came from a real RP2040 or from SITL.
	OnTelemetry func([]byte)

	// LocalVersion is logged once after connect; cosmetic only.
	LocalVersion string

	mu     sync.Mutex
	conn   net.Conn
	closed bool

	// chMu guards the most-recent channel snapshot the sender goroutine
	// pushes at ChannelRateHz. The mixer loop calls SendChannelIntent
	// to update; the sender re-emits even if SendChannelIntent isn't
	// called this tick (SITL needs continuous updates to avoid RX
	// failsafe).
	chMu       sync.Mutex
	chSnapshot [ipc.Channels]uint16
	chHasData  bool
}

// Dial opens a TCP connection to addr. Returns an open Conn or an
// error. The connection stays open for the life of the Conn; if it
// drops, Run logs and returns. Daemon's Restart=on-failure recovers.
func Dial(addr string) (*Conn, error) {
	if addr == "" {
		return nil, errors.New("sitl: addr is empty")
	}
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("sitl: dial %s: %w", addr, err)
	}
	return &Conn{addr: addr, conn: c}, nil
}

// Close drops the TCP connection.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// SendChannelIntent stages the latest channel snapshot for the
// sender goroutine to emit at the next tick. Cheap; doesn't block
// on the network. Drops are impossible: each tick re-reads the
// most recent snapshot.
func (c *Conn) SendChannelIntent(ch [ipc.Channels]uint16) error {
	c.chMu.Lock()
	c.chSnapshot = ch
	c.chHasData = true
	c.chMu.Unlock()
	return nil
}

// Run owns two goroutines for the duration of ctx: a TCP reader
// that parses incoming CRSF frames and dispatches them to
// OnTelemetry, and a ticker that emits the latest channel snapshot
// at ChannelRateHz. Returns when ctx is cancelled or the connection
// drops.
func (c *Conn) Run(ctx context.Context) error {
	if c.conn == nil {
		return errors.New("sitl: not dialed")
	}
	log.Printf("sitl: connected to %s (LocalVersion=%q)", c.addr, c.LocalVersion)

	errCh := make(chan error, 2)

	go func() {
		errCh <- c.runReader(ctx)
	}()
	go func() {
		errCh <- c.runSender(ctx)
	}()

	select {
	case <-ctx.Done():
		_ = c.Close()
		return nil
	case err := <-errCh:
		_ = c.Close()
		return err
	}
}

// runReader pulls CRSF frames off the TCP stream and dispatches.
func (c *Conn) runReader(ctx context.Context) error {
	buf := make([]byte, 0, 256)
	tmp := make([]byte, 256)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, err := c.conn.Read(tmp)
		if err != nil {
			if isTimeout(err) {
				continue
			}
			if errors.Is(err, io.EOF) {
				return errors.New("sitl: connection closed by remote")
			}
			return fmt.Errorf("sitl: read: %w", err)
		}
		buf = append(buf, tmp[:n]...)
		buf = c.drainFrames(buf)
	}
}

// drainFrames consumes complete CRSF frames from buf, dispatching
// each to OnTelemetry, and returns the remaining (incomplete) bytes
// to keep accumulating.
//
// CRSF frame on the wire: [addr][length][type][payload...][crc8].
// length is the count of bytes after itself, including type, payload
// and crc, so total wire length is length+2.
func (c *Conn) drainFrames(buf []byte) []byte {
	for {
		if len(buf) < 2 {
			return buf
		}
		// Sync byte filter: real CRSF traffic from a FC always has
		// addr 0xC8 (or in some versions 0xEA = radio-transmitter).
		// Anything else is junk; skip a byte and re-sync.
		if buf[0] != 0xC8 && buf[0] != 0xEA {
			buf = buf[1:]
			continue
		}
		length := int(buf[1])
		// Sanity: length is encoded as 2..62 typically; reject
		// obvious garbage to avoid waiting forever for a huge frame.
		if length < 2 || length > 62 {
			buf = buf[1:]
			continue
		}
		total := length + 2
		if len(buf) < total {
			return buf // need more bytes
		}
		// Validate CRC8 over [type..payload].
		inner := buf[2 : 2+length-1]
		crcRx := buf[2+length-1]
		if crcRx != crsfCRC(inner) {
			// Bad CRC: skip just one byte and retry alignment.
			buf = buf[1:]
			continue
		}
		// Strip to the IPC-equivalent shape: [addr][type][payload].
		stripped := make([]byte, 0, 1+length-1)
		stripped = append(stripped, buf[0])
		stripped = append(stripped, inner...)
		if c.OnTelemetry != nil {
			c.OnTelemetry(stripped)
		}
		buf = buf[total:]
	}
}

// runSender emits a packed channel frame at ChannelRateHz from the
// most recent snapshot. SITL expects continuous updates: skipping
// for too long makes INAV declare RX failsafe.
func (c *Conn) runSender(ctx context.Context) error {
	tick := time.NewTicker(time.Second / time.Duration(ChannelRateHz))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		c.chMu.Lock()
		hasData := c.chHasData
		snap := c.chSnapshot
		c.chMu.Unlock()
		if !hasData {
			// Until the daemon's mixer has produced a first snapshot,
			// emit center-stick across all channels so SITL doesn't
			// see a stuck-throttle armed state if we somehow start
			// armed (we don't, but defensive).
			for i := range snap {
				snap[i] = ipc.CrsfChMid
			}
		}
		frame := buildRCChannelsFrame(snap)
		if _, err := c.conn.Write(frame); err != nil {
			return fmt.Errorf("sitl: write: %w", err)
		}
	}
}

// buildRCChannelsFrame builds a CRSF RC_CHANNELS_PACKED frame: 16
// channels packed at 11 bits each into 22 bytes of payload, framed
// as [addr=0xC8][length=24][type=0x16][22 bytes][crc].
func buildRCChannelsFrame(ch [ipc.Channels]uint16) []byte {
	// Pack 16 x 11-bit channels into 22 bytes, LSB-first per CRSF.
	var packed [rcChannelsPayloadSz]byte
	var bitpos uint
	for i := 0; i < ipc.Channels; i++ {
		v := uint64(ch[i] & 0x7FF) // mask to 11 bits
		// Place v at bitpos; the lowest bit of v goes to the byte
		// (bitpos>>3) at sub-bit (bitpos & 7).
		shift := bitpos & 7
		idx := bitpos >> 3
		// Up to three bytes may be touched per channel.
		packed[idx] |= byte(v << shift)
		packed[idx+1] |= byte(v >> (8 - shift))
		if shift > 5 {
			packed[idx+2] |= byte(v >> (16 - shift))
		}
		bitpos += 11
	}

	out := make([]byte, 0, 4+rcChannelsPayloadSz)
	out = append(out, crsfAddrFC)              // addr
	out = append(out, byte(rcChannelsPayloadSz+2)) // length: type+payload+crc = 22+2
	out = append(out, crsfTypeRCChannels)       // type
	out = append(out, packed[:]...)             // 22 bytes payload
	// CRC over [type..payload].
	crcInput := out[2:]
	out = append(out, crsfCRC(crcInput))
	return out
}

// crsfCRC computes the CRSF CRC8 (poly 0xD5, init 0x00) over the
// input. Identical to internal/crsftee but duplicated here to keep
// this package independent (the tee depends on this calling shape;
// vice versa would create a circular import).
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

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
