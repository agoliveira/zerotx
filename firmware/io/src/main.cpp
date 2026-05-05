// main.cpp - ZeroTX IO board firmware entry point.
//
// Responsibilities:
//   1. Initialize Serial0 (USB-CDC to the daemon).
//   2. Initialize HAL (load pin map from EEPROM or fall back).
//   3. Construct the subsystem registry.
//   4. Dispatch incoming protocol commands to the right subsystem.
//   5. Tick each subsystem cooperatively every loop iteration.
//   6. Handle universal commands (GET version, GET caps).
//   7. Maintain the hardware watchdog.
//
// Adding a new subsystem to the build: include its header and add an
// instance to the kSubsystems array below. That's it.
//
// Adding a new pin: add an entry to HalPinId in hal.h, add the
// default + name in hal.cpp. Subsystems read pin numbers via hal::pin().

#include <Arduino.h>
#include <avr/wdt.h>
#include <string.h>
#include <stdlib.h>

#include "hal.h"
#include "protocol.h"
#include "subsystem.h"

#include "subsystems/hal_subsystem.h"
#include "subsystems/led_trackball.h"
// New subsystem headers go here as they land.

namespace {

// Firmware identity. Bumped on protocol-affecting changes.
constexpr const char* kFirmwareName    = "zerotx-io";
constexpr const char* kFirmwareVersion = "0.2.0-hal";

// Watchdog timeout. Long enough for any tick to complete (no
// subsystem should take more than a few ms), short enough for a real
// hang to recover quickly. AVR wdt_enable() takes a constant from
// avr/wdt.h.
constexpr uint8_t kWatchdogTimeout = WDTO_500MS;

// ---------------------------------------------------------------------
// Subsystem registry.
//
// Static instances. Order is irrelevant beyond the GET caps output,
// which presents subsystems in this order. Add new entries at the
// bottom for stable ordering.
//
// Subsystems take no constructor pin args; they fetch their pins from
// the HAL during begin().
// ---------------------------------------------------------------------
zerotx::HalSubsystem g_hal_subsys;
zerotx::LedTrackball g_led_trackball;

zerotx::Subsystem* const kSubsystems[] = {
  &g_hal_subsys,
  &g_led_trackball,
  // Add more here.
};
constexpr size_t kSubsystemCount = sizeof(kSubsystems) / sizeof(kSubsystems[0]);

// Per-loop reader for the protocol stream.
proto::LineReader g_reader{Serial};

// ---------------------------------------------------------------------
// Dispatch helpers.
// ---------------------------------------------------------------------

// Match a target token against a subsystem name, optionally parsing
// a ".N" instance suffix. Returns the subsystem (and instance index)
// on match; nullptr otherwise.
zerotx::Subsystem* lookupTarget(const char* target, uint8_t& instance_out) {
  if (!target) return nullptr;

  for (size_t i = 0; i < kSubsystemCount; ++i) {
    const char* name = kSubsystems[i]->name();
    size_t name_len = strlen(name);
    if (strncmp(target, name, name_len) != 0) continue;

    // Either an exact match (single-instance, no ".N") or a "."
    // followed by a numeric instance index.
    char tail = target[name_len];
    if (tail == '\0') {
      instance_out = 0;
      return kSubsystems[i];
    }
    if (tail == '.') {
      const char* num = target + name_len + 1;
      if (*num == '\0') return nullptr;
      char* end;
      long n = strtol(num, &end, 10);
      if (*end != '\0' || n < 0 || n >= kSubsystems[i]->count()) {
        return nullptr;
      }
      instance_out = static_cast<uint8_t>(n);
      return kSubsystems[i];
    }
    // target starts with name but isn't followed by either NUL or '.'
    // - keep looking (e.g. "vfd" prefix shouldn't match "vfdx").
  }
  return nullptr;
}

// Built-in commands not associated with any subsystem: version, caps.
bool handleBuiltin(const proto::Command& cmd, Stream& out) {
  if (!cmd.verb() || strcmp(cmd.verb(), "GET") != 0) return false;
  const char* target = cmd.target();
  if (!target) return false;

  if (strcmp(target, "version") == 0) {
    char body[64];
    snprintf(body, sizeof(body), "version %s %s", kFirmwareName, kFirmwareVersion);
    proto::writeResponse(out, body);
    return true;
  }

  if (strcmp(target, "caps") == 0) {
    // Walk subsystems and emit "name" or "name.0 name.1 ..." per
    // instance count. The line can get long; MAX_LINE on the daemon
    // side has to accommodate.
    char body[proto::MAX_LINE];
    int pos = snprintf(body, sizeof(body), "caps");
    for (size_t i = 0; i < kSubsystemCount && pos < (int)sizeof(body); ++i) {
      const char* name = kSubsystems[i]->name();
      uint8_t n = kSubsystems[i]->count();
      if (n <= 1) {
        pos += snprintf(body + pos, sizeof(body) - pos, " %s", name);
      } else {
        for (uint8_t k = 0; k < n && pos < (int)sizeof(body); ++k) {
          pos += snprintf(body + pos, sizeof(body) - pos, " %s.%u", name, (unsigned)k);
        }
      }
    }
    proto::writeResponse(out, body);
    return true;
  }

  return false;
}

void dispatch(const proto::Command& cmd, Stream& out) {
  if (handleBuiltin(cmd, out)) return;

  uint8_t instance = 0;
  zerotx::Subsystem* s = lookupTarget(cmd.target(), instance);
  if (!s) {
    proto::writeError(out, cmd.target() ? cmd.target() : "?", "unknown-target");
    return;
  }
  if (!s->handle(instance, cmd, out)) {
    proto::writeError(out, cmd.target(), "unknown-command");
  }
}

}  // namespace

// ---------------------------------------------------------------------
// Arduino entry points.
// ---------------------------------------------------------------------

void setup() {
  // Capture watchdog reset state before re-arming. AVR uses MCUSR for
  // reset cause flags; WDRF (bit 3) means watchdog reset.
  uint8_t mcusr = MCUSR;
  MCUSR = 0;
  wdt_disable();   // ensure clean state before re-enabling

  Serial.begin(115200);
  // Tiny grace period for USB-CDC enumeration; not strictly needed on
  // Mega (16U2 bridge is up immediately) but harmless.
  delay(100);

  proto::writeEvent(Serial, "boot",
      (mcusr & (1 << WDRF)) ? "watchdog-reset" : "power-on");

  // HAL must be initialized BEFORE subsystem begin() since they read
  // their pin assignments from it.
  hal::begin();
  proto::writeEvent(Serial, "hal",
      hal::source() == hal::HAL_SOURCE_EEPROM ? "loaded-eeprom" : "loaded-defaults");

  for (size_t i = 0; i < kSubsystemCount; ++i) {
    kSubsystems[i]->begin(Serial);
  }

  char ready[64];
  snprintf(ready, sizeof(ready), "ready %s %s", kFirmwareName, kFirmwareVersion);
  proto::writeEvent(Serial, "boot", ready);

  wdt_enable(kWatchdogTimeout);
}

void loop() {
  wdt_reset();

  // Process at most one line per loop iteration; long bursts of
  // commands stream over multiple iterations rather than blocking the
  // tick loop indefinitely.
  proto::Command cmd;
  if (g_reader.poll(cmd)) {
    dispatch(cmd, Serial);
  }

  uint32_t now = millis();
  for (size_t i = 0; i < kSubsystemCount; ++i) {
    kSubsystems[i]->tick(now, Serial);
  }
}
