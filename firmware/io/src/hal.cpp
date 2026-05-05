// hal.cpp - HAL pin map implementation.
//
// EEPROM layout:
//
//   [0..3]   magic = 0x5A455243 ("ZERC" little-endian when read as 32-bit)
//   [4]      version = HAL_EEPROM_VERSION (1)
//   [5]      stored count (must match compile-time HAL_PIN_COUNT)
//   [6..N]   pin assignments, one byte each, indexed by HalPinId
//   [N+1..N+2] CRC16 over [0..N]
//
// Validation order on boot:
//   1. Read magic. If wrong, fallback.
//   2. Read version. If wrong, fallback.
//   3. Read count. If != HAL_PIN_COUNT, fallback (firmware revision
//      changed the pin set; old map is no longer valid).
//   4. Read pin bytes + CRC. If CRC mismatch, fallback.
//   5. Otherwise, populate g_pins from EEPROM.
//
// Fallback writes the defaults back to EEPROM so the next boot is
// clean and the source becomes EEPROM.

#include "hal.h"

#include <EEPROM.h>
#include <avr/wdt.h>
#include <string.h>

namespace hal {

// ----- Constants --------------------------------------------------------

static constexpr uint16_t HAL_EEPROM_BASE   = 0;
static constexpr uint32_t HAL_EEPROM_MAGIC  = 0x5A455243UL;
static constexpr uint8_t  HAL_EEPROM_VERSION = 1;

// Maximum legal pin number on a Mega 2560. Pins 0/1 are reserved
// (USB Serial0). Pins above 69 don't exist.
static constexpr uint8_t HAL_PIN_MIN = 2;
static constexpr uint8_t HAL_PIN_MAX = 69;

// ----- Compiled defaults ------------------------------------------------
//
// Indexed by HalPinId. New entries match the order in the enum.
//
// VFD defaults (pins 30-35) chosen as a contiguous block on the
// Mega's secondary header, away from special-purpose pins (PWM,
// external interrupts, hardware UARTs/SPI/I2C). Easy to relocate via
// SET hal pin if the case wiring needs different pins.
//
// Buttons (38-42), LEDs (44-47), WS2813 data (49) are also in the
// secondary header zone. None of these need PWM or interrupt
// capability so any digital pin works.
static const uint8_t kHalPinDefaults[HAL_PIN_COUNT] = {
  /* HAL_LED_TRACKBALL_GREEN */ 8,
  /* HAL_LED_TRACKBALL_RED   */ 9,
  /* HAL_VFD0_RS             */ 30,
  /* HAL_VFD0_EN             */ 31,
  /* HAL_VFD0_D4             */ 32,
  /* HAL_VFD0_D5             */ 33,
  /* HAL_VFD0_D6             */ 34,
  /* HAL_VFD0_D7             */ 35,
  /* HAL_BUTTON_0            */ 38,
  /* HAL_BUTTON_1            */ 39,
  /* HAL_BUTTON_2            */ 40,
  /* HAL_BUTTON_3            */ 41,
  /* HAL_BUTTON_4            */ 42,
  /* HAL_LED_0               */ 44,
  /* HAL_LED_1               */ 45,
  /* HAL_LED_2               */ 46,
  /* HAL_LED_3               */ 47,
  /* HAL_WS_DATA             */ 49,
};

// Stable string names for the protocol surface. Indexed by HalPinId.
static const char* const kHalPinNames[HAL_PIN_COUNT] = {
  /* HAL_LED_TRACKBALL_GREEN */ "led_trackball_green",
  /* HAL_LED_TRACKBALL_RED   */ "led_trackball_red",
  /* HAL_VFD0_RS             */ "vfd0_rs",
  /* HAL_VFD0_EN             */ "vfd0_en",
  /* HAL_VFD0_D4             */ "vfd0_d4",
  /* HAL_VFD0_D5             */ "vfd0_d5",
  /* HAL_VFD0_D6             */ "vfd0_d6",
  /* HAL_VFD0_D7             */ "vfd0_d7",
  /* HAL_BUTTON_0            */ "button_0",
  /* HAL_BUTTON_1            */ "button_1",
  /* HAL_BUTTON_2            */ "button_2",
  /* HAL_BUTTON_3            */ "button_3",
  /* HAL_BUTTON_4            */ "button_4",
  /* HAL_LED_0               */ "led_0",
  /* HAL_LED_1               */ "led_1",
  /* HAL_LED_2               */ "led_2",
  /* HAL_LED_3               */ "led_3",
  /* HAL_WS_DATA             */ "ws_data",
};

// ----- Module state -----------------------------------------------------

static uint8_t   g_pins[HAL_PIN_COUNT];
static HalSource g_source = HAL_SOURCE_DEFAULTS;

// ----- CRC16 (CCITT, polynomial 0x1021) ---------------------------------
//
// Lightweight, no table; ~50 bytes flash, fast enough for ~20 bytes
// of EEPROM payload.

static uint16_t crc16(const uint8_t* data, size_t len) {
  uint16_t crc = 0xFFFF;
  for (size_t i = 0; i < len; ++i) {
    crc ^= static_cast<uint16_t>(data[i]) << 8;
    for (uint8_t b = 0; b < 8; ++b) {
      crc = (crc & 0x8000) ? (crc << 1) ^ 0x1021 : (crc << 1);
    }
  }
  return crc;
}

// ----- EEPROM I/O -------------------------------------------------------

static void writeDefaultsToEEPROM() {
  uint16_t addr = HAL_EEPROM_BASE;

  // Compose header + payload into a small staging buffer to compute
  // CRC in one pass.
  uint8_t buf[6 + HAL_PIN_COUNT];
  buf[0] = static_cast<uint8_t>(HAL_EEPROM_MAGIC & 0xFF);
  buf[1] = static_cast<uint8_t>((HAL_EEPROM_MAGIC >> 8) & 0xFF);
  buf[2] = static_cast<uint8_t>((HAL_EEPROM_MAGIC >> 16) & 0xFF);
  buf[3] = static_cast<uint8_t>((HAL_EEPROM_MAGIC >> 24) & 0xFF);
  buf[4] = HAL_EEPROM_VERSION;
  buf[5] = HAL_PIN_COUNT;
  for (uint8_t i = 0; i < HAL_PIN_COUNT; ++i) {
    buf[6 + i] = kHalPinDefaults[i];
  }
  uint16_t crc = crc16(buf, sizeof(buf));

  for (size_t i = 0; i < sizeof(buf); ++i) {
    EEPROM.update(addr++, buf[i]);
  }
  EEPROM.update(addr++, static_cast<uint8_t>(crc & 0xFF));
  EEPROM.update(addr++, static_cast<uint8_t>((crc >> 8) & 0xFF));
}

static bool tryLoadFromEEPROM() {
  uint16_t addr = HAL_EEPROM_BASE;

  uint8_t header[6];
  for (size_t i = 0; i < sizeof(header); ++i) {
    header[i] = EEPROM.read(addr++);
  }
  uint32_t magic = static_cast<uint32_t>(header[0])
                 | (static_cast<uint32_t>(header[1]) << 8)
                 | (static_cast<uint32_t>(header[2]) << 16)
                 | (static_cast<uint32_t>(header[3]) << 24);
  if (magic != HAL_EEPROM_MAGIC) return false;
  if (header[4] != HAL_EEPROM_VERSION) return false;
  if (header[5] != HAL_PIN_COUNT) return false;

  uint8_t pins[HAL_PIN_COUNT];
  for (uint8_t i = 0; i < HAL_PIN_COUNT; ++i) {
    pins[i] = EEPROM.read(addr++);
  }
  uint8_t crcLo = EEPROM.read(addr++);
  uint8_t crcHi = EEPROM.read(addr++);
  uint16_t storedCrc = static_cast<uint16_t>(crcLo) | (static_cast<uint16_t>(crcHi) << 8);

  uint8_t allBuf[6 + HAL_PIN_COUNT];
  memcpy(allBuf, header, 6);
  memcpy(allBuf + 6, pins, HAL_PIN_COUNT);
  uint16_t computedCrc = crc16(allBuf, sizeof(allBuf));
  if (storedCrc != computedCrc) return false;

  // Sanity check pin numbers - reject obviously bad values; recovery
  // requires the daemon to issue SET hal reset-defaults afterwards.
  for (uint8_t i = 0; i < HAL_PIN_COUNT; ++i) {
    if (pins[i] < HAL_PIN_MIN || pins[i] > HAL_PIN_MAX) return false;
  }

  memcpy(g_pins, pins, HAL_PIN_COUNT);
  return true;
}

// ----- Public API -------------------------------------------------------

void begin() {
  if (tryLoadFromEEPROM()) {
    g_source = HAL_SOURCE_EEPROM;
    return;
  }
  // Fallback: copy defaults into RAM, then mirror to EEPROM so next
  // boot is clean.
  memcpy(g_pins, kHalPinDefaults, HAL_PIN_COUNT);
  writeDefaultsToEEPROM();
  g_source = HAL_SOURCE_DEFAULTS;
}

uint8_t pin(HalPinId id) {
  if (id >= HAL_PIN_COUNT) return 0xFF;
  return g_pins[id];
}

const char* pinName(HalPinId id) {
  if (id >= HAL_PIN_COUNT) return nullptr;
  return kHalPinNames[id];
}

bool pinIdByName(const char* name, HalPinId& out) {
  if (!name) return false;
  for (uint8_t i = 0; i < HAL_PIN_COUNT; ++i) {
    if (strcmp(name, kHalPinNames[i]) == 0) {
      out = static_cast<HalPinId>(i);
      return true;
    }
  }
  return false;
}

HalSource source() {
  return g_source;
}

bool stagePin(HalPinId id, uint8_t pinNumber) {
  if (id >= HAL_PIN_COUNT) return false;
  if (pinNumber < HAL_PIN_MIN || pinNumber > HAL_PIN_MAX) return false;

  // Read the current EEPROM record, modify the one byte, recompute
  // CRC, write back. Header is already valid because begin() either
  // loaded a valid record OR wrote defaults.
  uint8_t buf[6 + HAL_PIN_COUNT];
  uint16_t addr = HAL_EEPROM_BASE;
  for (size_t i = 0; i < sizeof(buf); ++i) {
    buf[i] = EEPROM.read(addr++);
  }
  buf[6 + id] = pinNumber;
  uint16_t crc = crc16(buf, sizeof(buf));

  addr = HAL_EEPROM_BASE;
  for (size_t i = 0; i < sizeof(buf); ++i) {
    EEPROM.update(addr++, buf[i]);
  }
  EEPROM.update(addr++, static_cast<uint8_t>(crc & 0xFF));
  EEPROM.update(addr++, static_cast<uint8_t>((crc >> 8) & 0xFF));
  return true;
}

void resetDefaults() {
  // Wipe just the bytes we know about; cheaper than full clear() and
  // also avoids touching EEPROM regions other code might use later.
  uint16_t addr = HAL_EEPROM_BASE;
  size_t total = 6 + HAL_PIN_COUNT + 2;
  for (size_t i = 0; i < total; ++i) {
    EEPROM.update(addr++, 0xFF);
  }
  // Next boot will see bad magic, fall back to defaults, and rewrite.
}

void reboot() {
  // Use the watchdog to force a clean reset. Disable interrupts so
  // nothing kicks the WDT before it fires; 15ms timeout is shorter
  // than the main-loop kick cadence guarantee.
  cli();
  wdt_enable(WDTO_15MS);
  for (;;) {}
}

}  // namespace hal
