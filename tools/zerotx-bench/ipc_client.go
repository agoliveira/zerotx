package main

// IPC client for the COBS-framed binary protocol the RP2040 speaks.
// Mirrors pi/daemon/internal/ipc framing byte-for-byte; duplicated
// here rather than imported because the daemon's ipc package is
// internal-only relative to the daemon's go.mod and the bench tool
// is a separate module. The two implementations must stay in sync;
// when the framing changes upstream, this file changes too.

import (
	"bufio"
	"errors"
	"fmt"
	"time"

	"go.bug.st/serial"
)

// Wire constants. Must match firmware/crsf/src/protocol.h byte-for-
// byte and pi/daemon/internal/ipc/protocol.go.
const (
	ipcMsgChannelIntent byte = 0x01
	ipcMsgInputState    byte = 0x02
	ipcMsgHeartbeat     byte = 0x03
	ipcMsgInputEvent    byte = 0x05
	ipcMsgHello         byte = 0x10
	ipcMsgHelloAck      byte = 0x11
	ipcMsgTelemetry     byte = 0x12
	ipcMsgLog           byte = 0x14
	ipcMsgArmConfig     byte = 0x15

	ipcMaxPayload  = 256
	ipcProtoVer    = 4
	ipcChannels    = 16
	ipcCrsfChMin   = 172
	ipcCrsfChMid   = 992
	ipcCrsfChMax   = 1811
)

// ipcCRC16 computes CRC-16/CCITT-FALSE.
func ipcCRC16(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func ipcCOBSEncode(data []byte) []byte {
	out := make([]byte, 1, len(data)+2)
	codeIdx := 0
	code := byte(1)
	for _, b := range data {
		if b == 0 {
			out[codeIdx] = code
			code = 1
			codeIdx = len(out)
			out = append(out, 0)
		} else {
			out = append(out, b)
			code++
			if code == 0xFF {
				out[codeIdx] = code
				code = 1
				codeIdx = len(out)
				out = append(out, 0)
			}
		}
	}
	out[codeIdx] = code
	return out
}

func ipcCOBSDecode(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("cobs: empty")
	}
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		code := data[i]
		if code == 0 {
			return nil, errors.New("cobs: unexpected zero")
		}
		cp := int(code) - 1
		i++
		if i+cp > len(data) {
			return nil, errors.New("cobs: overrun")
		}
		out = append(out, data[i:i+cp]...)
		i += cp
		if code != 0xFF && i < len(data) {
			out = append(out, 0)
		}
	}
	return out, nil
}

// ipcFrame is a parsed protocol frame.
type ipcFrame struct {
	Type    byte
	Seq     byte
	Payload []byte
}

func ipcBuildFrame(msgType, seq byte, payload []byte) ([]byte, error) {
	if len(payload) > ipcMaxPayload {
		return nil, fmt.Errorf("payload too large: %d > %d", len(payload), ipcMaxPayload)
	}
	inner := make([]byte, 4+len(payload)+2)
	inner[0] = msgType
	inner[1] = seq
	inner[2] = byte(len(payload) & 0xFF)
	inner[3] = byte((len(payload) >> 8) & 0xFF)
	copy(inner[4:], payload)
	crc := ipcCRC16(inner[:4+len(payload)])
	inner[4+len(payload)] = byte(crc & 0xFF)
	inner[4+len(payload)+1] = byte((crc >> 8) & 0xFF)
	return append(ipcCOBSEncode(inner), 0x00), nil
}

func ipcParseFrame(decoded []byte) (ipcFrame, error) {
	if len(decoded) < 6 {
		return ipcFrame{}, fmt.Errorf("frame too short: %d", len(decoded))
	}
	plen := int(decoded[2]) | int(decoded[3])<<8
	if len(decoded) != 4+plen+2 {
		return ipcFrame{}, fmt.Errorf("length mismatch: header=%d, frame=%d", plen, len(decoded)-6)
	}
	gotCRC := uint16(decoded[4+plen]) | uint16(decoded[4+plen+1])<<8
	wantCRC := ipcCRC16(decoded[:4+plen])
	if gotCRC != wantCRC {
		return ipcFrame{}, fmt.Errorf("bad CRC: got %#04x want %#04x", gotCRC, wantCRC)
	}
	return ipcFrame{
		Type:    decoded[0],
		Seq:     decoded[1],
		Payload: decoded[4 : 4+plen],
	}, nil
}

