// hal.cpp - HAL pin map implementation (EEPROM layout v2).
//
// EEPROM layout v2:
//
//   [0..3]   magic = 0x5A455243 ("ZERC" little-endian)
//   [4]      version = HAL_EEPROM_VERSION (2)
//   [5]      stored count (must match compile-time HAL_PIN_COUNT)
//   [6..6+2N-1]  N entries, 2 bytes each: { pin_number, flags }
//   [end..end+1] CRC16 over preceding bytes
//
// Validation order on boot:
//   1. Magic. If wrong, fallback.
//   2. Version. If wrong, fallback (covers v1->v2 upgrade: old
//      single-byte entries cannot be read as new two-byte entries
//      reliably, so we just rewrite defaults).
//   3. Count. If != HAL_PIN_COUNT, fallback.
//   4. Pin/flag bytes + CRC. If mismatch, fallback.
//   5. Otherwise populate g_pins / g_flags from EEPROM.
//
// Fallback writes the defaults back to EEPROM so the next boot is
// clean and the source becomes EEPROM. Any pre-existing v1 EEPROM
// content is silently superseded.

#include "hal.h"

#include <EEPROM.h>
#include <avr/wdt.h>
#include <string.h>

namespace hal {

// ----- Constants --------------------------------------------------------

static constexpr uint16_t HAL_EEPROM_BASE    = 0;
static constexpr uint32_t HAL_EEPROM_MAGIC   = 0x5A455243UL;
static constexpr uint8_t  HAL_EEPROM_VERSION = 2;

// Bytes per pin entry in EEPROM v2 (pin_number + flags).
static constexpr uint8_t HAL_ENTRY_BYTES = 2;

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
// secondary header zone. Relays default to pins 22-25.
//
// All flags default to 0 (active-high). ACTIVE_LOW is set per-pin
// via SET hal flag for boards that wire their inputs through an
// inverting transistor stage.
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
  /* HAL_RELAY_0             */ 22,
  /* HAL_RELAY_1             */ 23,
  /* HAL_RELAY_2             */ 24,
  /* HAL_RELAY_3             */ 25,
  /* HAL_LDR_0               */ 54,  // = A0 on the Mega
  /* HAL_BUZZER              */ 50,
  /* HAL_ENC0_A              */ 51,
  /* HAL_ENC0_B              */ 52,
  /* HAL_ENC0_SW             */ 53,
};

// Default flags: 0 across the board (active-high). Operator can flip
// individual entries via SET hal flag <name> 1 if their wiring
// requires active-low.
static const uint8_t kHalFlagDefaults[HAL_PIN_COUNT] = {0};

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
  /* HAL_RELAY_0             */ "relay_0",
  /* HAL_RELAY_1             */ "relay_1",
  /* HAL_RELAY_2             */ "relay_2",
  /* HAL_RELAY_3             */ "relay_3",
  /* HAL_LDR_0               */ "ldr_0",
  /* HAL_BUZZER              */ "buzzer",
  /* HAL_ENC0_A              */ "enc0_a",
  /* HAL_ENC0_B              */ "enc0_b",
  /* HAL_ENC0_SW             */ "enc0_sw",
};

// ----- Module state -----------------------------------------------------

static uint8_t   g_pins[HAL_PIN_COUNT];
static uint8_t   g_flags[HAL_PIN_COUNT];
static HalSource g_source = HAL_SOURCE_DEFAULTS;

// ----- CRC16 (CCITT, polynomial 0x1021) ---------------------------------

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
//
// Header is 6 bytes (magic + version + count), followed by 2 bytes
// per pin entry, followed by 2 bytes of CRC. Total = 6 + 2N + 2.

static constexpr size_t kPayloadBytes = 6 + HAL_ENTRY_BYTES * HAL_PIN_COUNT;

static void writeBufToEEPROM(const uint8_t* buf, uint16_t crc) {
  uint16_t addr = HAL_EEPROM_BASE;
  for (size_t i = 0; i < kPayloadBytes; ++i) {
    EEPROM.update(addr++, buf[i]);
  }
  EEPROM.update(addr++, static_cast<uint8_t>(crc & 0xFF));
  EEPROM.update(addr++, static_cast<uint8_t>((crc >> 8) & 0xFF));
}

