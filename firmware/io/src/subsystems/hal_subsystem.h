// hal_subsystem.h - protocol surface for the HAL pin map.
//
// Pseudo-subsystem (no physical hardware). Lets the daemon configure
// pin assignments at runtime and persist them to EEPROM.
//
// Protocol:
//   GET hal map
//     -> > hal map source=<eeprom|defaults> count=<N>
//     -> > hal pin <id> <name> <number>
//     -> ... (one per pin; final response has no values to mark end)
//
//   GET hal source
//     -> > hal source <eeprom|defaults>
//
//   SET hal pin <name> <number>
//     -> stages pin override in EEPROM. Takes effect on next boot.
//        Returns ok or appropriate error.
//
//   SET hal reset-defaults
//     -> wipes EEPROM. Next boot uses compiled defaults and rewrites
//        them (so source becomes 'eeprom' again afterwards).
//
//   SET hal reboot
//     -> soft reset via watchdog. Daemon's USB session drops; daemon
//        reconnects after re-enumeration.

#ifndef ZEROTX_IO_HAL_SUBSYSTEM_H
#define ZEROTX_IO_HAL_SUBSYSTEM_H

#include "../subsystem.h"

namespace zerotx {

class HalSubsystem : public Subsystem {
public:
  const char* name() const override { return "hal"; }

  // No begin(), no tick() - pure protocol surface.
  bool handle(uint8_t instance, const proto::Command& cmd, Stream& out) override;

private:
  void handleGet(const proto::Command& cmd, Stream& out);
  void handleSet(const proto::Command& cmd, Stream& out);
  void emitMap(Stream& out);
};

}  // namespace zerotx

#endif  // ZEROTX_IO_HAL_SUBSYSTEM_H
