// Package ipc implements the ZeroTX wire protocol used between the Pi-side
// daemon and the RP2040 firmware.
//
// On-the-wire frame format (after COBS decode):
//
//	[type:1][seq:1][len_lo:1][len_hi:1][payload:len][crc_lo:1][crc_hi:1]
//
// CRC is CRC-16/CCITT-FALSE over [type..payload]. Each frame is COBS-encoded
// and terminated with a 0x00 byte on the wire. The constants below mirror
// firmware/crsf/src/protocol.h byte-for-byte and are kept in sync manually.
package ipc

import "fmt"

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

	// MsgArmConfig: Pi -> MCU. Sent at link open and on model change.
	// Tells the firmware which channel slots are throttle and arm in
	// the current model, plus the disarm thresholds and values.
	// Payload layout (6 bytes, little-endian for the uint16 fields):
	//   [thrIdx:1][armIdx:1][thrThreshold:2][armDisarmValue:2]
	//
	// Used by the RP2040's defense-in-depth disarm: when ARM-key
	// transitions high->low, if ch[thrIdx] <= thrThreshold the
	// firmware writes armDisarmValue to ch[armIdx] in the outbound
	// channel buffer, then sends the usual MsgInputEvent up to the
	// daemon. The daemon's arm machine performs the same check
	// independently; the firmware path is the safety net for a hung
	// daemon, not the primary mechanism.
	//
	// Index fields are bounded by ipc.Channels (16); out-of-range
	// values cause the firmware to reject the config and stay with
	// whatever it had before (or its compile-time defaults at boot).
	MsgArmConfig byte = 0x15

	MsgLog byte = 0x14 // MCU -> Pi, ASCII string
)

// Reserved input IDs for MsgInputEvent.
//
// 0x00 is reserved as "invalid / probe". Future controls-area inputs
// (additional safety-critical hardware only) take subsequent values.
const (
	InputInvalid   byte = 0x00
	InputArmKey    byte = 0x01
	InputMomentary byte = 0x02
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
//
// MsgArmConfig (0x15) is registered in v3 by code organization but
// the firmware-side handler arrives in a separate firmware commit.
// Until then the daemon may choose to send it; old firmware drops
// unknown message types harmlessly (see the parser convention noted
// for v2). ProtoVersion will bump to 4 when the firmware-side
// arm_override module lands.
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

// ArmConfig is the typed view of MsgArmConfig's payload. Fields are
// what the RP2040 needs to perform the firmware-level disarm check
// independently of the daemon.
type ArmConfig struct {
	// ThrIdx is the 0-based channel slot the model's throttle stick
	// feeds into. Model-dependent: TAER -> 0, AETR -> 2, etc.
	// Resolved daemon-side via model.EdgeTXModel.ThrottleChannel().
	ThrIdx uint8

	// ArmIdx is the 0-based channel slot the model's arm switch
	// drives. Conventionally 4 (operator's "channel 5") on the
	// project's primary models.
	ArmIdx uint8

	// ThrThreshold is the throttle-low cutoff in CRSF units. At or
	// below this value the firmware considers throttle "zero" and
	// permits a disarm. Matches the daemon's ThrottleChanged check
	// (cmd/zerotxd/main.go: ch[thrIdx] <= 200) so both layers agree.
	ThrThreshold uint16

	// ArmDisarmValue is what the firmware writes to ch[ArmIdx] when
	// cutting the channel on a permitted disarm. Conventionally
	// CrsfChMin (172) -- the same value the daemon's channel mapper
	// produces when the arm state machine is in DISARMED.
	ArmDisarmValue uint16
}

// BuildArmConfigPayload serializes c into the 6-byte payload format
// of MsgArmConfig. Caller wraps with BuildFrame(MsgArmConfig, seq, ...).
func BuildArmConfigPayload(c ArmConfig) []byte {
	return []byte{
		c.ThrIdx,
		c.ArmIdx,
		byte(c.ThrThreshold & 0xff),
		byte(c.ThrThreshold >> 8),
		byte(c.ArmDisarmValue & 0xff),
		byte(c.ArmDisarmValue >> 8),
	}
}

// ParseArmConfigPayload decodes a 6-byte MsgArmConfig payload into
// an ArmConfig. Returns an error on short payloads or when index
// fields are out of range. Range-check matches what the firmware
// will do on receive; bounds-checking here is a defense-in-depth
// against the daemon ever building a malformed message.
func ParseArmConfigPayload(p []byte) (ArmConfig, error) {
	if len(p) < 6 {
		return ArmConfig{}, fmt.Errorf("ipc: arm-config payload too short: %d", len(p))
	}
	c := ArmConfig{
		ThrIdx:         p[0],
		ArmIdx:         p[1],
		ThrThreshold:   uint16(p[2]) | uint16(p[3])<<8,
		ArmDisarmValue: uint16(p[4]) | uint16(p[5])<<8,
	}
	if c.ThrIdx >= Channels {
		return ArmConfig{}, fmt.Errorf("ipc: arm-config thrIdx %d out of range (max %d)", c.ThrIdx, Channels-1)
	}
	if c.ArmIdx >= Channels {
		return ArmConfig{}, fmt.Errorf("ipc: arm-config armIdx %d out of range (max %d)", c.ArmIdx, Channels-1)
	}
	return c, nil
}
