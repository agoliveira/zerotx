// servo.h - servo output subsystem.
//
// Four servo channels driven via the Arduino Servo library. Pin
// assignments come from HAL (HAL_SERVO_0..3).
//
// The Servo library uses Timer 5 on the Mega 2560. Once any servo
// instance is attached, hardware PWM on pins 44/45/46 is gone.
// Today VFD0 lives on those pins as plain digital outputs (HD44780
// doesn't need PWM), so the conflict has no practical effect.
//
// Lazy attach: the underlying Servo object only grabs its pin (and
// activates Timer 5) on first command. An instance that's never
// commanded leaves its pin alone and Timer 5 alone. SET servo.<n>
// detach releases the pin and lets it return to whatever GPIO use
// the operator wants.
//
// Protocol:
//
//   SET servo.<n> angle <0..180>     Standard servo angle.
//   SET servo.<n> us <500..2500>     Direct microsecond pulse width
//                                    (escape hatch for servos with
//                                    non-standard travel ranges).
//   SET servo.<n> detach             Release the pin. Servo stops
//                                    receiving pulses; its position
//                                    becomes mechanical-load-dependent.
//   GET servo.<n>                    Reports attached/detached and
//                                    last commanded value.

#ifndef ZEROTX_IO_SERVO_H
#define ZEROTX_IO_SERVO_H

#include <Arduino.h>
#include <Servo.h>

#include "../hal.h"
#include "../subsystem.h"

namespace zerotx {

class ServoSubsys : public Subsystem {
public:
  ServoSubsys() {}

  static constexpr uint8_t kInstanceCount = 4;

  const char* name() const override { return "servo"; }
  uint8_t count() const override { return kInstanceCount; }

  void begin(Stream& out) override;
  bool handle(uint8_t instance, const proto::Command& cmd, Stream& out) override;

private:
  struct Instance {
    ::Servo  servo;
    bool     attached;       // true after first attach()
    uint16_t last_us;        // last commanded microsecond value (0 = never set)
    int16_t  last_angle;     // last commanded angle 0..180, or -1 if last cmd was 'us'
  };

  Instance instances_[kInstanceCount];

  static const hal::HalPinId kPinForInstance[kInstanceCount];
  static const char* const   kInstanceLabel[kInstanceCount];

  bool ensureAttached(uint8_t index, Instance& inst, Stream& out);
  bool handleSet(uint8_t index, Instance& inst, const proto::Command& cmd, Stream& out);
  void handleGet(uint8_t index, Instance& inst, Stream& out);
};

}  // namespace zerotx

#endif  // ZEROTX_IO_SERVO_H
