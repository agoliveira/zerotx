// led.cpp - generic on/off LED subsystem.

#include "led.h"

#include <stdio.h>
#include <string.h>

#include "../hal.h"

namespace zerotx {

// Active-HIGH (NPN switch on the LED cathode line; GPIO HIGH ->
// transistor ON -> LED lit). Same convention as LedTrackball; flip
// here if the panel wiring is inverted.
static constexpr uint8_t ON_LEVEL  = HIGH;
static constexpr uint8_t OFF_LEVEL = LOW;

static hal::HalPinId pinIdFor(uint8_t instance) {
  return static_cast<hal::HalPinId>(hal::HAL_LED_0 + instance);
}

void Led::begin(Stream& out) {
  (void)out;
  for (uint8_t i = 0; i < kCount; ++i) {
    pins_[i] = hal::pin(pinIdFor(i));
    pinMode(pins_[i], OUTPUT);
    digitalWrite(pins_[i], OFF_LEVEL);
    on_[i] = false;
  }
}

bool Led::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  if (instance >= kCount) return false;
  const char* verb = cmd.verb();
  if (!verb) return false;

  if (strcmp(verb, "SET") == 0) {
    const char* v = cmd.param();
    if (!v) {
      proto::writeError(out, "led", "missing-state");
      return true;
    }
    bool turnOn;
    if      (strcmp(v, "on") == 0  || strcmp(v, "1") == 0) turnOn = true;
    else if (strcmp(v, "off") == 0 || strcmp(v, "0") == 0) turnOn = false;
    else { proto::writeError(out, "led", "invalid-state"); return true; }

    on_[instance] = turnOn;
    digitalWrite(pins_[instance], turnOn ? ON_LEVEL : OFF_LEVEL);
    return true;
  }

  if (strcmp(verb, "GET") == 0) {
    char body[20];
    snprintf(body, sizeof(body), "led.%u %s",
             (unsigned)instance, on_[instance] ? "on" : "off");
    proto::writeResponse(out, body);
    return true;
  }

  return false;
}

}  // namespace zerotx
