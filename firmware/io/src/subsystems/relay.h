// relay.h - relay output subsystem.
//
// Four independently controlled relays. Electrically the firmware
// behavior is identical to a generic on/off LED output, but kept as
// a separate subsystem because relays drive real loads (motors,
// pumps, fans, latches): naming them in the protocol surface makes
// daemon-side intent obvious in traces and rules out cross-wiring
// mistakes.
//
// Default polarity: HIGH = energized (active-high) like every other
// output. Per-pin ACTIVE_LOW flag in HAL flips this for boards that
// use an inverting transistor stage (common on cheap modular relay
// boards). Set via:
//
//   SET hal flag relay_0 0x01
//   SET hal reboot
//
// Boot semantics: the GPIO is set to its OFF level BEFORE
// pinMode(OUTPUT) so the relay never glitches on during the brief
// transition from input-high-Z to driven-output. This requires
// digitalWrite then pinMode in that order.
//
// Protocol:
//   SET relay.<n> <on|off|0|1>      -> energize / de-energize
//   GET relay.<n>                    -> > relay.<n> <on|off>
//
// Multi-instance, count() = 4.

#ifndef ZEROTX_IO_RELAY_H
#define ZEROTX_IO_RELAY_H

#include "../subsystem.h"

namespace zerotx {

class Relay : public Subsystem {
public:
  static constexpr uint8_t kCount = 4;

  const char* name() const override { return "relay"; }
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

#endif  // ZEROTX_IO_RELAY_H
