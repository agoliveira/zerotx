#include "status_led.h"

#include "pico/stdlib.h"
#include "hardware/pio.h"
#include "ws2812.pio.h"

#define WS2812_FREQ_HZ      800000.0f
#define WS2812_PIO          pio0
#define WS2812_SM           0

static uint s_led_offset;

/* WS2812 (NeoPixel) wants GRB packed in the high 24 bits of a 32-bit word. */
static inline uint32_t pack_grb(uint8_t r, uint8_t g, uint8_t b) {
    return ((uint32_t)g << 24) | ((uint32_t)r << 16) | ((uint32_t)b << 8);
}

void status_led_init(void) {
    s_led_offset = pio_add_program(WS2812_PIO, &ws2812_program);
    ws2812_program_init(WS2812_PIO, WS2812_SM, s_led_offset, STATUS_LED_PIN, WS2812_FREQ_HZ, false);
    status_led_set(0, 0, 0);
}

void status_led_set(uint8_t r, uint8_t g, uint8_t b) {
    pio_sm_put_blocking(WS2812_PIO, WS2812_SM, pack_grb(r, g, b));
}

/* Trim brightness so the on-board LED is bearable to look at. */
static inline uint8_t dim(uint8_t v) { return (uint8_t)((v * 24u) / 255u); }

void status_led_update(link_state_t s, uint64_t now_us) {
    /* Patterns:
     *   BOOT      : white slow pulse
     *   PENDING   : amber solid
     *   OK        : green solid
     *   HOLD      : amber rapid blink
     *   FAILSAFE  : red rapid blink
     */
    uint64_t now_ms = now_us / 1000u;
    bool blink_fast = (now_ms / 100u) & 1u;
    /* Slow pulse 0..255..0 over ~2s */
    uint8_t pulse = (uint8_t)((now_ms % 2000u) < 1000u ? (now_ms % 1000u) / 4u : (1000u - (now_ms % 1000u)) / 4u);

    switch (s) {
    case LINK_BOOT:
        status_led_set(dim(pulse), dim(pulse), dim(pulse));
        break;
    case LINK_PENDING:
        status_led_set(dim(255), dim(160), 0);
        break;
    case LINK_OK:
        status_led_set(0, dim(255), 0);
        break;
    case LINK_HOLD:
        if (blink_fast) status_led_set(dim(255), dim(160), 0);
        else            status_led_set(0, 0, 0);
        break;
    case LINK_FAILSAFE:
        if (blink_fast) status_led_set(dim(255), 0, 0);
        else            status_led_set(0, 0, 0);
        break;
    default:
        status_led_set(0, 0, 0);
        break;
    }
}
