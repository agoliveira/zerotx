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

	// MsgInputEvent: MCU -> Pi. Sent on stable edge transition of a
	// physical control-panel input, plus once at boot to establish
	// ground truth. Payload layout:
	//   [input_id:1][state:1]
	// Input IDs are reserved per-input below (InputArmKey, etc).
	// State is the logical (protocol-polarity) value, not the raw
	// pin level: the firmware translates wiring polarity so the
	// daemon sees a consistent semantic regardless of how the
	// physical switch is wired.
	MsgInputEvent byte = 0x05

	MsgHello    byte = 0x10 // both, [proto:1][reserved:3][version_str:N]
	MsgHelloAck byte = 0x11 // both, same payload as MsgHello

	// MsgTelemetry: MCU -> Pi. The MCU receives CRSF telemetry frames
	// from the ELRS module and forwards them as-is to the daemon for
	// parsing. Payload layout:
	//   [crsf_addr:1][crsf_type:1][crsf_payload:N]
	// The daemon's internal/telemetry package decodes these into typed
	// sensor data. Unknown frame types are logged once and ignored.
	MsgTelemetry byte = 0x12

	MsgLog byte = 0x14 // MCU -> Pi, ASCII string
)

// Reserved input IDs for MsgInputEvent.
//
// 0x00 is reserved as "invalid / probe". Future controls-area inputs
// (additional safety-critical hardware only) take subsequent values.
const (
	InputInvalid byte = 0x00
	InputArmKey  byte = 0x01
)

// ProtoVersion is the wire-format protocol version. Bumped only when the
// frame format or message semantics change in an incompatible way. Both
// the daemon and the RP2040 firmware must agree on this value at link
// open time; mismatches gate channel intent emission.
//
// v2: adds MsgTelemetry. New firmware against old daemon is
// backward-compatible at the IPC parser level (unknown messages are
// dropped by the parser); old firmware against new daemon means no
// telemetry data, but the daemon still works (auto-checks fall back
// to manual confirmations).
//
// v3 (current): adds MsgInputEvent. Daemon arming features
// (state machine, AUX channel control) require both sides at v3.
const ProtoVersion uint8 = 3

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
