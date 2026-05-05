// led.h - generic indicator-LED subsystem.
//
// Four panel LEDs as pure on/off outputs. The trackball LEDs have
// their own subsystem because they have multi-state animation;
// these are dumber.
//
// Default polarity is active-HIGH (HIGH = LED lit). Per-pin
// ACTIVE_LOW flag in HAL flips this for boards wired through an
// inverting transistor stage.
//
// Protocol:
//   SET led.<n> <on|off|0|1>      -> set state
//   GET led.<n>                    -> > led.<n> <on|off>
//
// Multi-instance, count() = 4.

#ifndef ZEROTX_IO_LED_H
#define ZEROTX_IO_LED_H

#include "../subsystem.h"

namespace zerotx {

class Led : public Subsystem {
public:
  static constexpr uint8_t kCount = 4;

  const char* name() const override { return "led"; }
  uint8_t count() const override { return kCount; }

  void begin(Stream& out) override;
  bool handle(uint8_t instance, const proto::Command& cmd, Stream& out) override;

private:
  uint8_t onLevel(uint8_t instance) const;
  uint8_t offLevel(uint8_t instance) const;

  uint8_t pins_[kCount]      = {0xFF, 0xFF, 0xFF, 0xFF};
  bool    activeLow_[kCount] = {false, false, false, false};
  bool    on_[kCount]        = {false, false, false, false};
};

}  // namespace zerotx

#endif  // ZEROTX_IO_LED_H
