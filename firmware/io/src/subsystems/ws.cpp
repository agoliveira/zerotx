// ws.cpp - WS2813 strip subsystem implementation.

#include "ws.h"

#include <new>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "../hal.h"

namespace zerotx {

void Ws::begin(Stream& out) {
  uint8_t pin = hal::pin(hal::HAL_WS_DATA);
  // NEO_GRB + NEO_KHZ800 is the standard WS2812B/WS2813 timing.
  strip_ = new (strip_storage_) Adafruit_NeoPixel(
      kPixelCount, pin, NEO_GRB + NEO_KHZ800);
  strip_->begin();
  strip_->setBrightness(brightness_);
  strip_->clear();
  strip_->show();
  proto::writeEvent(out, "ws.0", "ready");
}

bool Ws::parseColor(const char* s, uint8_t& r, uint8_t& g, uint8_t& b) {
  if (!s) return false;
  size_t len = strlen(s);
  if (len != 6) return false;
  // Validate all hex digits.
  for (size_t i = 0; i < 6; ++i) {
    char c = s[i];
    bool ok = (c >= '0' && c <= '9') ||
              (c >= 'a' && c <= 'f') ||
              (c >= 'A' && c <= 'F');
    if (!ok) return false;
  }
  unsigned long v = strtoul(s, nullptr, 16);
  r = static_cast<uint8_t>((v >> 16) & 0xFF);
  g = static_cast<uint8_t>((v >> 8)  & 0xFF);
  b = static_cast<uint8_t>(v         & 0xFF);
  return true;
}

bool Ws::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  if (instance != 0) return false;
  if (!strip_) {
    proto::writeError(out, "ws.0", "not-initialized");
    return true;
  }

  const char* verb = cmd.verb();
  if (!verb) return false;

  if (strcmp(verb, "GET") == 0) {
    char body[40];
    snprintf(body, sizeof(body), "ws.0 count=%u brightness=%u",
             (unsigned)kPixelCount, (unsigned)brightness_);
    proto::writeResponse(out, body);
    return true;
  }

  if (strcmp(verb, "SET") != 0) return false;
  const char* p = cmd.param();
  if (!p) {
    proto::writeError(out, "ws.0", "missing-param");
    return true;
  }

  if (strcmp(p, "pixel") == 0) {
    const char* idxStr = cmd.arg(0);
    const char* hexStr = cmd.arg(1);
    if (!idxStr || !hexStr) {
      proto::writeError(out, "ws.0", "usage:set ws.0 pixel <i> <rrggbb>");
      return true;
    }
    int idx = atoi(idxStr);
    if (idx < 0 || idx >= (int)kPixelCount) {
      proto::writeError(out, "ws.0", "index-out-of-range");
      return true;
    }
    uint8_t r, g, b;
    if (!parseColor(hexStr, r, g, b)) {
      proto::writeError(out, "ws.0", "invalid-color");
      return true;
    }
    strip_->setPixelColor(idx, r, g, b);
    strip_->show();
    return true;
  }

  if (strcmp(p, "all") == 0) {
    const char* hexStr = cmd.arg(0);
    if (!hexStr) {
      proto::writeError(out, "ws.0", "missing-color");
      return true;
    }
    uint8_t r, g, b;
    if (!parseColor(hexStr, r, g, b)) {
      proto::writeError(out, "ws.0", "invalid-color");
      return true;
    }
    for (uint16_t i = 0; i < kPixelCount; ++i) {
      strip_->setPixelColor(i, r, g, b);
    }
    strip_->show();
    return true;
  }

  if (strcmp(p, "brightness") == 0) {
    const char* v = cmd.arg(0);
    if (!v) { proto::writeError(out, "ws.0", "missing-brightness"); return true; }
    int n = atoi(v);
    if (n < 0 || n > 255) {
      proto::writeError(out, "ws.0", "brightness-out-of-range");
      return true;
    }
    brightness_ = static_cast<uint8_t>(n);
    strip_->setBrightness(brightness_);
    strip_->show();
    return true;
  }

  if (strcmp(p, "clear") == 0) {
    strip_->clear();
    strip_->show();
    return true;
  }

  proto::writeError(out, "ws.0", "unknown-param");
  return true;
}

}  // namespace zerotx
