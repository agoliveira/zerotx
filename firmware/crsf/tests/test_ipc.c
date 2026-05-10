/*
 * Host-side test for src/ipc.c.
 *
 * Builds with the system's gcc (not arm-none-eabi). Round-trips a handful of
 * frames through encode -> decode and writes their on-the-wire bytes to stdout
 * as hex. tests/test_cross.py compares those bytes against Python's encoder
 * to confirm both sides agree.
 *
 * Build:
 *   gcc -std=c11 -Wall -Wextra -I../src -o test_ipc test_ipc.c ../src/ipc.c
 * Run:
 *   ./test_ipc > vectors.hex
 */

#include <assert.h>
#include <stdio.h>
#include <string.h>

#include "ipc.h"

static uint8_t  out_buf[2048];
static size_t   out_len;

static void capture(uint8_t b) {
    if (out_len < sizeof(out_buf)) out_buf[out_len++] = b;
}

static void dump_hex(const char *label, const uint8_t *buf, size_t n) {
    printf("%s ", label);
    for (size_t i = 0; i < n; i++) printf("%02x", buf[i]);
    printf("\n");
}

static void roundtrip(uint8_t type, uint8_t seq, const uint8_t *payload, uint16_t plen) {
    out_len = 0;
    int rc = ipc_send(type, seq, payload, plen);
    assert(rc == 0);

    /* Feed the bytes back into ipc_feed and confirm decode. */
    bool ok = false;
    ipc_frame_t got;
    for (size_t i = 0; i < out_len; i++) {
        if (ipc_feed(out_buf[i], &got)) ok = true;
    }
    assert(ok);
    assert(got.type == type);
    assert(got.seq == seq);
    assert(got.len == plen);
    if (plen) assert(memcmp(got.payload, payload, plen) == 0);

    char label[32];
    snprintf(label, sizeof(label), "vec type=%02x seq=%02x", type, seq);
    dump_hex(label, out_buf, out_len);
}

int main(void) {
    ipc_init(capture);

    /* heartbeat */
    uint8_t hb = 0x42;
    roundtrip(0x03, 42, &hb, 1);

    /* channel intent, all centered */
    uint8_t intent[32];
    for (int i = 0; i < 16; i++) {
        intent[i * 2]     = 0xE0;  /* 992 = 0x3E0 LE -> 0xE0 0x03 */
        intent[i * 2 + 1] = 0x03;
    }
    roundtrip(0x01, 0, intent, sizeof(intent));

    /* all-zeros payload to exercise COBS overhead */
    uint8_t zeros[32] = {0};
    roundtrip(0x01, 7, zeros, sizeof(zeros));

    /* log message (slow path, ASCII payload) */
    const char *msg = "hello, zerotx";
    roundtrip(0x14, 1, (const uint8_t *)msg, (uint16_t)strlen(msg));

    /* crc independent check */
    uint16_t c = ipc_crc16((const uint8_t *)"123456789", 9);
    fprintf(stderr, "crc16 of '123456789' = 0x%04x (want 0x29B1)\n", c);
    assert(c == 0x29B1);
    fprintf(stderr, "all host tests pass\n");
    return 0;
}
