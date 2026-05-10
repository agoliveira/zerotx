#ifndef ZEROTX_CRSF_H
#define ZEROTX_CRSF_H

#include <stdint.h>
#include <stddef.h>

#define CRSF_FRAME_RC_LEN  26       /* sync + len + type + 22 payload + crc */
#define CRSF_RX_MAX        64       /* max CRSF frame size including framing */

void crsf_init(unsigned int baud);

/*
 * Pack 16 channels (raw 11-bit values, 0..2047) into a CRSF RC_CHANNELS_PACKED frame.
 * out must point to at least CRSF_FRAME_RC_LEN bytes.
 */
void crsf_pack_rc(const uint16_t channels[16], uint8_t out[CRSF_FRAME_RC_LEN]);

/* Blocking send of a complete CRSF frame on the configured UART. */
void crsf_send(const uint8_t *frame, size_t len);

/* CRC8 DVB-S2 (poly 0xD5), exposed for tests. */
uint8_t crsf_crc8(const uint8_t *data, size_t len);

/*
 * Drain any available bytes from the CRSF UART into the internal RX
 * parser. For each complete, CRC-valid frame seen, the callback is
 * invoked with the frame's address byte, type byte, and payload.
 *
 *   addr     CRSF "destination address" byte (typically 0xC8 or 0xEA)
 *   type     CRSF frame type
 *   payload  pointer to the payload bytes (length = payload_len)
 *
 * Frames that fail CRC are silently discarded. This function is non-
 * blocking; call it frequently from the main loop.
 */
typedef void (*crsf_rx_cb_t)(uint8_t addr, uint8_t type,
                             const uint8_t *payload, size_t payload_len);
void crsf_rx_poll(crsf_rx_cb_t cb);

#endif /* ZEROTX_CRSF_H */
