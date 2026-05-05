// ws.h - WS2813 addressable LED strip subsystem.
//
// Single strip of up to kPixelCount pixels, daemon-controlled. The
// daemon sets pixels via SET ws.0 commands; the firmware updates
// the strip immediately (auto-show after each command).
//
// AVR + WS2813 caveat: writing the strip disables interrupts during
// the bit-banged data signal (~30 microseconds per pixel). For 16
// pixels that's ~500us per show. USB-CDC packets that arrive during
// that window may be delayed. Not visible at human-watching cadences;
// would matter for sustained high-throughput animations, which we
// explicitly don't aim for.
//
// Protocol:
//   SET ws.0 pixel <index> <rrggbb>     # set one pixel, auto-show
//   SET ws.0 all <rrggbb>               # set all pixels, auto-show
//   SET ws.0 brightness <0..255>        # global brightness
//   SET ws.0 clear                      # all pixels off, auto-show
//   GET ws.0                            # > ws.0 count=<N> brightness=<B>
//
// Single instance.

#ifndef ZEROTX_IO_WS_H
#define ZEROTX_IO_WS_H

#include <Adafruit_NeoPixel.h>

#include "../subsystem.h"

namespace zerotx {

class Ws : public Subsystem {
public:
  static constexpr uint16_t kPixelCount = 16;

  Ws() {}

  const char* name() const override { return "ws"; }
  uint8_t count() const override { return 1; }

  void begin(Stream& out) override;
  bool handle(uint8_t instance, const proto::Command& cmd, Stream& out) override;

private:
  // Adafruit_NeoPixel takes pin in its ctor; we need to delay
  // construction until begin() (when HAL pin is resolved). Use
  // placement-new into static storage like the VFD does.
  Adafruit_NeoPixel* strip_ = nullptr;
  alignas(Adafruit_NeoPixel) uint8_t strip_storage_[
      sizeof(Adafruit_NeoPixel)];

  uint8_t brightness_ = 64;     // 0..255

  // Parse an "rrggbb" hex string. Returns true and fills (r,g,b) on
  // success.
  static bool parseColor(const char* s, uint8_t& r, uint8_t& g, uint8_t& b);
};

}  // namespace zerotx

#endif  // ZEROTX_IO_WS_H
