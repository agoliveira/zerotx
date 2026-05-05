// pinmap.h - central pin assignments for the IO board.
//
// All physical pin numbers live here so re-routing is a one-file
// change. Subsystem code references the named constants, never raw
// pin numbers. Comments document what is wired where, including any
// hardware notes (active-low? pull-up? transistor switch?).
//
// Mega 2560 pin reference:
//   - Digital GPIO: 0-53
//   - Analog input: A0-A15  (also usable as digital 54-69)
//   - PWM-capable:  2-13, 44-46
//   - External INT: 2 (INT0), 3 (INT1), 18 (INT5), 19 (INT4),
//                   20 (INT3, SDA), 21 (INT2, SCL)
//   - SPI:          50 (MISO), 51 (MOSI), 52 (SCK), 53 (SS)
//   - I2C:          20 (SDA), 21 (SCL)
//   - HW Serial1:   18 (TX1), 19 (RX1)
//   - HW Serial2:   16 (TX2), 17 (RX2)
//   - HW Serial3:   14 (TX3), 15 (RX3)
//
// Reserved pins: 0/1 are USB Serial0 (the protocol channel to the
// daemon). Do not assign these.

#ifndef ZEROTX_IO_PINMAP_H
#define ZEROTX_IO_PINMAP_H

#include <Arduino.h>

namespace pinmap {

// ---------------------------------------------------------------------
// Trackball LEDs (active-low to ground, switched via NPN transistors
// from the Mega GPIO. Pin drives the transistor base via a series
// resistor; collector pulls the LED-cathode line to GND when ON).
// ---------------------------------------------------------------------
constexpr uint8_t LED_TRACKBALL_GREEN = 8;
constexpr uint8_t LED_TRACKBALL_RED   = 9;

// ---------------------------------------------------------------------
// VFD (Noritake CU20025ECPB-W1J, parallel mode). Pin numbers TBD by
// hardware build; placeholder block below is for scaffolding only and
// will be filled in when the subsystem is implemented. The current
// Pro Micro firmware uses specific pins that can be remapped freely
// to any Mega digital pin; the parallel bus needs 8 data + ~3 control
// lines.
// ---------------------------------------------------------------------
// constexpr uint8_t VFD0_DATA_BASE = ?;  // 8 contiguous data pins
// constexpr uint8_t VFD0_RS        = ?;
// constexpr uint8_t VFD0_RW        = ?;
// constexpr uint8_t VFD0_E         = ?;

// ---------------------------------------------------------------------
// Buzzer (PWM output to a small piezo or active buzzer; assumes the
// buzzer common is on the buzzer pin and the other pin to GND).
// ---------------------------------------------------------------------
// constexpr uint8_t BUZZER = 7;  // PWM-capable

// ---------------------------------------------------------------------
// LDR (light-dependent resistor) ambient light sensor. Wired as a
// voltage divider into one of the analog inputs.
// ---------------------------------------------------------------------
// constexpr uint8_t SENSOR_LDR = A0;

// ---------------------------------------------------------------------
// WS2813 strip data line. Single output; the protocol addresses
// individual pixels by index.
// ---------------------------------------------------------------------
// constexpr uint8_t WS_DATA = 6;

// ---------------------------------------------------------------------
// Buttons. Each entry is one pin with INPUT_PULLUP; press = LOW.
// External-INT-capable pins preferred for low-latency edge detection,
// but polling at the loop cadence is also fine.
// ---------------------------------------------------------------------
// constexpr uint8_t BUTTON_0 = 18;  // INT5

}  // namespace pinmap

#endif  // ZEROTX_IO_PINMAP_H
