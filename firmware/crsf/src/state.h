#ifndef ZEROTX_STATE_H
#define ZEROTX_STATE_H

#include <stdint.h>
#include <stdbool.h>
#include "protocol.h"

typedef enum {
    LINK_BOOT = 0,      /* Just powered up, USB not enumerated */
    LINK_PENDING,       /* USB up, no Pi heartbeat seen yet */
    LINK_OK,            /* Pi heartbeat fresh, normal operation */
    LINK_HOLD,          /* Heartbeat lost, holding last channels briefly */
    LINK_FAILSAFE       /* HOLD expired, CRSF emission stopped */
} link_state_t;

void state_init(void);

/* Latest channel intent from Pi. Held in HOLD; ignored in FAILSAFE. */
void state_set_channels(const uint16_t channels[ZTX_CHANNELS]);
void state_get_channels(uint16_t out[ZTX_CHANNELS]);

/* Single-slot accessors used by arm_override to modify the
 * outbound buffer without overwriting the daemon's other channels.
 * Idx is clamped: out-of-range writes are silently dropped, reads
 * return 0. Both are safe in single-core firmware where the only
 * other writer is the Pi heartbeat handler (state_set_channels)
 * which runs on the same main-loop thread. */
void state_set_channel(uint8_t idx, uint16_t value);
uint16_t state_get_channel(uint8_t idx);

/* Update on Pi-side heartbeat reception. */
void state_note_heartbeat(uint64_t now_us);

/* Mark USB enumerated -> exit LINK_BOOT. */
void state_note_usb_ready(uint64_t now_us);

/* Drive state transitions based on time. Call frequently. */
void state_tick(uint64_t now_us);

link_state_t state_current(void);

/* Should the main loop emit CRSF this cycle? */
bool state_should_emit_crsf(void);

#endif /* ZEROTX_STATE_H */
