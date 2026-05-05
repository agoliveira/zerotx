// encoder.h - rotary encoder subsystem (KY-040 style).
//
// Single quadrature rotary encoder with optional push-button. Three
// pins: A (CLK), B (DT), SW (push). All idle HIGH via internal
// pull-ups. The A and B pins form a quadrature pair that the
// firmware reads via state-table decoding for robust bounce
// rejection.
//
// Encoders typically produce 4 quadrature transitions per detent
// (one click). This subsystem accumulates sub-detent transitions
// and emits one CW or CCW event per detent; the daemon sees a
// clean stream of clicks rather than the raw transitions.
//
// The push-button uses the same polling debounce pattern as the
// button subsystem (20ms stable hold).
//
// Protocol:
//   GET enc.0                          > enc.0 button=<pressed|released>
//
// Events:
//   EVENT enc.0 cw
//   EVENT enc.0 ccw
//   EVENT enc.0 press
//   EVENT enc.0 release
//
// Single instance.

#ifndef ZEROTX_IO_ENCODER_H
#define ZEROTX_IO_ENCODER_H

#include "../subsystem.h"

namespace zerotx {

class Encoder : public Subsystem {
public:
  // Quadrature transitions per detent. KY-040 modules are 4. Some
  // industrial encoders are 2. Compile-time for now; could become
  // a runtime SET if a different encoder shows up.
  static constexpr int8_t kTransitionsPerDetent = 4;

  // Button debounce: same value as the button subsystem.
  static constexpr uint32_t kButtonDebounceMs = 20;

  const char* name() const override { return "enc"; }
  uint8_t count() const override { return 1; }

  void begin(Stream& out) override;
  void tick(uint32_t now_ms, Stream& out) override;
  bool handle(uint8_t instance, const proto::Command& cmd, Stream& out) override;

private:
  uint8_t a_pin_  = 0xFF;
  uint8_t b_pin_  = 0xFF;
  uint8_t sw_pin_ = 0xFF;

  // Quadrature state machine. prev_state_ holds the (A<<1)|B from
  // the last sample (0..3). accum_ accumulates +/-1 per valid
  // transition; we emit when |accum_| reaches kTransitionsPerDetent.
  uint8_t prev_state_ = 0;
  int8_t  accum_      = 0;

  // Button debounce state.
  bool     btn_raw_           = true;   // active-low; idle = HIGH = true
  bool     btn_stable_        = true;
  uint32_t btn_last_change_ms = 0;
};

}  // namespace zerotx

#endif  // ZEROTX_IO_ENCODER_H
