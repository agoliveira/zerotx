// led_trackball.h - trackball status LED subsystem.
//
// Two-LED arcade trackball (green + red), each switched to ground via
// an NPN transistor driven by a Mega GPIO pin. The protocol exposes
// a small set of named states; the firmware renders the visual
// (solid, slow pulse, blink) without daemon-side tick-by-tick driving.
//
// Pin assignments come from the HAL (hal::HAL_LED_TRACKBALL_GREEN,
// hal::HAL_LED_TRACKBALL_RED). begin() resolves them; subsequent
// reassignment requires a reboot since pinMode() runs once.
//
// States (canonical, do not extend without explicit need):
//   off          - both LEDs off
//   green-solid  - green on continuously
//   green-pulse  - green slow breathing (~2s cycle)
//   red-solid    - red on continuously
//   red-blink    - red rapid blink (~150ms on / ~150ms off)
//
// Protocol:
//   SET led.trackball <state>     -> apply state
//   GET led.trackball             -> respond with current state
//
// Single instance; no .N suffix in the protocol.

#ifndef ZEROTX_IO_LED_TRACKBALL_H
#define ZEROTX_IO_LED_TRACKBALL_H

#include "../subsystem.h"

namespace zerotx {

class LedTrackball : public Subsystem {
public:
  LedTrackball() {}

  const char* name() const override { return "led.trackball"; }

  void begin(Stream& out) override;
  void tick(uint32_t now_ms, Stream& out) override;
  bool handle(uint8_t instance, const proto::Command& cmd, Stream& out) override;

private:
  enum class State : uint8_t {
    Off,
    GreenSolid,
    GreenPulse,
    RedSolid,
    RedBlink,
  };

  // Render the current state for the given time. Called from tick()
  // so animated states update without daemon involvement.
  void render(uint32_t now_ms);

  // Translate a textual state to the enum. Returns false if unknown.
  static bool parseState(const char* s, State& out);

  // Reverse - human-readable name for the state, used by GET.
  static const char* stateName(State s);

  // Pin numbers resolved from HAL at begin() time. Stored locally so
  // tick() doesn't keep going through the HAL accessor.
  uint8_t green_pin_ = 0xFF;
  uint8_t red_pin_   = 0xFF;
  State   state_ = State::Off;
};

}  // namespace zerotx

#endif  // ZEROTX_IO_LED_TRACKBALL_H
