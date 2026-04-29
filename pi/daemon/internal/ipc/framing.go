package ipc

import (
	"errors"
	"fmt"
)

// CRC16 computes CRC-16/CCITT-FALSE (init=0xFFFF, poly=0x1021, no reflection,
// no xorout) over data. Matches the firmware implementation.
func CRC16(data []byte) uint16 {
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

// COBSEncode produces a COBS-encoded copy of data. The result does not include
// the trailing 0x00 delimiter; callers append it when writing to the wire.
func COBSEncode(data []byte) []byte {
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

// COBSDecode reverses COBSEncode. The input must not contain the 0x00
// frame delimiter.
func COBSDecode(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("cobs: empty input")
	}
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		code := data[i]
		if code == 0 {
			return nil, errors.New("cobs: unexpected zero in encoded stream")
		}
		copy := int(code) - 1
		i++
		if i+copy > len(data) {
			return nil, errors.New("cobs: code overruns buffer")
		}
		out = append(out, data[i:i+copy]...)
		i += copy
		if code != 0xFF && i < len(data) {
			out = append(out, 0)
		}
	}
	return out, nil
}

// Frame is a parsed protocol frame. Payload is a slice into the buffer
// supplied to ParseFrame; copy it if you need to retain it.
type Frame struct {
	Type    byte
	Seq     byte
	Payload []byte
}

// BuildFrame composes a complete on-the-wire frame: COBS-encoded inner bytes
// plus the trailing 0x00 delimiter.
func BuildFrame(msgType, seq byte, payload []byte) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, fmt.Errorf("ipc: payload too large: %d > %d", len(payload), MaxPayload)
	}
	inner := make([]byte, 4+len(payload)+2)
	inner[0] = msgType
	inner[1] = seq
	inner[2] = byte(len(payload) & 0xFF)
	inner[3] = byte((len(payload) >> 8) & 0xFF)
	copy(inner[4:], payload)
	crc := CRC16(inner[:4+len(payload)])
	inner[4+len(payload)] = byte(crc & 0xFF)
	inner[4+len(payload)+1] = byte((crc >> 8) & 0xFF)
	encoded := COBSEncode(inner)
	return append(encoded, 0x00), nil
}

// ParseFrame validates and unpacks a COBS-decoded inner frame.
func ParseFrame(decoded []byte) (Frame, error) {
	if len(decoded) < 6 {
		return Frame{}, fmt.Errorf("ipc: frame too short: %d", len(decoded))
	}
	plen := int(decoded[2]) | int(decoded[3])<<8
	if len(decoded) != 4+plen+2 {
		return Frame{}, fmt.Errorf("ipc: length mismatch: header says %d, frame is %d", plen, len(decoded)-6)
	}
	gotCRC := uint16(decoded[4+plen]) | uint16(decoded[4+plen+1])<<8
	wantCRC := CRC16(decoded[:4+plen])
	if gotCRC != wantCRC {
		return Frame{}, fmt.Errorf("ipc: bad CRC: got %#04x want %#04x", gotCRC, wantCRC)
	}
	return Frame{
		Type:    decoded[0],
		Seq:     decoded[1],
		Payload: decoded[4 : 4+plen],
	}, nil
}
