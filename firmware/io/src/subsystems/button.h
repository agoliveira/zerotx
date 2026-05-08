// button.h - panel button subsystem.
//
// Ten GPIO buttons wired active-low to ground with the Mega's
// internal pull-up resistors enabled. Polled at the loop cadence
// with 20ms software debounce. State changes (down/up edges) emit
// EVENT lines; the daemon decides what each button does.
//
// No long-press detection on the firmware; daemon can compute press
// duration from event timestamps if it wants.
//
// Protocol:
//   GET button.<n>          -> > button.<n> <pressed|released>
//
// Events (firmware -> daemon, no daemon poll required):
//   EVENT button.<n> down
//   EVENT button.<n> up
//
// Multi-instance, count() = 5.

#ifndef ZEROTX_IO_BUTTON_H
#define ZEROTX_IO_BUTTON_H

#include "../subsystem.h"

namespace zerotx {

class Button : public Subsystem {
public:
  static constexpr uint8_t kCount = 10;
  // Debounce: state must be stable for this many ms before edge fires.
  static constexpr uint32_t kDebounceMs = 20;

  const char* name() const override { return "button"; }
  uint8_t count() const override { return kCount; }

  void begin(Stream& out) override;
  void tick(uint32_t now_ms, Stream& out) override;
  bool handle(uint8_t instance, const proto::Command& cmd, Stream& out) override;

private:
  // Per-button state. raw_ tracks the last pin read, stable_ is the
  // debounced value. last_change_ms_ marks when raw_ last differed
  // from stable_.
  struct Slot {
    uint8_t  pin = 0xFF;
    bool     raw = true;          // active-low, true = released
    bool     stable = true;       // debounced
    uint32_t last_change_ms = 0;
  };
  Slot slots_[kCount];
};

}  // namespace zerotx

#endif  // ZEROTX_IO_BUTTON_H
