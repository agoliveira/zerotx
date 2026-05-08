// servo.cpp - servo subsystem implementation.

#include "servo.h"

#include <string.h>
#include <stdlib.h>

namespace zerotx {

const hal::HalPinId ServoSubsys::kPinForInstance[kInstanceCount] = {
  hal::HAL_SERVO_0,
  hal::HAL_SERVO_1,
  hal::HAL_SERVO_2,
  hal::HAL_SERVO_3,
};

const char* const ServoSubsys::kInstanceLabel[kInstanceCount] = {
  "servo.0", "servo.1", "servo.2", "servo.3",
};

void ServoSubsys::begin(Stream& out) {
  (void)out;
  // Lazy attach: don't grab any servo pin until first command. This
  // keeps Timer 5 free for hardware PWM users until a servo is
  // actually addressed.
  for (uint8_t i = 0; i < kInstanceCount; ++i) {
    instances_[i].attached = false;
    instances_[i].last_us = 0;
    instances_[i].last_angle = -1;
  }
}

bool ServoSubsys::ensureAttached(uint8_t index, Instance& inst, Stream& out) {
  if (inst.attached) return true;
  uint8_t pin = hal::pin(kPinForInstance[index]);
  // Servo::attach returns 0 on failure (no slot available; library
  // supports up to 12 servos on Mega anyway, so realistically this
  // succeeds, but we check defensively).
  if (inst.servo.attach(pin) == 0) {
    proto::writeError(out, kInstanceLabel[index], "attach-failed");
    return false;
  }
  inst.attached = true;
  return true;
}

bool ServoSubsys::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  if (instance >= kInstanceCount) return false;
  Instance& inst = instances_[instance];

  const char* verb = cmd.verb();
  if (!verb) return false;
  if (strcmp(verb, "SET") == 0) return handleSet(instance, inst, cmd, out);
  if (strcmp(verb, "GET") == 0) { handleGet(instance, inst, out); return true; }
  return false;
}

bool ServoSubsys::handleSet(uint8_t index, Instance& inst, const proto::Command& cmd, Stream& out) {
  const char* label = kInstanceLabel[index];
  const char* p = cmd.param();
  if (!p) {
    proto::writeError(out, label, "missing-param");
    return true;
  }

  if (strcmp(p, "angle") == 0) {
    const char* v = cmd.arg(0);
    if (!v) { proto::writeError(out, label, "missing-angle"); return true; }
    int n = atoi(v);
    if (n < 0 || n > 180) { proto::writeError(out, label, "invalid-angle"); return true; }
    if (!ensureAttached(index, inst, out)) return true;
    inst.servo.write((uint8_t)n);
    inst.last_angle = (int16_t)n;
    inst.last_us = inst.servo.readMicroseconds();
    return true;
  }

  if (strcmp(p, "us") == 0) {
    const char* v = cmd.arg(0);
    if (!v) { proto::writeError(out, label, "missing-us"); return true; }
    int n = atoi(v);
    if (n < 500 || n > 2500) { proto::writeError(out, label, "invalid-us"); return true; }
    if (!ensureAttached(index, inst, out)) return true;
    inst.servo.writeMicroseconds(n);
    inst.last_us = (uint16_t)n;
    inst.last_angle = -1;   // last command was raw us, not angle
    return true;
  }

  if (strcmp(p, "detach") == 0) {
    if (inst.attached) {
      inst.servo.detach();
      inst.attached = false;
    }
    return true;
  }

  proto::writeError(out, label, "unknown-param");
  return true;
}

void ServoSubsys::handleGet(uint8_t index, Instance& inst, Stream& out) {
  char body[64];
  if (!inst.attached) {
    snprintf(body, sizeof(body), "%s attached=0", kInstanceLabel[index]);
  } else if (inst.last_angle >= 0) {
    snprintf(body, sizeof(body), "%s attached=1 angle=%d us=%u",
             kInstanceLabel[index], inst.last_angle, inst.last_us);
  } else {
    snprintf(body, sizeof(body), "%s attached=1 us=%u",
             kInstanceLabel[index], inst.last_us);
  }
  proto::writeResponse(out, body);
}

}  // namespace zerotx
