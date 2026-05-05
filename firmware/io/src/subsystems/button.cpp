// button.cpp - polling-based debounced button input.

#include "button.h"

#include <stdio.h>

#include "../hal.h"

namespace zerotx {

// Map instance index to its HAL pin id. Buttons 0-4 -> HAL_BUTTON_0..4.
static hal::HalPinId pinIdFor(uint8_t instance) {
  return static_cast<hal::HalPinId>(hal::HAL_BUTTON_0 + instance);
}

void Button::begin(Stream& out) {
  (void)out;
  for (uint8_t i = 0; i < kCount; ++i) {
    slots_[i].pin = hal::pin(pinIdFor(i));
    pinMode(slots_[i].pin, INPUT_PULLUP);
    // Initial read; INPUT_PULLUP idles HIGH = released.
    bool released = digitalRead(slots_[i].pin) == HIGH;
    slots_[i].raw    = released;
    slots_[i].stable = released;
    slots_[i].last_change_ms = millis();
  }
}

void Button::tick(uint32_t now_ms, Stream& out) {
  for (uint8_t i = 0; i < kCount; ++i) {
    Slot& s = slots_[i];
    bool current = digitalRead(s.pin) == HIGH;  // true = released
    if (current != s.raw) {
      s.raw = current;
      s.last_change_ms = now_ms;
    }
    // If the raw value has differed from stable for at least
    // kDebounceMs, accept it as the new stable state and emit edge.
    if (s.raw != s.stable && (now_ms - s.last_change_ms) >= kDebounceMs) {
      s.stable = s.raw;
      char target[12];
      snprintf(target, sizeof(target), "button.%u", (unsigned)i);
      // released = HIGH = up edge; pressed = LOW = down edge.
      proto::writeEvent(out, target, s.stable ? "up" : "down");
    }
  }
}

bool Button::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  if (instance >= kCount) return false;
  const char* verb = cmd.verb();
  if (!verb || strcmp(verb, "GET") != 0) return false;

  char body[24];
  snprintf(body, sizeof(body), "button.%u %s",
           (unsigned)instance,
           slots_[instance].stable ? "released" : "pressed");
  proto::writeResponse(out, body);
  return true;
}

}  // namespace zerotx
