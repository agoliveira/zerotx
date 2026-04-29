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
