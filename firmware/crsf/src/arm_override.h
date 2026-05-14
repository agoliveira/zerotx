/*
 * arm_override.h - firmware-level defense-in-depth disarm.
 *
 * When the operator flips the ARM switch from up to down with throttle
 * at or below the configured threshold, this module zeroes the arm
 * channel directly in the outbound CRSF buffer -- bypassing the daemon.
 * If throttle is above threshold the override does nothing and the
 * daemon (assumed alive and well) handles refusal via its arm state
 * machine, which emits EventDisarmDeniedInFlight to surface audio +
 * panel feedback.
 *
 * Purpose: keep the disarm safety property correct even if the daemon
 * is hung, deadlocked, or otherwise unable to react. Not a primary
 * mechanism -- the daemon-side arm machine in
 * pi/daemon/internal/arm/arm.go enforces the same policy at a higher
 * layer.
 *
 * Channel slot indices and the throttle threshold are pushed by the
 * daemon at link open via MSG_ARM_CONFIG. Before that message arrives
 * (or if the daemon never sends one), TAER defaults are used --
 * matching the project's primary aircraft (Big Talon).
 *
 * Config flow:
 *   daemon (model.EdgeTXModel.ThrottleChannel() resolves thr_idx)
 *     -> MSG_ARM_CONFIG (6 bytes: thr_idx, arm_idx, thr_threshold LE,
 *                        arm_disarm_value LE)
 *     -> arm_override_set_config()
 *
 * Hot path (input_arm sees a stable high->low edge on the ARM pin):
 *   input_arm.c -> arm_override_on_arm_key_edge(false)
 *     -> reads state_get_channel(thr_idx)
 *     -> if <= thr_threshold: state_set_channel(arm_idx, arm_disarm_value)
 *
 * Threading: single-core firmware. All entry points are called from
 * the main poll loop. No locks.
 */
#ifndef ZEROTX_ARM_OVERRIDE_H
#define ZEROTX_ARM_OVERRIDE_H

#include <stdint.h>
#include <stdbool.h>

/* Reset to compile-time defaults (TAER: thr_idx=0, arm_idx=4,
 * thr_threshold=200, arm_disarm_value=172). Safe to call repeatedly. */
void arm_override_init(void);

/* Apply a new config from MSG_ARM_CONFIG. Returns true on accept.
 * Returns false (config unchanged) on bad indices; the firmware
 * keeps whatever it had. The daemon side range-checks too, so
 * receiving an out-of-range value means the wire got corrupted or
 * a future daemon sent a value this firmware doesn't understand. */
bool arm_override_set_config(uint8_t thr_idx,
                             uint8_t arm_idx,
                             uint16_t thr_threshold,
                             uint16_t arm_disarm_value);

/* Notification hook called by input_arm.c on every stable arm-key
 * edge transition. key_up is the same logical state that goes out
 * over MSG_INPUT_EVENT to the daemon (true == switch in up position
 * == arm-requested). Only the high->low transition triggers the
 * override; the up direction is a no-op here (arming is the
 * daemon's call, three-input gate). */
void arm_override_on_arm_key_edge(bool key_up);

#endif /* ZEROTX_ARM_OVERRIDE_H */
