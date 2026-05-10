#include "ipc.h"

#include <stdarg.h>
#include <stdio.h>
#include <string.h>

/*
 * No pico/stdlib.h here on purpose. Output goes through the writer callback
 * supplied by the caller, so this translation unit is host-buildable for tests.
 */

/* CRC-16/CCITT-FALSE, init=0xFFFF, poly=0x1021, no reflection, no xorout. */
uint16_t ipc_crc16(const uint8_t *data, size_t len) {
    uint16_t crc = 0xFFFF;
    for (size_t i = 0; i < len; i++) {
        crc ^= (uint16_t)data[i] << 8;
        for (int b = 0; b < 8; b++) {
            crc = (crc & 0x8000) ? (uint16_t)((crc << 1) ^ 0x1021) : (uint16_t)(crc << 1);
        }
    }
    return crc;
}

/*
 * RX state: a rolling buffer of COBS-encoded bytes, finalized when 0x00 arrives.
 * On finalize we COBS-decode in place into a separate decoded buffer, validate
 * CRC, and present the frame to the caller.
 */
static uint8_t  rx_cobs[ZTX_MAX_FRAME_COBS];
static size_t   rx_cobs_len = 0;
static uint8_t  rx_decoded[ZTX_MAX_FRAME_RAW];
static bool     rx_overrun = false;
static ipc_write_byte_fn s_write = 0;

void ipc_init(ipc_write_byte_fn writer) {
    rx_cobs_len = 0;
    rx_overrun = false;
    s_write = writer;
}

/* COBS decode src[0..src_len-1] into dst, returning decoded length or -1 on error. */
static int cobs_decode(const uint8_t *src, size_t src_len, uint8_t *dst, size_t dst_max) {
    if (src_len == 0) return -1;
    size_t si = 0, di = 0;
    while (si < src_len) {
        uint8_t code = src[si++];
        if (code == 0) return -1;            /* stray zero, malformed */
        size_t copy = code - 1u;
        if (si + copy > src_len) return -1;  /* code claims more bytes than exist */
        if (di + copy > dst_max) return -1;  /* dst overflow */
        for (size_t i = 0; i < copy; i++) dst[di++] = src[si++];
        if (code != 0xFF && si < src_len) {
            if (di >= dst_max) return -1;
            dst[di++] = 0;
        }
    }
    return (int)di;
}

/* COBS encode src[0..src_len-1] into dst, returning encoded length (no trailing 0x00). */
static int cobs_encode(const uint8_t *src, size_t src_len, uint8_t *dst, size_t dst_max) {
    if (src_len + 2 > dst_max) return -1;
    size_t code_idx = 0, di = 1;
    uint8_t code = 1;
    for (size_t si = 0; si < src_len; si++) {
        if (src[si] == 0) {
            dst[code_idx] = code;
            code = 1;
            code_idx = di++;
            if (di > dst_max) return -1;
        } else {
            if (di >= dst_max) return -1;
            dst[di++] = src[si];
            code++;
            if (code == 0xFF) {
                dst[code_idx] = code;
                code = 1;
                code_idx = di++;
                if (di > dst_max) return -1;
            }
        }
    }
    dst[code_idx] = code;
    return (int)di;
}

bool ipc_feed(uint8_t b, ipc_frame_t *out_frame) {
    if (b == 0x00) {
        /* End of frame. Try to decode whatever we accumulated. */
        if (rx_overrun || rx_cobs_len == 0) {
            rx_cobs_len = 0;
            rx_overrun = false;
            return false;
        }
        int dec_len = cobs_decode(rx_cobs, rx_cobs_len, rx_decoded, sizeof(rx_decoded));
        rx_cobs_len = 0;
        if (dec_len < 6) return false;        /* type+seq+len(2)+crc(2) minimum */
        uint16_t payload_len = (uint16_t)rx_decoded[2] | ((uint16_t)rx_decoded[3] << 8);
        if ((size_t)dec_len != (size_t)4 + payload_len + 2) return false;
        uint16_t got_crc = (uint16_t)rx_decoded[4 + payload_len]
                         | ((uint16_t)rx_decoded[4 + payload_len + 1] << 8);
        uint16_t calc_crc = ipc_crc16(rx_decoded, 4 + payload_len);
        if (got_crc != calc_crc) return false;
        out_frame->type    = rx_decoded[0];
        out_frame->seq     = rx_decoded[1];
        out_frame->len     = payload_len;
        out_frame->payload = &rx_decoded[4];
        return true;
    }
    if (rx_cobs_len < sizeof(rx_cobs)) {
        rx_cobs[rx_cobs_len++] = b;
    } else {
        rx_overrun = true;
    }
    return false;
}

int ipc_send(uint8_t type, uint8_t seq, const uint8_t *payload, uint16_t len) {
    if (len > ZTX_MAX_PAYLOAD) return -1;

    uint8_t raw[ZTX_MAX_FRAME_RAW];
    raw[0] = type;
    raw[1] = seq;
    raw[2] = (uint8_t)(len & 0xFF);
    raw[3] = (uint8_t)(len >> 8);
    if (len > 0 && payload != NULL) memcpy(&raw[4], payload, len);
    uint16_t crc = ipc_crc16(raw, 4u + len);
    raw[4 + len]     = (uint8_t)(crc & 0xFF);
    raw[4 + len + 1] = (uint8_t)(crc >> 8);

    uint8_t enc[ZTX_MAX_FRAME_COBS];
    int enc_len = cobs_encode(raw, 4u + len + 2u, enc, sizeof(enc));
    if (enc_len < 0) return -1;

    /* Transmit encoded frame + 0x00 delimiter through injected writer. */
    if (!s_write) return -1;
    for (int i = 0; i < enc_len; i++) s_write(enc[i]);
    s_write(0x00);
    return 0;
}

void ipc_log(const char *fmt, ...) {
    char buf[ZTX_MAX_PAYLOAD];
    va_list ap;
    va_start(ap, fmt);
    int n = vsnprintf(buf, sizeof(buf), fmt, ap);
    va_end(ap);
    if (n < 0) return;
    if ((size_t)n > sizeof(buf)) n = sizeof(buf);
    ipc_send(MSG_LOG, 0, (const uint8_t *)buf, (uint16_t)n);
}
