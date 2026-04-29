// Package ipc implements the ZeroTX wire protocol used between the Pi-side
// daemon and the RP2040 firmware.
//
// On-the-wire frame format (after COBS decode):
//
//	[type:1][seq:1][len_lo:1][len_hi:1][payload:len][crc_lo:1][crc_hi:1]
//
// CRC is CRC-16/CCITT-FALSE over [type..payload]. Each frame is COBS-encoded
// and terminated with a 0x00 byte on the wire. The constants below mirror
// rp2040/src/protocol.h byte-for-byte and are kept in sync manually.
package ipc

// Message type codes.
const (
	MsgChannelIntent byte = 0x01 // Pi -> MCU, 32 bytes (16 * uint16 LE)
	MsgInputState    byte = 0x02 // MCU -> Pi, empty in M1
	MsgHeartbeat     byte = 0x03 // both, 1 byte seq
	MsgLog           byte = 0x14 // MCU -> Pi, ASCII string
)

// Sizing limits (must agree with the firmware).
const (
	MaxPayload   = 256
	MaxFrameRaw  = 4 + MaxPayload + 2  // type+seq+len+payload+crc
	MaxFrameCOBS = MaxFrameRaw + 2 + 1 // COBS overhead + delimiter
	Channels     = 16
)

// CRSF channel raw range (11-bit).
const (
	CrsfChMin uint16 = 172  // ~988 us
	CrsfChMid uint16 = 992  // ~1500 us
	CrsfChMax uint16 = 1811 // ~2012 us
)

// Heartbeat / failsafe budget, milliseconds. Daemon should send heartbeats
// well below HeartbeatRxTimeoutMs to absorb scheduler jitter.
const (
	HeartbeatRxTimeoutMs = 200 // MCU declares HOLD after this gap
	HoldMs               = 600 // HOLD window before FAILSAFE
	HeartbeatTxPeriodMs  = 100 // daemon -> MCU heartbeat interval
	CRSFPeriodMs         = 20  // 50 Hz channel intent
)
