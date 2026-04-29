#ifndef ZEROTX_CRSF_H
#define ZEROTX_CRSF_H

#include <stdint.h>
#include <stddef.h>

#define CRSF_FRAME_RC_LEN  26       /* sync + len + type + 22 payload + crc */

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

#endif /* ZEROTX_CRSF_H */
