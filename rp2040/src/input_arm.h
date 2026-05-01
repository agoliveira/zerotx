/*
 * Arm key input module for ZeroTX RP2040 firmware.
 *
 * Reads a single GPIO wired to the panel's guarded arm switch and
 * emits MSG_INPUT_EVENT frames over IPC on stable edge transitions.
 *
 * Wiring:
 *   GP14 with internal pull-up.
 *   Switch: one terminal to GP14, other terminal to GND.
 *   Switch closed (key UP)   -> pin reads LOW
 *   Switch open   (key DOWN) -> pin reads HIGH
 *
 * The module translates the wiring polarity into the protocol's
 * polarity convention: state=0 for DOWN, state=1 for UP. So the
 * daemon's arm state machine sees "logical key state" regardless of
 * how the switch is wired.
 *
 * Debounce: 20ms time-based. A change must hold for the debounce
 * interval before an event fires. Initial boot state is captured
 * after the first stable read and emitted once so the daemon has
 * ground truth without operator action.
 *
 * Pin assignment is intentionally on a free GPIO with no neighbours
 * doing anything timing-sensitive: GP14 is far from UART (GP0/GP1)
 * and from the onboard WS2812 (GP16).
 */
#ifndef ZEROTX_INPUT_ARM_H
#define ZEROTX_INPUT_ARM_H

#include <stdint.h>

#define INPUT_ARM_PIN  14u

void input_arm_init(void);

/* Poll the arm key. Call once per main loop. Internally rate-limited
 * so it's safe to invoke at the loop's natural cadence (~kHz). Emits
 * MSG_INPUT_EVENT via ipc_send when a stable edge is detected.
 *
 * On the very first call after boot this emits one event reflecting
 * the initial stable state (after the debounce hold time elapses).
 *
 * The seq parameter is the next IPC tx sequence number to use; the
 * caller's tx-seq counter is bumped if (and only if) this function
 * emitted a frame. The return value is the new seq value.
 */
uint8_t input_arm_poll(uint64_t now_us, uint8_t seq);

#endif /* ZEROTX_INPUT_ARM_H */
