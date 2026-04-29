/*
 * Host-side test for src/crsf.c RX parser. Verifies that:
 *  - A well-formed CRSF telemetry frame is parsed and the callback
 *    sees the right address, type, payload.
 *  - A frame with a bad CRC is discarded silently.
 *  - The parser resyncs after garbage bytes.
 *
 * Build:
 *   gcc -std=c11 -Wall -Wextra -DCRSF_TEST_HOST -I../src \
 *       -o test_crsf_rx test_crsf_rx.c ../src/crsf.c
 *
 * Note: src/crsf.c uses Pico SDK headers (pico/stdlib.h, hardware/uart.h)
 * for the TX path. For the host-side build we shim these in test_crsf_rx.c
 * before including the source. See `feed_byte` mock below.
 */

#include <assert.h>
#include <stdio.h>
#include <string.h>
#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>

/* === Stub Pico SDK headers used by crsf.c === */

/* Stub UART functions used by crsf.c. These are only called by crsf_init
 * and crsf_send, neither of which are exercised in this test. */
typedef int uart_inst_t;
#define uart0  ((uart_inst_t *)0)

#define GPIO_FUNC_UART 0
#define UART_PARITY_NONE 0

static void uart_init(uart_inst_t *u, unsigned baud) { (void)u; (void)baud; }
static void uart_set_format(uart_inst_t *u, int dbits, int sbits, int parity) {
    (void)u; (void)dbits; (void)sbits; (void)parity;
}
static void uart_set_fifo_enabled(uart_inst_t *u, bool en) { (void)u; (void)en; }
static void uart_write_blocking(uart_inst_t *u, const uint8_t *p, size_t n) {
    (void)u; (void)p; (void)n;
}
static void gpio_set_function(int gpio, int func) { (void)gpio; (void)func; }

/* Mock UART RX: tests push bytes into a queue, the source's
 * uart_is_readable / uart_getc drain them. */
static uint8_t  mock_rx_buf[256];
static size_t   mock_rx_len  = 0;
static size_t   mock_rx_head = 0;

static bool uart_is_readable(uart_inst_t *u) {
    (void)u;
    return mock_rx_head < mock_rx_len;
}
static char uart_getc(uart_inst_t *u) {
    (void)u;
    if (mock_rx_head >= mock_rx_len) return 0;
    return (char)mock_rx_buf[mock_rx_head++];
}
static void mock_rx_reset(void) { mock_rx_len = mock_rx_head = 0; }
static void mock_rx_push(uint8_t b) {
    if (mock_rx_len < sizeof(mock_rx_buf)) mock_rx_buf[mock_rx_len++] = b;
}
static void mock_rx_push_n(const uint8_t *src, size_t n) {
    for (size_t i = 0; i < n; i++) mock_rx_push(src[i]);
}

/* The header is normally produced by pico-sdk; provide a stub. */
#define PICO_STDLIB_H_  /* prevents source from re-including */

/* Now pull in the source under test. crsf.c includes "crsf.h" itself. */
#include "../src/crsf.c"

/* === Test scaffolding === */

static struct {
    int   calls;
    uint8_t addr;
    uint8_t type;
    uint8_t payload[64];
    size_t  payload_len;
} captured;

static void cap_cb(uint8_t addr, uint8_t type,
                   const uint8_t *payload, size_t payload_len) {
    captured.calls++;
    captured.addr = addr;
    captured.type = type;
    captured.payload_len = payload_len;
    if (payload_len <= sizeof(captured.payload)) {
        memcpy(captured.payload, payload, payload_len);
    }
}

static void capture_reset(void) {
    memset(&captured, 0, sizeof(captured));
    /* Reset the parser's static state too. */
    rx.state = RX_WAIT_ADDR;
    rx.got = 0;
}

/* Build a CRSF frame: [addr][len][type][payload...][crc]
 * `len` covers type+payload+crc. CRC is over [type..payload]. */
