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

/* Slow-path messages */
#define MSG_LOG             0x14  /* MCU -> Pi, ASCII string */

/* Wire-format protocol version. Bumped only when frame format or
 * message semantics change in an incompatible way. Both sides must
 * agree on this at link open time.
 */
#define ZTX_PROTO_VERSION   1u

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
