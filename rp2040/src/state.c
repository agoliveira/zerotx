#include "state.h"

#include <string.h>

#include "ipc.h"

static link_state_t s_state;
static uint64_t     s_last_heartbeat_us;
static uint64_t     s_state_entered_us;
static uint16_t     s_channels[ZTX_CHANNELS];
static bool         s_usb_ready;

static void enter(link_state_t next, uint64_t now_us, const char *reason) {
    if (s_state == next) return;
    link_state_t prev = s_state;
    s_state = next;
    s_state_entered_us = now_us;
    ipc_log("state: %d -> %d (%s)", (int)prev, (int)next, reason);
}

void state_init(void) {
    s_state = LINK_BOOT;
    s_last_heartbeat_us = 0;
    s_state_entered_us = 0;
    s_usb_ready = false;
    /* Default channels: sticks centered, throttle low, arm low. */
    for (int i = 0; i < ZTX_CHANNELS; i++) s_channels[i] = ZTX_CRSF_CH_MID;
    s_channels[2] = ZTX_CRSF_CH_MIN; /* assume CH3 = throttle, low */
    s_channels[4] = ZTX_CRSF_CH_MIN; /* assume CH5 = arm, low */
}

void state_set_channels(const uint16_t channels[ZTX_CHANNELS]) {
    memcpy(s_channels, channels, sizeof(s_channels));
}

void state_get_channels(uint16_t out[ZTX_CHANNELS]) {
    memcpy(out, s_channels, sizeof(s_channels));
}

void state_note_heartbeat(uint64_t now_us) {
    s_last_heartbeat_us = now_us;
    if (s_state == LINK_PENDING || s_state == LINK_HOLD || s_state == LINK_FAILSAFE) {
        enter(LINK_OK, now_us, "heartbeat received");
    }
}

void state_note_usb_ready(uint64_t now_us) {
    s_usb_ready = true;
    if (s_state == LINK_BOOT) enter(LINK_PENDING, now_us, "usb ready");
}

void state_tick(uint64_t now_us) {
    if (s_state == LINK_BOOT) {
        if (s_usb_ready) enter(LINK_PENDING, now_us, "usb ready");
        return;
    }

    /* Time since last Pi heartbeat */
    uint64_t hb_age_us = (s_last_heartbeat_us == 0)
                         ? UINT64_MAX
                         : (now_us - s_last_heartbeat_us);

    switch (s_state) {
    case LINK_PENDING:
        /* Stays here until first heartbeat arrives. No emission. */
        break;
    case LINK_OK:
        if (hb_age_us > (uint64_t)ZTX_HEARTBEAT_RX_TIMEOUT_MS * 1000u) {
            enter(LINK_HOLD, now_us, "heartbeat timeout");
        }
        break;
    case LINK_HOLD: {
        uint64_t held_us = now_us - s_state_entered_us;
        if (held_us > (uint64_t)ZTX_HOLD_MS * 1000u) {
            enter(LINK_FAILSAFE, now_us, "hold expired");
        }
        break;
    }
    case LINK_FAILSAFE:
        /* Exit only via heartbeat (handled in state_note_heartbeat). */
        break;
    default: break;
    }
}

link_state_t state_current(void) { return s_state; }

bool state_should_emit_crsf(void) {
    /* Emit in LINK_OK and LINK_HOLD only. */
    return s_state == LINK_OK || s_state == LINK_HOLD;
}
