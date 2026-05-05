// ldr.cpp - LDR analog input subsystem.

#include "ldr.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "../hal.h"

namespace zerotx {

void Ldr::begin(Stream& out) {
  pin_ = hal::pin(hal::HAL_LDR_0);
  // Analog inputs need pinMode INPUT (the default), no pull-up.
  // The voltage divider sets the level; pull-up would skew it.
  pinMode(pin_, INPUT);
  last_raw_ = analogRead(pin_);
  last_emitted_raw_ = last_raw_;
  last_sample_ms_ = millis();
  last_emit_ms_ = last_sample_ms_;
  have_emitted_ = false;
  proto::writeEvent(out, "ldr.0", "ready");
}

void Ldr::tick(uint32_t now_ms, Stream& out) {
  if (now_ms - last_sample_ms_ < kSampleIntervalMs) return;
  last_sample_ms_ = now_ms;

  uint16_t raw = analogRead(pin_);
  last_raw_ = raw;

  // Decide whether to emit. Emit if either:
  //   - the raw value moved by more than the deadband (significant
  //     change, the daemon should know)
  //   - heartbeat interval elapsed since last emit (so the daemon
  //     knows the value is still fresh even when stable)
  //   - we haven't emitted yet (cold start)
  uint16_t delta = (raw > last_emitted_raw_)
                    ? (raw - last_emitted_raw_)
                    : (last_emitted_raw_ - raw);
  bool emit = !have_emitted_
              || delta > deadband_
              || (now_ms - last_emit_ms_) >= heartbeat_ms_;
  if (!emit) return;

  char body[24];
  snprintf(body, sizeof(body), "raw=%u", (unsigned)raw);
  proto::writeEvent(out, "ldr.0", body);

  last_emitted_raw_ = raw;
  last_emit_ms_ = now_ms;
  have_emitted_ = true;
}

bool Ldr::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  if (instance != 0) return false;
  const char* verb = cmd.verb();
  if (!verb) return false;

  if (strcmp(verb, "GET") == 0) {
    char body[32];
    snprintf(body, sizeof(body), "ldr.0 raw=%u", (unsigned)last_raw_);
    proto::writeResponse(out, body);
    return true;
  }

  if (strcmp(verb, "SET") == 0) {
    const char* p = cmd.param();
    if (!p) {
      proto::writeError(out, "ldr.0", "missing-param");
      return true;
    }
    if (strcmp(p, "deadband") == 0) {
      const char* v = cmd.arg(0);
      if (!v) { proto::writeError(out, "ldr.0", "missing-value"); return true; }
      int n = atoi(v);
      if (n < 0 || n > 1023) {
        proto::writeError(out, "ldr.0", "deadband-out-of-range");
        return true;
      }
      deadband_ = (uint16_t)n;
      return true;
    }
    if (strcmp(p, "heartbeat-ms") == 0) {
      const char* v = cmd.arg(0);
      if (!v) { proto::writeError(out, "ldr.0", "missing-value"); return true; }
      long n = atol(v);
      if (n < 100 || n > 600000L) {
        proto::writeError(out, "ldr.0", "heartbeat-out-of-range");
        return true;
      }
      heartbeat_ms_ = (uint32_t)n;
      return true;
    }
    proto::writeError(out, "ldr.0", "unknown-param");
    return true;
  }

  return false;
}

}  // namespace zerotx
