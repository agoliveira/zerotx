/*
 * Momentary button input module for ZeroTX RP2040 firmware.
 *
 * Reads a panel-mount momentary push-button and emits a single
 * MSG_INPUT_EVENT frame on each stable press edge. Used to confirm
 * arming intent (the SH-equivalent in EdgeTX's standard arming
 * workflow: T-low + SF up + SH press -> armed).
 *
 * Wiring:
 *   GP15 with internal pull-up.
 *   Switch: one terminal to GP15, other terminal to GND.
 *   Switch open   (not pressed) -> pin reads HIGH
 *   Switch closed (pressed)     -> pin reads LOW
 *
 * The module emits an event on each stable press (debounced). Release
 * events are intentionally NOT emitted: a momentary's only meaningful
 * action is the press, and emitting release events would just spam the
 * IPC link with state=0 events the daemon ignores. Wire-encoded as
 * state=1 (press), consistent with the arm key's "active=1" convention.
 *
 * Debounce: 20ms time-based, mirroring the arm key. A change must
 * hold for the debounce interval before the press event fires.
 *
 * No boot announce. The resting state of a momentary carries no
 * meaning to the daemon (which only listens for press events to call
 * armMachine.Confirm). Holding the button down at boot results in no
 * event until the operator releases and re-presses.
 *
 * Pin assignment is on a free GPIO adjacent to the existing arm key
 * (GP14): both inputs share the same wiring polarity and pull-up
 * scheme, so a single panel cable can carry both signals plus GND.
 */
#ifndef ZEROTX_INPUT_MOMENTARY_H
#define ZEROTX_INPUT_MOMENTARY_H

#include <stdint.h>

#define INPUT_MOMENTARY_PIN  15u

void input_momentary_init(void);

/* Poll the momentary button. Call once per main loop. Internally
 * rate-limited so it's safe to invoke at the loop's natural cadence
 * (~kHz). Emits MSG_INPUT_EVENT via ipc_send when a stable press
 * edge is detected; release edges are silent.
 *
 * The seq parameter is the next IPC tx sequence number to use; the
 * caller's tx-seq counter is bumped if (and only if) this function
 * emitted a frame. The return value is the new seq value.
 */
uint8_t input_momentary_poll(uint64_t now_us, uint8_t seq);

#endif /* ZEROTX_INPUT_MOMENTARY_H */
