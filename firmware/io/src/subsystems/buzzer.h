// buzzer.h - piezo buzzer subsystem.
//
// Single GPIO drives a piezo buzzer via Arduino's tone(). Works
// with passive piezos (variable pitch) and active piezos (fixed
// pitch but tone() still drives the pin so it works either way).
//
// The firmware plays one tone at a time: SET buzzer beep starts a
// tone; SET buzzer silence stops it; a tone with a duration auto-
// stops via the AVR timer ISR that backs tone(). This subsystem
// does NOT play patterns or sequences; the daemon does that by
// issuing back-to-back beep commands at appropriate intervals.
//
// Protocol:
//   SET buzzer beep <freq_hz> <duration_ms>
//   SET buzzer silence
//   GET buzzer                        > buzzer <sounding|idle>
//
// Single instance. No '.0' suffix in the protocol; the system has
// at most one buzzer.

#ifndef ZEROTX_IO_BUZZER_H
#define ZEROTX_IO_BUZZER_H

#include "../subsystem.h"

namespace zerotx {

class Buzzer : public Subsystem {
public:
  const char* name() const override { return "buzzer"; }

  void begin(Stream& out) override;
  void tick(uint32_t now_ms, Stream& out) override;
  bool handle(uint8_t instance, const proto::Command& cmd, Stream& out) override;

private:
  uint8_t  pin_    = 0xFF;
  // 0 = idle. Otherwise = millis() value at which the current tone
  // ends. Tracked so GET can report sounding/idle accurately; the
  // hardware end is enforced by tone()'s own duration machinery.
  uint32_t end_ms_ = 0;
};

}  // namespace zerotx

#endif  // ZEROTX_IO_BUZZER_H
