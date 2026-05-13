// subsystem.h - abstract base for all IO subsystems.
//
// Every peripheral (VFD, indicator LEDs, WS2813 strip, LDR, button,
// buzzer, ...) implements this interface. The main dispatcher walks
// a global registry of subsystems and dispatches Commands to the one
// whose name() prefix matches the Command's target.
//
// "Name" is the short identifier used in the protocol. For instanced
// subsystems (multiple VFDs, multiple WS2813 strips, multiple
// buttons), the protocol uses "<name>.<instance>" and the dispatcher
// passes the instance index to handle().
//
// To add a new subsystem:
//
//   1. Create subsystems/<name>.h and .cpp with a class deriving
//      from Subsystem.
//   2. Implement the four virtual methods.
//   3. Add an instance to the registry array in main.cpp.
//
// That's it. No central dispatcher edit, no protocol parser change.

#ifndef ZEROTX_IO_SUBSYSTEM_H
#define ZEROTX_IO_SUBSYSTEM_H

#include <Arduino.h>

#include "protocol.h"

namespace zerotx {

class Subsystem {
public:
  virtual ~Subsystem() {}

  // Short identifier used in the protocol. Must match the literal
  // "<subsystem>" portion of "SET <subsystem>.<n> ..." commands. The
  // pointer must remain valid for the lifetime of the program (use
  // a const literal).
  virtual const char* name() const = 0;

  // Number of instances this subsystem exposes. Default is 1; override
  // for multi-instance subsystems (e.g. two VFDs, multiple buttons).
  // Instance indices are 0..count()-1. Single-instance subsystems
  // accept the bare name with no ".N" suffix in the protocol.
  virtual uint8_t count() const { return 1; }

  // One-time setup at boot. Configure pins, init libraries, etc.
  // Called after Serial0 is up so subsystems may emit boot events
  // via writeEvent().
  virtual void begin(Stream& out) { (void)out; }

  // Cooperative tick. Called every main loop iteration with the
  // current millis() value. Subsystems must return promptly; long
  // operations are spread across multiple ticks. This is also where
  // periodic events get pushed to the daemon.
  virtual void tick(uint32_t now_ms, Stream& out) {
    (void)now_ms; (void)out;
  }

  // Handle a command targeted at this subsystem. The dispatcher has
  // already verified the target matches name() and parsed the
  // instance index. Return true if the command was handled (success
  // or failure with error already written); false if the verb/param
  // combination is unknown.
  virtual bool handle(uint8_t instance, const proto::Command& cmd, Stream& out) = 0;
};

}  // namespace zerotx

#endif  // ZEROTX_IO_SUBSYSTEM_H