// ipcClient is the bench tool's RP2040 link. Opens the port at the
// daemon's standard 115200 baud (USB-CDC ignores baud), reads
// 0x00-delimited COBS-framed packets, and provides a Send method
// for the small writes we need (hello, heartbeat, channel intent
// for the ELRS probe).
type ipcClient struct {
	port    serial.Port
	br      *bufio.Reader
	timeout time.Duration
	seq     byte
}

// ipcRP2040Patterns matches the Raspberry Pi Pico in USB-CDC mode.
// Bare Picos enumerate as "Raspberry_Pi_Pico"; some firmwares
// override the descriptor.
var ipcRP2040Patterns = []string{
	"Raspberry_Pi_Pico",
	"Pico",
}

const ipcBaud = 115200

func openIPCClient(timeout time.Duration) (*ipcClient, string, error) {
	ports, err := findUSBSerial(ipcRP2040Patterns...)
	if err != nil {
		return nil, "", err
	}
	if len(ports) == 0 {
		return nil, "", fmt.Errorf("no RP2040 (Raspberry_Pi_Pico) found under %s", serialByIDDir)
	}
	port, err := openSerialAt(ports[0].Path, ipcBaud)
	if err != nil {
		return nil, ports[0].ByID, fmt.Errorf("open %s: %w", ports[0].Path, err)
	}
	_ = port.SetReadTimeout(timeout)
	return &ipcClient{
		port:    port,
		br:      bufio.NewReaderSize(port, 4096),
		timeout: timeout,
	}, ports[0].ByID, nil
}

func (c *ipcClient) Close() error {
	if c == nil || c.port == nil {
		return nil
	}
	return c.port.Close()
}

// Send transmits one frame. The seq byte auto-increments.
func (c *ipcClient) Send(msgType byte, payload []byte) error {
	frame, err := ipcBuildFrame(msgType, c.seq, payload)
	if err != nil {
		return err
	}
	c.seq++
	_, err = c.port.Write(frame)
	return err
}

// ReadFrame reads one COBS-framed packet. Returns when a frame is
// parsed, when the read deadline expires, or on read error. Bad
// frames (CRC fail, decode error) are dropped silently and reading
// continues -- the firmware sometimes emits partial frames during
// USB enumeration glitches.
func (c *ipcClient) ReadFrame() (ipcFrame, error) {
	for {
		// Read up to and including the 0x00 delimiter.
		raw, err := c.br.ReadBytes(0x00)
		if err != nil {
			return ipcFrame{}, err
		}
		if len(raw) <= 1 {
			continue // just the delimiter
		}
		// Strip trailing 0x00.
		raw = raw[:len(raw)-1]
		decoded, err := ipcCOBSDecode(raw)
		if err != nil {
			continue // partial-frame from boot, retry
		}
		frame, err := ipcParseFrame(decoded)
		if err != nil {
			continue
		}
		return frame, nil
	}
}

// buildHelloPayload formats a HELLO/HELLO_ACK payload:
// [proto:1][reserved:3=0][version_str:N].
func buildHelloPayload(proto uint8, version string) []byte {
	out := make([]byte, 4, 4+len(version))
	out[0] = proto
	// reserved bytes already zero
	return append(out, []byte(version)...)
}

// parseHelloPayload extracts (proto, version) from a HELLO/HELLO_ACK
// payload. Tolerant of short payloads (returns "" for missing
// version).
func parseHelloPayload(p []byte) (proto uint8, version string) {
	if len(p) < 1 {
		return 0, ""
	}
	proto = p[0]
	if len(p) > 4 {
		version = string(p[4:])
	}
	return
}
