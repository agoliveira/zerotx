/*
 * ZeroTX IPC protocol definitions.
 * Shared between RP2040 firmware and Pi-side tools.
 *
 * Frame layout after COBS decode:
 *   [type:1][seq:1][len_lo:1][len_hi:1][payload:len][crc_lo:1][crc_hi:1]
 *
 * CRC: CRC-16/CCITT-FALSE over [type..payload] (inclusive).
 * On the wire each frame is COBS-encoded and terminated by 0x00.
 */
#ifndef ZEROTX_PROTOCOL_H
#define ZEROTX_PROTOCOL_H

#include <stdint.h>
#include <stddef.h>

/* Hot-path messages */
#define MSG_CHANNEL_INTENT  0x01  /* Pi -> MCU, 32 bytes (16 * uint16) */
#define MSG_INPUT_STATE     0x02  /* MCU -> Pi, empty in M1 */
#define MSG_HEARTBEAT       0x03  /* both, 1 byte seq */

/* Handshake messages.
 *
 * On link open, either side may send MSG_HELLO carrying its protocol
 * version and a human-readable version string. The other side responds
 * with MSG_HELLO_ACK in the same format. If the protocol versions
 * match, the daemon lifts its channel-intent gate and normal traffic
 * flows. If they mismatch, the daemon refuses to send channel intents;
 * the MCU watchdog eventually times out and FC failsafe takes over.
 *
 * Payload layout: [proto:1][reserved:3 zeros][version_str:N]
 */
#define MSG_HELLO           0x10
#define MSG_HELLO_ACK       0x11

/* Telemetry frame from FC (via ELRS, via UART RX) forwarded to the
 * daemon. Payload layout:
 *   [addr:1][type:1][crsf_payload:N]
 *
 * The daemon parses the CRSF type byte and dispatches to a per-frame
 * decoder. The MCU does NOT parse telemetry contents — keeping it dumb
 * means new sensor types are added in Go without firmware changes.
 *
 * Stale-detection and per-sensor caching live in the daemon. The MCU
 * forwards every well-formed CRSF frame as it arrives. */
#define MSG_TELEMETRY       0x12

/* Physical control panel input event. MCU -> Pi only. Sent on edge
 * detection (debounced) plus once at boot to establish ground truth.
 *
 * Payload layout: [input_id:1][state:1]
 *
 * input_id values are reserved per-input below (ZTX_INPUT_*). state
 * is the logical state in the protocol's polarity convention, not the
 * raw pin level: the MCU translates wiring polarity into the protocol
 * value so changes in physical wiring don't ripple to the daemon.
 *
 * For the arm key:
 *   state = 0  key DOWN (disarmed-intent / safe)
 *   state = 1  key UP   (arming-requested-intent)
 *
 * The MCU emits an event only on stable edge transitions. The daemon
 * may also see a single boot-time event reflecting the initial state.
 * The state machine in the daemon handles boot-key-up warning. */
#define MSG_INPUT_EVENT     0x05

/* Slow-path messages */
#define MSG_LOG             0x14  /* MCU -> Pi, ASCII string */

/* MSG_ARM_CONFIG (0x15): Pi -> MCU. Sent at link open and on every
 * model change. Configures the firmware-level defense-in-depth
 * disarm in arm_override.c.
 *
 * Payload (6 bytes, little-endian for the uint16 fields):
 *   [thr_idx:1][arm_idx:1][thr_threshold:2][arm_disarm_value:2]
 *
 * thr_idx          0-15  throttle channel slot (model-dependent;
 *                        TAER->0, AETR->2)
 * arm_idx          0-15  arm channel slot (conventionally 4)
 * thr_threshold    CRSF unit cutoff; throttle at or below this
 *                  is considered "zero" and permits a disarm
 * arm_disarm_value value to write into ch[arm_idx] on disarm
 *                  (conventionally ZTX_CRSF_CH_MIN = 172)
 *
 * Out-of-range indices are rejected by the firmware; the daemon
 * side also range-checks, so a malformed message means corruption
 * or a future daemon. Firmware keeps whatever config it had
 * (or compile-time defaults at boot). */
#define MSG_ARM_CONFIG      0x15

/* Reserved input IDs for MSG_INPUT_EVENT.
 *
 * 0x00 is reserved as "invalid / probe". Future controls-area inputs
 * (additional safety-critical hardware only — see project's control
 * panel design philosophy) take subsequent values. */
#define ZTX_INPUT_INVALID    0x00
#define ZTX_INPUT_ARM_KEY    0x01
#define ZTX_INPUT_MOMENTARY  0x02

/* Wire-format protocol version. Bumped only when frame format or
 * message semantics change in an incompatible way. Both sides must
 * agree on this at link open time.
 *
 * v2: adds MSG_TELEMETRY (0x12) carrying raw CRSF telemetry frames.
 *     Backward compatible at the IPC parser level (unknown messages
 *     are silently dropped) but the GUI features that depend on
 *     telemetry require a daemon and firmware that both speak v2.
 *
 * v3: adds MSG_INPUT_EVENT (0x05) carrying physical control-panel
 *     input edges. Backward compatible at the IPC parser level
 *     (unknown messages are silently dropped). Daemon arming
 *     features require both sides at v3.
 *
 * v4: adds MSG_ARM_CONFIG (0x15) for the firmware-level disarm
 *     safety net. Old daemon (v3) against new firmware: firmware
 *     uses compile-time TAER defaults; primary aircraft works,
 *     non-TAER models would have the wrong throttle slot. New
 *     daemon (v4) against old firmware: daemon's config push is
 *     dropped as unknown; daemon-side arm machine still works.
 *     Both sides on v4: firmware-level safety net is active.
 */
#define ZTX_PROTO_VERSION   4u

/* Sizing limits */
#define ZTX_MAX_PAYLOAD     256
#define ZTX_MAX_FRAME_RAW   (4 + ZTX_MAX_PAYLOAD + 2)        /* type+seq+len+payload+crc */
#define ZTX_MAX_FRAME_COBS  (ZTX_MAX_FRAME_RAW + 2 + 1)      /* COBS overhead + delimiter */

/* Channel count */
#define ZTX_CHANNELS        16

/* Heartbeat / failsafe budget (milliseconds) */
#define ZTX_HEARTBEAT_RX_TIMEOUT_MS  200u   /* missing this long -> HOLD */
#define ZTX_HOLD_MS                  600u   /* time spent in HOLD before FAILSAFE */
#define ZTX_HEARTBEAT_TX_PERIOD_MS   200u   /* MCU emits heartbeat at this rate */

/* CRSF emission cadence */
#define ZTX_CRSF_PERIOD_MS           20u    /* 50 Hz */

/* CRSF channel raw range (11-bit) */
#define ZTX_CRSF_CH_MIN              172u   /* ~988 us */
#define ZTX_CRSF_CH_MID              992u   /* ~1500 us */
#define ZTX_CRSF_CH_MAX              1811u  /* ~2012 us */

#endif /* ZEROTX_PROTOCOL_H */
