// hal_subsystem.cpp - HAL protocol surface implementation.

#include "hal_subsystem.h"

#include <stdlib.h>
#include <string.h>

#include "../hal.h"

namespace zerotx {

bool HalSubsystem::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  (void)instance;
  const char* verb = cmd.verb();
  if (!verb) return false;

  if (strcmp(verb, "GET") == 0) {
    handleGet(cmd, out);
    return true;
  }
  if (strcmp(verb, "SET") == 0) {
    handleSet(cmd, out);
    return true;
  }
  return false;
}

void HalSubsystem::handleGet(const proto::Command& cmd, Stream& out) {
  // Param distinguishes sub-queries: "map", "source".
  // "GET hal" with no param -> default to map.
  const char* p = cmd.param();
  if (!p || strcmp(p, "map") == 0) {
    emitMap(out);
    return;
  }
  if (strcmp(p, "source") == 0) {
    char body[40];
    snprintf(body, sizeof(body), "hal source %s",
             hal::source() == hal::HAL_SOURCE_EEPROM ? "eeprom" : "defaults");
    proto::writeResponse(out, body);
    return;
  }
  proto::writeError(out, "hal", "unknown-get");
}

void HalSubsystem::emitMap(Stream& out) {
  // Header line.
  char body[64];
  snprintf(body, sizeof(body), "hal map source=%s count=%u",
           hal::source() == hal::HAL_SOURCE_EEPROM ? "eeprom" : "defaults",
           (unsigned)hal::HAL_PIN_COUNT);
  proto::writeResponse(out, body);

  // One line per pin. Format: "hal pin <id-num> <name> <pin-num>"
  for (uint8_t i = 0; i < hal::HAL_PIN_COUNT; ++i) {
    hal::HalPinId id = static_cast<hal::HalPinId>(i);
    const char* nm = hal::pinName(id);
    snprintf(body, sizeof(body), "hal pin %u %s %u",
             (unsigned)i, nm ? nm : "?", (unsigned)hal::pin(id));
    proto::writeResponse(out, body);
  }
}

void HalSubsystem::handleSet(const proto::Command& cmd, Stream& out) {
  const char* p = cmd.param();
  if (!p) {
    proto::writeError(out, "hal", "missing-param");
    return;
  }

  if (strcmp(p, "pin") == 0) {
    const char* nameStr = cmd.arg(0);
    const char* numStr  = cmd.arg(1);
    if (!nameStr || !numStr) {
      proto::writeError(out, "hal", "usage:set hal pin <name> <num>");
      return;
    }
    hal::HalPinId id;
    if (!hal::pinIdByName(nameStr, id)) {
      proto::writeError(out, "hal", "unknown-pin-name");
      return;
    }
    char* end;
    long n = strtol(numStr, &end, 10);
    if (*end != '\0') {
      proto::writeError(out, "hal", "invalid-pin-number");
      return;
    }
    if (n < 0 || n > 255) {
      proto::writeError(out, "hal", "pin-out-of-range");
      return;
    }
    if (!hal::stagePin(id, static_cast<uint8_t>(n))) {
      proto::writeError(out, "hal", "stage-failed");
      return;
    }
    char body[64];
    snprintf(body, sizeof(body), "hal staged %s %ld (reboot to apply)",
             nameStr, n);
    proto::writeResponse(out, body);
    return;
  }

  if (strcmp(p, "reset-defaults") == 0) {
    hal::resetDefaults();
    proto::writeResponse(out, "hal reset-defaults staged (reboot to apply)");
    return;
  }

  if (strcmp(p, "reboot") == 0) {
    proto::writeResponse(out, "hal reboot");
    out.flush();
    hal::reboot();  // does not return
  }

  proto::writeError(out, "hal", "unknown-set");
}

}  // namespace zerotx
