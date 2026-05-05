// ldr.h - light-dependent resistor analog input subsystem.
//
// LDR forms a voltage divider with a fixed resistor (~10kohm typical)
// between 5V and GND; the junction goes to a Mega analog pin
// (default A0 = digital pin 54). Reads as 10-bit ADC (0-1023).
//
// The firmware emits raw ADC values only; lux conversion needs a
// per-circuit calibration curve the firmware doesn't know. The
// daemon does any thresholding or conversion.
//
// Bandwidth control: the firmware emits an EVENT only when the
// reading changes by more than the deadband (default 20 counts ~=
// 2% of full scale), or every heartbeat-ms (default 5000ms) so the
// daemon knows the value is still current even when stable.
//
// Protocol:
//   SET ldr.0 deadband <0..1023>      cap on stable-state spam
//   SET ldr.0 heartbeat-ms <ms>       max gap between emits
//   GET ldr.0                          > ldr.0 raw=<n>
//
// Events:
//   EVENT ldr.0 raw=<n>
//
// Single instance.

#ifndef ZEROTX_IO_LDR_H
#define ZEROTX_IO_LDR_H

#include "../subsystem.h"

namespace zerotx {

class Ldr : public Subsystem {
public:
  const char* name() const override { return "ldr"; }
  uint8_t count() const override { return 1; }

  void begin(Stream& out) override;
  void tick(uint32_t now_ms, Stream& out) override;
  bool handle(uint8_t instance, const proto::Command& cmd, Stream& out) override;

private:
  // ~10Hz sample rate. Faster gains nothing for an LDR (light
  // changes slowly); slower risks missing fast covers/uncovers.
  static constexpr uint32_t kSampleIntervalMs = 100;

  uint8_t  pin_              = 0xFF;
  uint16_t last_raw_         = 0;
  uint16_t last_emitted_raw_ = 0;
  uint32_t last_sample_ms_   = 0;
  uint32_t last_emit_ms_     = 0;

  uint16_t deadband_         = 20;
  uint32_t heartbeat_ms_     = 5000;
  bool     have_emitted_     = false;
};

}  // namespace zerotx

#endif  // ZEROTX_IO_LDR_H
