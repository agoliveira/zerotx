// relay.cpp - relay output subsystem.

#include "relay.h"

#include <stdio.h>
#include <string.h>

#include "../hal.h"

namespace zerotx {

static hal::HalPinId pinIdFor(uint8_t instance) {
  return static_cast<hal::HalPinId>(hal::HAL_RELAY_0 + instance);
}

void Relay::begin(Stream& out) {
  (void)out;
  for (uint8_t i = 0; i < kCount; ++i) {
    pins_[i] = hal::pin(pinIdFor(i));
    activeLow_[i] = hal::activeLow(pinIdFor(i));
    // CRITICAL: write the off level FIRST, then change pinMode to
    // OUTPUT. AVR pinMode(OUTPUT) latches whatever's in the PORT
    // register, which we've just primed with the desired off level.
    // Without this ordering, the pin would briefly drive LOW (the
    // PORT register's reset value) before our off-level write, and
    // for an active-low relay that LOW would be a glitchy ON pulse.
    digitalWrite(pins_[i], offLevel(i));
    pinMode(pins_[i], OUTPUT);
    on_[i] = false;
  }
}

uint8_t Relay::onLevel(uint8_t instance) const {
  return activeLow_[instance] ? LOW : HIGH;
}
uint8_t Relay::offLevel(uint8_t instance) const {
  return activeLow_[instance] ? HIGH : LOW;
}

bool Relay::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  if (instance >= kCount) return false;
  const char* verb = cmd.verb();
  if (!verb) return false;

  if (strcmp(verb, "SET") == 0) {
    const char* v = cmd.param();
    if (!v) {
      proto::writeError(out, "relay", "missing-state");
      return true;
    }
    bool turnOn;
    if      (strcmp(v, "on") == 0  || strcmp(v, "1") == 0) turnOn = true;
    else if (strcmp(v, "off") == 0 || strcmp(v, "0") == 0) turnOn = false;
    else { proto::writeError(out, "relay", "invalid-state"); return true; }

    on_[instance] = turnOn;
    digitalWrite(pins_[instance], turnOn ? onLevel(instance) : offLevel(instance));
    return true;
  }

  if (strcmp(verb, "GET") == 0) {
    char body[24];
    snprintf(body, sizeof(body), "relay.%u %s",
             (unsigned)instance, on_[instance] ? "on" : "off");
    proto::writeResponse(out, body);
    return true;
  }

  return false;
}

}  // namespace zerotx
