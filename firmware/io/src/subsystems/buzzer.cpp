// buzzer.cpp - piezo buzzer subsystem.

#include "buzzer.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "../hal.h"

namespace zerotx {

// Frequency clamps. tone() accepts any value but very-low or very-
// high frequencies are inaudible or strain the buzzer.
static constexpr uint16_t kMinFreqHz = 31;     // tone() lower limit
static constexpr uint16_t kMaxFreqHz = 20000;  // ultrasonic ceiling
static constexpr uint32_t kMaxDurMs  = 60000;  // 1 minute cap

void Buzzer::begin(Stream& out) {
  pin_ = hal::pin(hal::HAL_BUZZER);
  pinMode(pin_, OUTPUT);
  digitalWrite(pin_, LOW);
  end_ms_ = 0;
  proto::writeEvent(out, "buzzer", "ready");
}

void Buzzer::tick(uint32_t now_ms, Stream& out) {
  (void)out;
  // Hardware-ended tones already stopped via tone()'s ISR; we only
  // clear our state tracking so GET reports correctly.
  if (end_ms_ != 0 && now_ms >= end_ms_) {
    end_ms_ = 0;
  }
}

bool Buzzer::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  (void)instance;
  const char* verb = cmd.verb();
  if (!verb) return false;

  if (strcmp(verb, "GET") == 0) {
    uint32_t now = millis();
    bool sounding = end_ms_ != 0 && now < end_ms_;
    char body[24];
    snprintf(body, sizeof(body), "buzzer %s", sounding ? "sounding" : "idle");
    proto::writeResponse(out, body);
    return true;
  }

  if (strcmp(verb, "SET") != 0) return false;

  const char* p = cmd.param();
  if (!p) {
    proto::writeError(out, "buzzer", "missing-param");
    return true;
  }

  if (strcmp(p, "beep") == 0) {
    const char* freqStr = cmd.arg(0);
    const char* durStr  = cmd.arg(1);
    if (!freqStr || !durStr) {
      proto::writeError(out, "buzzer", "usage:set buzzer beep <freq> <dur_ms>");
      return true;
    }
    long freq = atol(freqStr);
    long dur  = atol(durStr);
    if (freq < kMinFreqHz || freq > kMaxFreqHz) {
      proto::writeError(out, "buzzer", "freq-out-of-range");
      return true;
    }
    if (dur < 1 || dur > (long)kMaxDurMs) {
      proto::writeError(out, "buzzer", "duration-out-of-range");
      return true;
    }
    tone(pin_, (unsigned int)freq, (unsigned long)dur);
    end_ms_ = millis() + (uint32_t)dur;
    return true;
  }

  if (strcmp(p, "silence") == 0) {
    noTone(pin_);
    digitalWrite(pin_, LOW);
    end_ms_ = 0;
    return true;
  }

  proto::writeError(out, "buzzer", "unknown-param");
  return true;
}

}  // namespace zerotx
