# ZeroTX Hardware Pinout Reference

Pinout for each microcontroller in the ZeroTX case. Values here track
the source files cited at the top of each section. When a definition
moves in the source, update this doc in the same commit.

This first pass covers the three MCUs that are the most complex to
wire: the RP2040, the Mega 2560, and the ESP32 driving the HUB75 LED
panel. Pi 400 USB topology and 12V power distribution are deferred
to a follow-up.

## RP2040 Zero (CRSF generator + arm key)

**Board:** Waveshare RP2040 Zero. Castellated 21-pin edge layout,
onboard WS2812 RGB LED on GP16, USB-C.

**Source of truth:**

- `rp2040/src/crsf.c` (lines 10-11): `CRSF_UART_TX`, `CRSF_UART_RX`
- `rp2040/src/input_arm.h` (line 32): `INPUT_ARM_PIN`
- `rp2040/src/status_led.h` (line 8): `STATUS_LED_PIN`

Pin numbers are compile-time `#define` values. Changing them requires
a firmware reflash.

| GPIO | Direction | Function | Notes |
|------|-----------|----------|-------|
| GP0  | output | UART0 TX to ELRS module (CRSF) | Hardware UART. Series resistor at the end of cable, see hardware-bom |
| GP1  | input  | UART0 RX from ELRS module (CRSF) | Hardware UART, telemetry path |
| GP14 | input  | Aviator-style arm key | Internal pull-up. Switch to GND. Far from UART and LED, no timing-sensitive neighbors |
| GP16 | output | Onboard WS2812 status LED | Hardwired on the Waveshare board, driven by PIO0 |

**Free GPIO** for future expansion: GP2-GP13, GP15, GP17-GP29 (subject
to which pads are accessible on the Zero footprint; GP17-GP25 are on
the bottom solder pads, not the edge headers).

**Caveats:**

- USB CDC is the IPC channel to the daemon. Don't repurpose USB.
- Watchdog hardware-enabled in firmware; main loop must kick within
  the watchdog timeout or the board resets. Relevant if anyone ever
  adds a long-blocking call.

## Arduino Mega 2560 (IO board)

**Board:** Standard Arduino Mega 2560 (Rev3 or pin-compatible clone),
ATmega2560, USB Type-B. The MCU enumerates as `/dev/ttyACM*` over the
onboard USB-serial bridge.

**Source of truth:**

- `firmware/io/src/hal.h` (`enum HalPinId`): the symbolic pin slots
- `firmware/io/src/hal.cpp` (`kHalPinDefaults[]`, lines 61-89): the
  default pin numbers for each slot

Unlike the RP2040 and ESP32, **Mega pin numbers are runtime-configurable**.
The HAL stores the pin map in EEPROM and falls back to the compiled
defaults if EEPROM is blank or corrupt. The daemon can rewrite pin
assignments via the `hal` subsystem protocol commands and reboot the
Mega; no reflash required. The table below is the **default** map.

| Mega pin | Function (HAL slot) | Notes |
|----------|---------------------|-------|
| 0  | USB Serial RX (Serial0) | Hardcoded, NOT in HAL. Protocol channel to the daemon. Don't touch |
| 1  | USB Serial TX (Serial0) | Hardcoded, NOT in HAL. Protocol channel to the daemon. Don't touch |
| 8  | Trackball ring LED, green (`led_trackball_green`) | PWM-capable (Timer 2/4) |
| 9  | Trackball ring LED, red (`led_trackball_red`) | PWM-capable (Timer 2/4) |
| 22 | Relay 0 (`relay_0`) | Default active-high |
| 23 | Relay 1 (`relay_1`) | Default active-high |
| 24 | Relay 2 (`relay_2`) | Default active-high |
| 25 | Relay 3 (`relay_3`) | Default active-high |
| 30 | VFD RS (`vfd0_rs`) | Noritake CU20025ECPB-W1J in 4-bit HD44780 mode |
| 31 | VFD EN (`vfd0_en`) | |
| 32 | VFD D4 (`vfd0_d4`) | |
| 33 | VFD D5 (`vfd0_d5`) | |
| 34 | VFD D6 (`vfd0_d6`) | |
| 35 | VFD D7 (`vfd0_d7`) | |
| 38 | Panel button 0 (`button_0`) | Active-low to GND, internal pull-up |
| 39 | Panel button 1 (`button_1`) | |
| 40 | Panel button 2 (`button_2`) | |
| 41 | Panel button 3 (`button_3`) | |
| 42 | Panel button 4 (`button_4`) | |
| 44 | Indicator LED 0 (`led_0`) | PWM-capable (Timer 5), simple on/off in firmware today |
| 45 | Indicator LED 1 (`led_1`) | PWM-capable (Timer 5) |
| 46 | Indicator LED 2 (`led_2`) | PWM-capable (Timer 5) |
| 47 | Indicator LED 3 (`led_3`) | PWM-capable (Timer 5) |
| 49 | WS2813 strip data (`ws_data`) | |
| 50 | Buzzer (`buzzer`) | Drives passive piezo via `tone()`. **Also SPI MISO** |
| 51 | Encoder A (`enc0_a`) | KY-040 quadrature. **Also SPI MOSI** |
| 52 | Encoder B (`enc0_b`) | KY-040 quadrature. **Also SPI SCK** |
| 53 | Encoder switch (`enc0_sw`) | KY-040 push button. **Also SPI SS** |
| 54 (A0) | LDR ambient-light sensor (`ldr_0`) | Analog input. `analogRead()` accepts the digital pin number directly |

