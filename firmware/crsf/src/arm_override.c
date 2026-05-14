/*
 * arm_override.c - see arm_override.h for the contract.
 */
#include "arm_override.h"

#include "ipc.h"
#include "protocol.h"
#include "state.h"

/* TAER-layout compile-time defaults. Match the daemon-side
 * armConfigFromModel(nil) so a daemon that never pushes config and
 * a firmware that never receives one converge on the same behavior.
 *   thr_idx=0   throttle is channel 1 in TAER (T-A-E-R)
 *   arm_idx=4   arm switch on channel 5 (operator's "channel 5",
 *               protocol-side 0-indexed = 4)
 *   thr_threshold=200  matches daemon's ch[thrIdx] <= 200 check
 *   arm_disarm_value=ZTX_CRSF_CH_MIN (172)  bottom of CRSF range,
 *               same value the daemon's mapper emits for DISARMED. */
static struct {
    uint8_t  thr_idx;
    uint8_t  arm_idx;
    uint16_t thr_threshold;
    uint16_t arm_disarm_value;
    bool     configured;   /* set true on first successful set_config;
                              for diagnostics in logs only -- behavior
                              is identical whether configured or not. */
} s = {
    .thr_idx          = 0,
    .arm_idx          = 4,
    .thr_threshold    = 200,
    .arm_disarm_value = ZTX_CRSF_CH_MIN,
    .configured       = false,
};

void arm_override_init(void) {
    s.thr_idx          = 0;
    s.arm_idx          = 4;
    s.thr_threshold    = 200;
    s.arm_disarm_value = ZTX_CRSF_CH_MIN;
    s.configured       = false;
}

bool arm_override_set_config(uint8_t thr_idx,
                             uint8_t arm_idx,
                             uint16_t thr_threshold,
                             uint16_t arm_disarm_value) {
    if (thr_idx >= ZTX_CHANNELS || arm_idx >= ZTX_CHANNELS) {
        ipc_log("arm_override: rejected config (thr=%u arm=%u out of range)",
                (unsigned)thr_idx, (unsigned)arm_idx);
        return false;
    }
    s.thr_idx          = thr_idx;
    s.arm_idx          = arm_idx;
    s.thr_threshold    = thr_threshold;
    s.arm_disarm_value = arm_disarm_value;
    s.configured       = true;
    ipc_log("arm_override: config thr_idx=%u arm_idx=%u thr_threshold=%u arm_disarm_value=%u",
            (unsigned)thr_idx, (unsigned)arm_idx,
            (unsigned)thr_threshold, (unsigned)arm_disarm_value);
    return true;
}

void arm_override_on_arm_key_edge(bool key_up) {
    /* Only the high->low (disarm-intent) edge does anything. The
     * low->high edge is the operator requesting an arm, which is
     * the daemon's three-input gate to decide. */
    if (key_up) return;

    uint16_t throttle = state_get_channel(s.thr_idx);
    if (throttle > s.thr_threshold) {
        /* In-flight disarm refused. Daemon's arm machine sees the
         * same SF edge via MSG_INPUT_EVENT and will emit
         * EventDisarmDeniedInFlight, which fires the audio cue
         * and panel alarm overlay. Nothing for us to do here -- we
         * just don't touch the channel. */
        ipc_log("arm_override: disarm refused (throttle=%u > %u)",
                (unsigned)throttle, (unsigned)s.thr_threshold);
        return;
    }

    /* Throttle is low. Cut the arm channel directly. This is the
     * defense-in-depth bit: even if the daemon is hung and never
     * updates s_channels, the FC will see the arm channel drop on
     * the next CRSF emit (within ZTX_CRSF_PERIOD_MS = 20ms). */
    state_set_channel(s.arm_idx, s.arm_disarm_value);
    ipc_log("arm_override: disarmed (throttle=%u <= %u, ch[%u]=%u)",
            (unsigned)throttle, (unsigned)s.thr_threshold,
            (unsigned)s.arm_idx, (unsigned)s.arm_disarm_value);
}
