/*
 * Arm key input - GPIO read with debounce, IPC emission on edge.
 * See input_arm.h for the contract.
 */
#include "input_arm.h"

#include "pico/stdlib.h"
#include "hardware/gpio.h"

#include "protocol.h"
#include "ipc.h"

#define DEBOUNCE_US  20000u  /* 20ms */

/* Module state. Single-instance; the arm key is one switch globally. */
static struct {
    bool      raw_last;       /* last raw pin read (logical: true = UP) */
    bool      stable;         /* current debounced state */
    uint64_t  changed_at_us;  /* last raw-edge time */
    bool      announced;      /* have we sent the initial state event? */
} s;

void input_arm_init(void) {
    gpio_init(INPUT_ARM_PIN);
    gpio_set_dir(INPUT_ARM_PIN, GPIO_IN);
    gpio_pull_up(INPUT_ARM_PIN);

    /* Pin is active-low (switch to ground): pin LOW means key UP. */
    bool initial_up = (gpio_get(INPUT_ARM_PIN) == 0);
    s.raw_last      = initial_up;
    s.stable        = initial_up;
    s.changed_at_us = 0;
    s.announced     = false;
}

uint8_t input_arm_poll(uint64_t now_us, uint8_t seq) {
    bool raw_up = (gpio_get(INPUT_ARM_PIN) == 0);

    /* Detect raw-edge: arm the debounce timer. */
    if (raw_up != s.raw_last) {
        s.raw_last      = raw_up;
        s.changed_at_us = now_us;
        return seq;
    }

    /* If raw == stable, nothing to do (apart from the boot-announce
     * case below). */
    if (raw_up == s.stable && s.announced) {
        return seq;
    }

    /* Wait for raw to hold steady for the debounce interval. */
    if ((now_us - s.changed_at_us) < DEBOUNCE_US) {
        return seq;
    }

    /* Stable change confirmed (or boot-time first announce). */
    s.stable = raw_up;
    uint8_t payload[2];
    payload[0] = ZTX_INPUT_ARM_KEY;
    payload[1] = raw_up ? 1u : 0u;
    ipc_send(MSG_INPUT_EVENT, seq, payload, 2);
    s.announced = true;
    return (uint8_t)(seq + 1u);
}