static size_t build_frame(uint8_t *out, uint8_t addr, uint8_t type,
                          const uint8_t *payload, size_t payload_len) {
    out[0] = addr;
    out[1] = (uint8_t)(payload_len + 2); /* type + payload + crc */
    out[2] = type;
    memcpy(&out[3], payload, payload_len);
    out[3 + payload_len] = crsf_crc8(&out[2], payload_len + 1);
    return payload_len + 4;
}

int main(void) {
    /* === Test 1: well-formed frame === */
    capture_reset();
    mock_rx_reset();
    uint8_t payload1[] = { 0x12, 0x34, 0x56, 0x78 };
    uint8_t frame1[16];
    size_t  len1 = build_frame(frame1, 0xEA, 0x14, payload1, sizeof payload1);
    mock_rx_push_n(frame1, len1);
    crsf_rx_poll(cap_cb);
    assert(captured.calls == 1);
    assert(captured.addr == 0xEA);
    assert(captured.type == 0x14);
    assert(captured.payload_len == sizeof payload1);
    assert(memcmp(captured.payload, payload1, sizeof payload1) == 0);
    printf("test 1 OK: well-formed frame parsed\n");

    /* === Test 2: bad CRC discarded === */
    capture_reset();
    mock_rx_reset();
    uint8_t frame2[16];
    size_t  len2 = build_frame(frame2, 0xEA, 0x14, payload1, sizeof payload1);
    frame2[len2 - 1] ^= 0xFF; /* corrupt CRC */
    mock_rx_push_n(frame2, len2);
    crsf_rx_poll(cap_cb);
    assert(captured.calls == 0);
    printf("test 2 OK: bad CRC silently discarded\n");

    /* === Test 3: garbage before a good frame; resync works === */
    capture_reset();
    mock_rx_reset();
    /* Push 5 bytes of garbage that aren't valid addr bytes. */
    uint8_t garbage[] = { 0x00, 0x55, 0xAA, 0xFF, 0x12 };
    mock_rx_push_n(garbage, sizeof garbage);
    /* Now a real frame. */
    uint8_t payload3[] = { 0x99 };
    uint8_t frame3[8];
    size_t  len3 = build_frame(frame3, 0xC8, 0x21, payload3, sizeof payload3);
    mock_rx_push_n(frame3, len3);
    crsf_rx_poll(cap_cb);
    assert(captured.calls == 1);
    assert(captured.addr == 0xC8);
    assert(captured.type == 0x21);
    assert(captured.payload_len == 1);
    assert(captured.payload[0] == 0x99);
    printf("test 3 OK: parser resyncs after garbage\n");

    /* === Test 4: invalid length byte triggers resync === */
    capture_reset();
    mock_rx_reset();
    /* 0xEA followed by len=0 (impossible): parser must skip. Then a
     * valid frame should still parse. */
    mock_rx_push(0xEA);
    mock_rx_push(0x00); /* invalid len */
    uint8_t frame4[8];
    size_t  len4 = build_frame(frame4, 0xEA, 0x08, payload3, sizeof payload3);
    mock_rx_push_n(frame4, len4);
    crsf_rx_poll(cap_cb);
    assert(captured.calls == 1);
    assert(captured.type == 0x08);
    printf("test 4 OK: invalid length resyncs cleanly\n");

    /* === Test 5: two frames back-to-back === */
    capture_reset();
    mock_rx_reset();
    uint8_t f5a[8], f5b[8];
    uint8_t pa[] = { 0x01, 0x02 };
    uint8_t pb[] = { 0x03, 0x04, 0x05 };
    size_t la = build_frame(f5a, 0xEA, 0x02, pa, sizeof pa);
    size_t lb = build_frame(f5b, 0xEA, 0x07, pb, sizeof pb);
    mock_rx_push_n(f5a, la);
    mock_rx_push_n(f5b, lb);
    crsf_rx_poll(cap_cb);
    assert(captured.calls == 2);
    /* The capture struct only holds the last; just check it's the second. */
    assert(captured.type == 0x07);
    assert(captured.payload_len == 3);
    printf("test 5 OK: back-to-back frames both parsed\n");

    printf("\nALL CRSF RX TESTS PASSED\n");
    return 0;
}
