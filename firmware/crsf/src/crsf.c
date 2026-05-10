#include "crsf.h"

#include <string.h>

#include "pico/stdlib.h"
#include "hardware/uart.h"

/* UART0 on GPIO 0 (TX) / GPIO 1 (RX) */
#define CRSF_UART_ID    uart0
#define CRSF_UART_TX    0
#define CRSF_UART_RX    1

#define CRSF_ADDR_FC    0xC8u   /* Sync byte for radio->module RC frames */
#define CRSF_TYPE_RC    0x16u   /* RC_CHANNELS_PACKED */

/* CRC8 DVB-S2 lookup, poly 0xD5, init 0x00. */
static uint8_t crc8_table[256];
static bool crc8_table_init_done = false;

static void crc8_table_init(void) {
    for (int i = 0; i < 256; i++) {
        uint8_t crc = (uint8_t)i;
        for (int b = 0; b < 8; b++) {
            crc = (crc & 0x80) ? (uint8_t)((crc << 1) ^ 0xD5) : (uint8_t)(crc << 1);
        }
        crc8_table[i] = crc;
    }
    crc8_table_init_done = true;
}

uint8_t crsf_crc8(const uint8_t *data, size_t len) {
    if (!crc8_table_init_done) crc8_table_init();
    uint8_t c = 0;
    for (size_t i = 0; i < len; i++) c = crc8_table[c ^ data[i]];
    return c;
}

void crsf_init(unsigned int baud) {
    if (!crc8_table_init_done) crc8_table_init();
    uart_init(CRSF_UART_ID, baud);
    gpio_set_function(CRSF_UART_TX, GPIO_FUNC_UART);
    gpio_set_function(CRSF_UART_RX, GPIO_FUNC_UART);
    uart_set_format(CRSF_UART_ID, 8, 1, UART_PARITY_NONE);
    uart_set_fifo_enabled(CRSF_UART_ID, true);
}

/*
 * Pack 16 11-bit channels into 22 bytes (LSB-first across the stream).
 * Bit 0 of channel 0 is bit 0 of out byte 0.
 */
static void pack11(const uint16_t ch[16], uint8_t out[22]) {
    memset(out, 0, 22);
    uint32_t bitpos = 0;
    for (int i = 0; i < 16; i++) {
        uint32_t v = (uint32_t)(ch[i] & 0x07FF);
        uint32_t byte = bitpos >> 3;
        uint32_t shift = bitpos & 7u;
        out[byte]     |= (uint8_t)(v << shift);
        out[byte + 1] |= (uint8_t)(v >> (8u - shift));
        if (shift > 5u) out[byte + 2] |= (uint8_t)(v >> (16u - shift));
        bitpos += 11;
    }
}

void crsf_pack_rc(const uint16_t channels[16], uint8_t out[CRSF_FRAME_RC_LEN]) {
    out[0] = CRSF_ADDR_FC;
    out[1] = 24u;             /* type + 22 payload + crc */
    out[2] = CRSF_TYPE_RC;
    pack11(channels, &out[3]);
    /* CRC over [type..payload], i.e. out[2..2+23] inclusive = out[2..24] (length 23). */
    out[25] = crsf_crc8(&out[2], 23);
}

void crsf_send(const uint8_t *frame, size_t len) {
    uart_write_blocking(CRSF_UART_ID, frame, len);
}

/* === RX-side CRSF parser ===
 *
 * Telemetry frames arrive from the ELRS module on the same UART. They
 * follow the CRSF frame structure but are addressed to the radio
 * (0xEA) rather than the FC (0xC8). We don't filter by address; we
 * parse any well-formed frame and forward it to the callback.
 *
 * Frame layout:
 *   [addr:1][len:1][type:1][payload:len-2][crc:1]
 *
 * `len` covers everything from `type` through `crc` inclusive (so the
 * payload is `len - 2` bytes). CRC is computed over `[type..payload]`,
 * i.e. the first `len - 1` bytes after the length byte.
 *
 * CRSF frames can be up to 64 bytes in total. We use a 64-byte buffer
 * and resync whenever we see invalid framing or a bad CRC.
 */

typedef enum {
    RX_WAIT_ADDR = 0,
    RX_WAIT_LEN,
    RX_WAIT_PAYLOAD,
} rx_state_t;

static struct {
    rx_state_t state;
    uint8_t buf[CRSF_RX_MAX];
    uint8_t addr;
    uint8_t len;       /* value of the length byte */
    uint8_t got;       /* bytes collected for the current payload+crc */
} rx;

/* Reasonable list of CRSF source-address bytes we expect. Anything else
 * is treated as garbage and we resync. The two we care about are 0xEA
 * (radio dest) and 0xC8 (FC dest); some firmwares put the FC address
 * here on broadcast frames. We accept either to be liberal. */
static bool crsf_addr_ok(uint8_t a) {
    return (a == 0xEAu) || (a == 0xC8u);
}

void crsf_rx_poll(crsf_rx_cb_t cb) {
    /* Drain in a tight loop, but cap iterations so a flood of bytes
     * can't starve the rest of the main loop. */
    for (int safety = 0; safety < 256; safety++) {
        if (!uart_is_readable(CRSF_UART_ID)) return;
        uint8_t b = (uint8_t)uart_getc(CRSF_UART_ID);

        switch (rx.state) {
        case RX_WAIT_ADDR:
            if (crsf_addr_ok(b)) {
                rx.addr = b;
                rx.state = RX_WAIT_LEN;
            }
            /* else: drop and stay in WAIT_ADDR until we sync. */
            break;

        case RX_WAIT_LEN:
            /* `len` covers type+payload+crc. Minimum useful frame is
             * type+crc = 2 bytes. Maximum we accept is the buffer size
             * minus the address byte we already consumed. */
            if (b < 2 || b > (CRSF_RX_MAX - 2)) {
                rx.state = RX_WAIT_ADDR;
                break;
            }
            rx.len = b;
            rx.got = 0;
            rx.state = RX_WAIT_PAYLOAD;
            break;

        case RX_WAIT_PAYLOAD:
            rx.buf[rx.got++] = b;
            if (rx.got == rx.len) {
                /* Complete frame. Validate CRC. */
                /* CRC is the last byte; covers everything from type
                 * (rx.buf[0]) through the byte before CRC, i.e.
                 * the first len-1 bytes. */
                uint8_t expected = rx.buf[rx.len - 1];
                uint8_t actual = crsf_crc8(rx.buf, rx.len - 1);
                if (expected == actual) {
                    uint8_t type = rx.buf[0];
                    const uint8_t *payload = &rx.buf[1];
                    size_t payload_len = rx.len - 2;
                    if (cb) cb(rx.addr, type, payload, payload_len);
                }
                /* Whether OK or bad, we go back to looking for a
                 * fresh frame. Resync is automatic since we look for
                 * the address byte. */
                rx.state = RX_WAIT_ADDR;
            }
            break;
        }
    }
}