**Free pins** in the default config: 2-7, 10-21, 26-29, 36-37, 43, 48,
A1-A15. The Serial1 (18/19), Serial2 (16/17), and Serial3 (14/15) UARTs
are unused and free.

**Per-pin polarity flags** are also EEPROM-stored. The default for all
outputs is active-high; flip the `ACTIVE_LOW` bit per slot for boards
that need inverted drive (some relay boards, some optocoupler-isolated
inputs).

**Caveats:**

- **Encoder and buzzer occupy the SPI bus** (50-53). Fine while there
  is no other SPI peripheral, but adding an SD card reader, OLED via
  SPI, or any other SPI device requires remapping these slots first.
  All four are HAL-configurable, so this is a config change, not a
  hardware swap.
- **Pins 0/1 are reserved** for the USB-serial protocol channel and
  intentionally hardcoded out of HAL: if they could be remapped, the
  daemon couldn't recover from a bricked config.
- HAL EEPROM layout is versioned (`HAL_EEPROM_VERSION` in `hal.cpp`).
  Reordering `HalPinId` values bumps the version and invalidates older
  EEPROM contents.

## ESP32 DevKit V1 (HUB75 LED panel driver)

**Board:** ESP32 DevKit V1 (DOIT-style 30-pin), ESP32-WROOM-32 module,
mini-USB connector. The older variant; not the S3, S2, or C3.

**Source of truth:**

- `firmware/display/src/main.cpp` (lines 595-612): explicit pin
  assignments via `HUB75_I2S_CFG`
- ESP32-HUB75-MatrixPanel-DMA library defaults: pins not overridden in
  `main.cpp` use the library's compiled-in defaults

**Panel:** two chained Waveshare P2.5 64x32 RGB panels (1/16 scan), for
a 128x32 logical surface. Note that Waveshare 2.5mm-pitch panels swap
GREEN and BLUE channels relative to the standard HUB75 pinout; the
firmware accounts for this in the pin remap below.

| ESP32 GPIO | HUB75 signal | Source | Notes |
|------------|--------------|--------|-------|
| GPIO 25 | R1 (red, top half) | explicit | |
| GPIO 27 | G1 (green, top half) | explicit | Wired to G1 even though library names this slot "B1"; firmware swaps to keep RGB565 colors meaningful |
| GPIO 26 | B1 (blue, top half) | explicit | Library "G1" slot |
| GPIO 14 | R2 (red, bottom half) | explicit | |
| GPIO 13 | G2 (green, bottom half) | explicit | Library "B2" slot |
| GPIO 12 | B2 (blue, bottom half) | explicit | Library "G2" slot |
| GPIO 23 | A (address bit 0) | library default | |
| GPIO 19 | B (address bit 1) | library default | |
| GPIO 5  | C (address bit 2) | library default | |
| GPIO 17 | D (address bit 3) | library default | |
| (unused) | E (address bit 4) | explicit `gpio.e = -1` | 1/16 scan panels don't have an E pin |
| GPIO 4  | LAT (latch) | library default | |
| GPIO 15 | OE (output enable) | library default | |
| GPIO 16 | CLK (clock) | library default | |

**Caveats:**

- **G/B swap is panel-specific.** If a future build uses non-Waveshare
  panels with the standard pinout, the firmware swap must be removed
  in `main.cpp:606-607`.
- **Strapping pins.** GPIO 5, 12, and 15 are ESP32 boot strapping pins.
  GPIO 12 must read LOW at boot (otherwise the chip selects the wrong
  flash voltage); HUB75 R2 on GPIO 14 is fine, but watch for any
  pull-ups on the panel side fighting the boot state. So far the
  default panel wiring boots cleanly.
- **GPIO 6-11** are wired internally to the SPI flash. Never use them.
- **GPIO 1 and 3** are the USB-serial UART (TX0/RX0). The line protocol
  to the daemon runs on this UART; don't repurpose.
- **Library defaults are accepted by silence.** If you ever swap the
  HUB75 library version, re-verify that A/B/C/D/LAT/OE/CLK defaults
  haven't moved. The version pinned in `firmware/display/platformio.ini`
  is the one this doc tracks.

**Free pins** suitable for general use on this board: GPIO 2, 18, 21,
22, 32, 33. ADC-only (no output): 34, 35, 36, 39. The latter four are
input-only and lack internal pull-ups, useful for analog or buttons
with external pull-ups.

## Deferred to a follow-up

- Pi 400 USB topology: which physical port carries which MCU, udev
  rules, `/dev/serial/by-id/` paths.
- 12V power distribution: PSU output, buck regulators, per-MCU rail
  assignments and current budgets.
- External case I/O: pole cable bulkhead pinout, front-panel jacks,
  mains/12V input.
