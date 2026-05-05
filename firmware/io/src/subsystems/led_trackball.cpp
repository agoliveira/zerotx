// led_trackball.cpp - trackball status LED subsystem implementation.

#include "led_trackball.h"

#include <string.h>

#include "../hal.h"

namespace zerotx {

// Default polarity: HIGH = LED lit. The HAL ACTIVE_LOW flag flips
// this per-pin for boards wired through an inverting transistor
// stage (NPN switching the LED cathode to GND, etc.). The level
// helpers in led_trackball.h consult the captured per-pin polarity.

void LedTrackball::begin(Stream& out) {
  green_pin_        = hal::pin(hal::HAL_LED_TRACKBALL_GREEN);
  red_pin_          = hal::pin(hal::HAL_LED_TRACKBALL_RED);
  green_active_low_ = hal::activeLow(hal::HAL_LED_TRACKBALL_GREEN);
  red_active_low_   = hal::activeLow(hal::HAL_LED_TRACKBALL_RED);
  pinMode(green_pin_, OUTPUT);
  pinMode(red_pin_, OUTPUT);
  digitalWrite(green_pin_, greenOffLevel());
  digitalWrite(red_pin_, redOffLevel());
  state_ = State::Off;
  proto::writeEvent(out, "led.trackball", "ready");
}

void LedTrackball::tick(uint32_t now_ms, Stream& out) {
  (void)out;
  render(now_ms);
}

void LedTrackball::render(uint32_t now_ms) {
  switch (state_) {
    case State::Off:
      digitalWrite(green_pin_, greenOffLevel());
      digitalWrite(red_pin_,   redOffLevel());
      break;

    case State::GreenSolid:
      digitalWrite(green_pin_, greenOnLevel());
      digitalWrite(red_pin_,   redOffLevel());
      break;

    case State::GreenPulse: {
      // Soft breathing via duty-cycle PWM. We don't have analogWrite
      // on every pin (depends on hardware timer), so emulate with a
      // ~50Hz time-sliced PWM driven by now_ms. ~2s cycle, sinusoidal
      // brightness: phase 0..2000ms maps to 0..1 sin lobe.
      uint16_t phase = now_ms % 2000;
      // Triangle approximation of sin to avoid float math; close
      // enough for a status LED.
      uint8_t duty = (phase < 1000)
        ? (uint8_t)(phase * 255UL / 1000)
        : (uint8_t)((2000 - phase) * 255UL / 1000);
      uint16_t cycle = now_ms % 20;  // 50Hz at 20ms period
      bool on = (cycle * 255 / 20) < duty;
      digitalWrite(green_pin_, on ? greenOnLevel() : greenOffLevel());
      digitalWrite(red_pin_,   redOffLevel());
      break;
    }

    case State::RedSolid:
      digitalWrite(green_pin_, greenOffLevel());
      digitalWrite(red_pin_,   redOnLevel());
      break;

    case State::RedBlink: {
      // ~3Hz blink, 50% duty.
      bool on = (now_ms / 150) % 2 == 0;
      digitalWrite(green_pin_, greenOffLevel());
      digitalWrite(red_pin_,   on ? redOnLevel() : redOffLevel());
      break;
    }
  }
}

bool LedTrackball::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  // Single-instance subsystem; instance is always 0.
  (void)instance;

  const char* verb = cmd.verb();
  if (!verb) return false;

  if (strcmp(verb, "SET") == 0) {
    // Param slot holds the state name in this single-instance shape:
    //   SET led.trackball <state>
    // (no separate "param" word; the value sits at param() position).
    const char* state_str = cmd.param();
    if (!state_str) {
      proto::writeError(out, "led.trackball", "missing-state");
      return true;
    }
    State s;
    if (!parseState(state_str, s)) {
      proto::writeError(out, "led.trackball", "invalid-state");
      return true;
    }
    state_ = s;
    return true;
  }

  if (strcmp(verb, "GET") == 0) {
    char body[40];
    snprintf(body, sizeof(body), "led.trackball %s", stateName(state_));
    proto::writeResponse(out, body);
    return true;
  }

  return false;
}

bool LedTrackball::parseState(const char* s, State& out) {
  if (!s) return false;
  if (strcmp(s, "off")          == 0) { out = State::Off;         return true; }
  if (strcmp(s, "green-solid")  == 0) { out = State::GreenSolid;  return true; }
  if (strcmp(s, "green-pulse")  == 0) { out = State::GreenPulse;  return true; }
  if (strcmp(s, "red-solid")    == 0) { out = State::RedSolid;    return true; }
  if (strcmp(s, "red-blink")    == 0) { out = State::RedBlink;    return true; }
  return false;
}

const char* LedTrackball::stateName(State s) {
  switch (s) {
    case State::Off:         return "off";
    case State::GreenSolid:  return "green-solid";
    case State::GreenPulse:  return "green-pulse";
    case State::RedSolid:    return "red-solid";
    case State::RedBlink:    return "red-blink";
  }
  return "unknown";
}

}  // namespace zerotx
