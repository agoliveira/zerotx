// hal.h - hardware abstraction for pin assignments.
//
// All pins used by subsystems are listed in HalPinId. The actual
// pin numbers come from the runtime pin map, which is loaded from
// EEPROM at boot (or falls back to compiled defaults if EEPROM is
// blank/corrupt). This means re-pinning the case wiring does NOT
// require a firmware reflash: the daemon writes new values via the
// "hal" subsystem protocol commands and reboots the Mega.
//
// Adding a new pin:
//   1. Add an entry to HalPinId, before HAL_PIN_COUNT.
//   2. Add an entry to kHalPinNames[] in hal.cpp (string for protocol).
//   3. Add a default in kHalPinDefaults[] in hal.cpp.
//   4. Subsystem reads the pin number via hal::pin(HAL_xxx).
//
// Hardcoded pins (NOT in the HAL):
//   - Pin 0/1: USB Serial0 (the protocol channel itself). If these
//     could be remapped, the daemon couldn't recover from a bricked
//     config. They stay fixed.

#ifndef ZEROTX_IO_HAL_H
#define ZEROTX_IO_HAL_H

#include <Arduino.h>

namespace hal {

// Stable pin identifiers. Order is part of the EEPROM layout - new
// entries go at the END to keep older EEPROM contents valid. If you
// reorder these, bump HAL_EEPROM_VERSION in hal.cpp.
enum HalPinId : uint8_t {
  HAL_LED_TRACKBALL_GREEN = 0,
  HAL_LED_TRACKBALL_RED   = 1,
  // VFD instance 0 (Noritake CU20025ECPB-W1J in 4-bit HD44780 mode).
  // 6 pins: RS, EN, then four data lines D4..D7.
  HAL_VFD0_RS = 2,
  HAL_VFD0_EN = 3,
  HAL_VFD0_D4 = 4,
  HAL_VFD0_D5 = 5,
  HAL_VFD0_D6 = 6,
  HAL_VFD0_D7 = 7,
  // 5 panel buttons. Active-low to GND with internal pull-up.
  HAL_BUTTON_0 = 8,
  HAL_BUTTON_1 = 9,
  HAL_BUTTON_2 = 10,
  HAL_BUTTON_3 = 11,
  HAL_BUTTON_4 = 12,
  // 4 generic indicator LEDs. Pure on/off, daemon controls.
  HAL_LED_0 = 13,
  HAL_LED_1 = 14,
  HAL_LED_2 = 15,
  HAL_LED_3 = 16,
  // WS2813 strip data line.
  HAL_WS_DATA = 17,
  // 4 relays. Active-high default like all other outputs; per-pin
  // ACTIVE_LOW flag flips polarity for boards that need it.
  HAL_RELAY_0 = 18,
  HAL_RELAY_1 = 19,
  HAL_RELAY_2 = 20,
  HAL_RELAY_3 = 21,
  HAL_PIN_COUNT  // sentinel; must be last
};

// Per-pin flag bits. Bit 0 (ACTIVE_LOW) inverts the active level for
// output subsystems: HIGH becomes idle, LOW becomes asserted. The
// firmware default for ALL outputs is active-high (HIGH = active);
// setting this flag is for boards that wire their input through a
// transistor stage that inverts the polarity.
constexpr uint8_t HAL_FLAG_ACTIVE_LOW = 0x01;

// Source of the currently-active pin map. Reported via GET hal map
// so the daemon can tell whether the Mega is on operator-config or
// fallback values.
enum HalSource : uint8_t {
  HAL_SOURCE_DEFAULTS = 0,
  HAL_SOURCE_EEPROM   = 1,
};

// Initialize the pin map. Must be called before any subsystem
// begin(). Reads EEPROM; if the magic/version/CRC is bad or the
// stored count differs from compile-time HAL_PIN_COUNT, falls back
// to compiled defaults AND writes defaults back to EEPROM so the
// next boot is clean.
void begin();

// Current pin number for the given id. Returns the resolved value
// after begin() ran. Defined for all valid HalPinId values.
uint8_t pin(HalPinId id);

// Current flag bitmask for the given id. Bit definitions are the
// HAL_FLAG_* constants. Returns 0 for invalid ids.
uint8_t flags(HalPinId id);

// Convenience: true if the ACTIVE_LOW flag is set on the pin.
// Output subsystems use this to choose their idle/asserted GPIO
// levels. The default at firmware level is active-high (LOW = idle,
// HIGH = active); the flag inverts.
bool activeLow(HalPinId id);

// Human-readable name for an id. Used by the protocol commands.
// Returns nullptr for invalid ids. The strings are stable identifiers
// (snake_case, fixed) - they're part of the protocol surface.
const char* pinName(HalPinId id);

// Reverse lookup: name -> id. Returns true on match. Used by
// "SET hal pin <name> <number>".
bool pinIdByName(const char* name, HalPinId& out);

// Where the current map came from (EEPROM or compiled defaults).
HalSource source();

// Stage a pin override. Writes to EEPROM but does NOT change the
// in-memory pin() value - takes effect on next boot. Returns true
// on success, false on bad id or out-of-range pin number.
bool stagePin(HalPinId id, uint8_t pinNumber);

// Stage a flag override. Same staging semantics as stagePin: writes
// to EEPROM, takes effect on next boot. Replaces the entire flag
// byte with the given value (use bit operations on the daemon side
// to compose specific flag bits).
bool stageFlags(HalPinId id, uint8_t flags);

// Wipe EEPROM, restoring compiled defaults. Same staging semantics:
// next boot reads the defaults and rewrites them as EEPROM.
void resetDefaults();

// Soft reboot via the watchdog. Used by SET hal reboot. This
// function does not return (the WDT triggers a reset).
void reboot() __attribute__((noreturn));

}  // namespace hal

#endif  // ZEROTX_IO_HAL_H