static void writeDefaultsToEEPROM() {
  uint8_t buf[kPayloadBytes];
  buf[0] = static_cast<uint8_t>(HAL_EEPROM_MAGIC & 0xFF);
  buf[1] = static_cast<uint8_t>((HAL_EEPROM_MAGIC >> 8) & 0xFF);
  buf[2] = static_cast<uint8_t>((HAL_EEPROM_MAGIC >> 16) & 0xFF);
  buf[3] = static_cast<uint8_t>((HAL_EEPROM_MAGIC >> 24) & 0xFF);
  buf[4] = HAL_EEPROM_VERSION;
  buf[5] = HAL_PIN_COUNT;
  for (uint8_t i = 0; i < HAL_PIN_COUNT; ++i) {
    buf[6 + 2*i + 0] = kHalPinDefaults[i];
    buf[6 + 2*i + 1] = kHalFlagDefaults[i];
  }
  writeBufToEEPROM(buf, crc16(buf, sizeof(buf)));
}

static bool tryLoadFromEEPROM() {
  uint16_t addr = HAL_EEPROM_BASE;

  uint8_t buf[kPayloadBytes];
  for (size_t i = 0; i < kPayloadBytes; ++i) {
    buf[i] = EEPROM.read(addr++);
  }
  uint8_t crcLo = EEPROM.read(addr++);
  uint8_t crcHi = EEPROM.read(addr++);
  uint16_t storedCrc = static_cast<uint16_t>(crcLo) | (static_cast<uint16_t>(crcHi) << 8);

  uint32_t magic = static_cast<uint32_t>(buf[0])
                 | (static_cast<uint32_t>(buf[1]) << 8)
                 | (static_cast<uint32_t>(buf[2]) << 16)
                 | (static_cast<uint32_t>(buf[3]) << 24);
  if (magic != HAL_EEPROM_MAGIC) return false;
  if (buf[4] != HAL_EEPROM_VERSION) return false;
  if (buf[5] != HAL_PIN_COUNT) return false;

  if (crc16(buf, sizeof(buf)) != storedCrc) return false;

  // Sanity check pin numbers - reject obviously bad values.
  for (uint8_t i = 0; i < HAL_PIN_COUNT; ++i) {
    uint8_t pin = buf[6 + 2*i + 0];
    if (pin < HAL_PIN_MIN || pin > HAL_PIN_MAX) return false;
  }

  for (uint8_t i = 0; i < HAL_PIN_COUNT; ++i) {
    g_pins[i]  = buf[6 + 2*i + 0];
    g_flags[i] = buf[6 + 2*i + 1];
  }
  return true;
}

// ----- Public API -------------------------------------------------------

void begin() {
  if (tryLoadFromEEPROM()) {
    g_source = HAL_SOURCE_EEPROM;
    return;
  }
  memcpy(g_pins,  kHalPinDefaults, HAL_PIN_COUNT);
  memcpy(g_flags, kHalFlagDefaults, HAL_PIN_COUNT);
  writeDefaultsToEEPROM();
  g_source = HAL_SOURCE_DEFAULTS;
}

uint8_t pin(HalPinId id) {
  if (id >= HAL_PIN_COUNT) return 0xFF;
  return g_pins[id];
}

uint8_t flags(HalPinId id) {
  if (id >= HAL_PIN_COUNT) return 0;
  return g_flags[id];
}

bool activeLow(HalPinId id) {
  return (flags(id) & HAL_FLAG_ACTIVE_LOW) != 0;
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

// Read current EEPROM record into a buffer for staging. Caller-
// owned mutation; we then recompute CRC and write back.
static void readPayloadFromEEPROM(uint8_t* buf) {
  uint16_t addr = HAL_EEPROM_BASE;
  for (size_t i = 0; i < kPayloadBytes; ++i) {
    buf[i] = EEPROM.read(addr++);
  }
}

bool stagePin(HalPinId id, uint8_t pinNumber) {
  if (id >= HAL_PIN_COUNT) return false;
  if (pinNumber < HAL_PIN_MIN || pinNumber > HAL_PIN_MAX) return false;

  uint8_t buf[kPayloadBytes];
  readPayloadFromEEPROM(buf);
  buf[6 + 2*id + 0] = pinNumber;
  writeBufToEEPROM(buf, crc16(buf, sizeof(buf)));
  return true;
}

bool stageFlags(HalPinId id, uint8_t newFlags) {
  if (id >= HAL_PIN_COUNT) return false;

  uint8_t buf[kPayloadBytes];
  readPayloadFromEEPROM(buf);
  buf[6 + 2*id + 1] = newFlags;
  writeBufToEEPROM(buf, crc16(buf, sizeof(buf)));
  return true;
}

void resetDefaults() {
  // Wipe just the bytes we know about; cheaper than full clear() and
  // also avoids touching EEPROM regions other code might use later.
  uint16_t addr = HAL_EEPROM_BASE;
  size_t total = kPayloadBytes + 2;
  for (size_t i = 0; i < total; ++i) {
    EEPROM.update(addr++, 0xFF);
  }
}

void reboot() {
  cli();
  wdt_enable(WDTO_15MS);
  for (;;) {}
}

}  // namespace hal
