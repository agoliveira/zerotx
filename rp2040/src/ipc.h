#ifndef ZEROTX_IPC_H
#define ZEROTX_IPC_H

#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>
#include "protocol.h"

/* Decoded frame, valid until next ipc_feed call returns true. */
typedef struct {
    uint8_t  type;
    uint8_t  seq;
    uint16_t len;
    const uint8_t *payload;  /* points into rx_buf */
} ipc_frame_t;

/* Transport hook: byte-at-a-time writer the IPC layer uses for output. */
typedef void (*ipc_write_byte_fn)(uint8_t b);

/* Initialize parser state and bind the output transport. */
void ipc_init(ipc_write_byte_fn writer);

/*
 * Feed one byte from USB into the parser.
 * Returns true and fills out_frame when a full, CRC-valid frame is decoded.
 * The frame contents are valid only until the next call to ipc_feed.
 */
bool ipc_feed(uint8_t b, ipc_frame_t *out_frame);

/*
 * Build and write a COBS-framed message to USB.
 * payload may be NULL if len==0. Returns 0 on success, -1 on overflow.
 */
int ipc_send(uint8_t type, uint8_t seq, const uint8_t *payload, uint16_t len);

/* Convenience: emit a printf-style LOG message (truncated to ZTX_MAX_PAYLOAD). */
void ipc_log(const char *fmt, ...);

/* CRC-16/CCITT-FALSE, exposed for tests. */
uint16_t ipc_crc16(const uint8_t *data, size_t len);

#endif /* ZEROTX_IPC_H */
