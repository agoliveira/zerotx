#ifndef ZEROTX_STATUS_LED_H
#define ZEROTX_STATUS_LED_H

#include <stdint.h>
#include "state.h"

/* RP2040-Zero has its onboard WS2812 wired to GPIO16. */
#define STATUS_LED_PIN  16u

void status_led_init(void);

/* Update LED color/pattern based on current link state. Call periodically. */
void status_led_update(link_state_t s, uint64_t now_us);

/* Direct color set for tests. r,g,b each 0..255. */
void status_led_set(uint8_t r, uint8_t g, uint8_t b);

#endif /* ZEROTX_STATUS_LED_H */
