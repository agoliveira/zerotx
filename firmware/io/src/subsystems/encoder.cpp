// encoder.cpp - rotary encoder subsystem.

#include "encoder.h"

#include <stdio.h>
#include <string.h>

#include "../hal.h"

namespace zerotx {

// Quadrature decode lookup table.
//
// State is (A << 1) | B, with A and B being 0 or 1 (LOW/HIGH).
// Index = (prev_state << 2) | curr_state, 16 entries.
//
// Values: +1 = CW, -1 = CCW, 0 = no transition or invalid.
//
// Invalid transitions (e.g., 00 -> 11, where both inputs change at
// once) are rejected silently. With polling at the loop rate of a
// 16MHz AVR, valid transitions never appear as invalid, so this
// rejection only filters bounce.
static const int8_t kQuadTable[16] = {
   0, -1,  1,  0,
   1,  0,  0, -1,
  -1,  0,  0,  1,
   0,  1, -1,  0
};

static inline uint8_t readState(uint8_t a_pin, uint8_t b_pin) {
  return (uint8_t)((digitalRead(a_pin) << 1) | digitalRead(b_pin));
}

void Encoder::begin(Stream& out) {
  a_pin_  = hal::pin(hal::HAL_ENC0_A);
  b_pin_  = hal::pin(hal::HAL_ENC0_B);
  sw_pin_ = hal::pin(hal::HAL_ENC0_SW);

  pinMode(a_pin_,  INPUT_PULLUP);
  pinMode(b_pin_,  INPUT_PULLUP);
  pinMode(sw_pin_, INPUT_PULLUP);

  prev_state_ = readState(a_pin_, b_pin_);
  accum_      = 0;

  btn_raw_           = digitalRead(sw_pin_) == HIGH;
  btn_stable_        = btn_raw_;
  btn_last_change_ms = millis();

  proto::writeEvent(out, "enc.0", "ready");
}

void Encoder::tick(uint32_t now_ms, Stream& out) {
  // Quadrature sampling: read A and B, decode against previous state.
  uint8_t curr = readState(a_pin_, b_pin_);
  if (curr != prev_state_) {
    int8_t step = kQuadTable[(prev_state_ << 2) | curr];
    accum_ += step;
    prev_state_ = curr;

    // Emit one detent's worth of motion at a time. With kTPD = 4
    // and a clean encoder we'll hit +/-4 exactly on each click.
    while (accum_ >= kTransitionsPerDetent) {
      proto::writeEvent(out, "enc.0", "cw");
      accum_ -= kTransitionsPerDetent;
    }
    while (accum_ <= -kTransitionsPerDetent) {
      proto::writeEvent(out, "enc.0", "ccw");
      accum_ += kTransitionsPerDetent;
    }
  }

  // Button debounce - same pattern as the button subsystem.
  bool sw_now = digitalRead(sw_pin_) == HIGH;  // true = released
  if (sw_now != btn_raw_) {
    btn_raw_ = sw_now;
    btn_last_change_ms = now_ms;
  }
  if (btn_raw_ != btn_stable_ && (now_ms - btn_last_change_ms) >= kButtonDebounceMs) {
    btn_stable_ = btn_raw_;
    proto::writeEvent(out, "enc.0", btn_stable_ ? "release" : "press");
  }
}

bool Encoder::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  if (instance != 0) return false;
  const char* verb = cmd.verb();
  if (!verb) return false;

  if (strcmp(verb, "GET") == 0) {
    char body[32];
    snprintf(body, sizeof(body), "enc.0 button=%s",
             btn_stable_ ? "released" : "pressed");
    proto::writeResponse(out, body);
    return true;
  }

  return false;
}

}  // namespace zerotx
