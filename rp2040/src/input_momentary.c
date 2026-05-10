/*
 * Momentary button input - GPIO read with debounce, IPC emission on
 * each stable press edge. See input_momentary.h for the contract.
 */
#include "input_momentary.h"

#include "pico/stdlib.h"
#include "hardware/gpio.h"

#include "protocol.h"
#include "ipc.h"

#define DEBOUNCE_US  20000u  /* 20ms */

/* Module state. Single-instance; the momentary button is one switch. */
static struct {
    bool      raw_last;       /* last raw pin read (logical: true = pressed) */
    bool      stable;         /* current debounced state (true = pressed) */
    uint64_t  changed_at_us;  /* last raw-edge time */
} s;

void input_momentary_init(void) {
    gpio_init(INPUT_MOMENTARY_PIN);
    gpio_set_dir(INPUT_MOMENTARY_PIN, GPIO_IN);
    gpio_pull_up(INPUT_MOMENTARY_PIN);

    /* Pin is active-low (switch to ground): pin LOW means pressed. */
    bool initial_pressed = (gpio_get(INPUT_MOMENTARY_PIN) == 0);
    s.raw_last      = initial_pressed;
    s.stable        = initial_pressed;
    s.changed_at_us = 0;
}

uint8_t input_momentary_poll(uint64_t now_us, uint8_t seq) {
    bool raw_pressed = (gpio_get(INPUT_MOMENTARY_PIN) == 0);

    /* Detect raw-edge: arm the debounce timer. */
    if (raw_pressed != s.raw_last) {
        s.raw_last      = raw_pressed;
        s.changed_at_us = now_us;
        return seq;
    }

    /* If raw == stable, no transition pending. */
    if (raw_pressed == s.stable) {
        return seq;
    }

    /* Wait for raw to hold steady for the debounce interval. */
    if ((now_us - s.changed_at_us) < DEBOUNCE_US) {
        return seq;
    }

    /* Stable change confirmed. Update state but only EMIT on the
     * pressed edge (false -> true). Release events are silently
     * absorbed: the daemon doesn't care about them, and emitting
     * would just spam the IPC link with state=0 frames it ignores. */
    bool pressed_edge = !s.stable && raw_pressed;
    s.stable = raw_pressed;
    if (!pressed_edge) {
        return seq;
    }

    uint8_t payload[2];
    payload[0] = ZTX_INPUT_MOMENTARY;
    payload[1] = 1u;  /* press; release edges aren't emitted */
    ipc_send(MSG_INPUT_EVENT, seq, payload, 2);
    return (uint8_t)(seq + 1u);
}
