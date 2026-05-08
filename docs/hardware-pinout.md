# ZeroTX Hardware Pinout Reference

Pinout, USB topology, and power distribution for the ZeroTX case.
Values here track the source files cited at the top of each section
(for the firmware) or the physical layout of the case (for USB and
power). When a definition moves in the source or the wiring changes,
update this doc in the same commit.

The MCU sections come first (most complex to wire), followed by the
case-level USB topology, the power distribution tree, and the
external pole cable.

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
- `firmware/io/src/hal.cpp` (`kHalPinDefaults[]`): the default pin
  numbers for each slot

Unlike the RP2040 and ESP32, **Mega pin numbers are runtime-configurable**.
The HAL stores the pin map in EEPROM and falls back to the compiled
defaults if EEPROM is blank, the magic number is wrong, or the version
doesn't match. The daemon can rewrite pin assignments via the `hal`
subsystem protocol commands and reboot the Mega; no reflash required.
The table below is the **default** map.

| Mega pin | Function (HAL slot) | Notes |
|----------|---------------------|-------|
| 0  | USB Serial RX (Serial0) | Hardcoded, NOT in HAL. Protocol channel to the daemon. Don't touch |
| 1  | USB Serial TX (Serial0) | Hardcoded, NOT in HAL. Protocol channel to the daemon. Don't touch |
| 2  | Encoder A (`enc0_a`) | INT0, hardware-interrupt capable |
| 3  | Encoder B (`enc0_b`) | INT1, hardware-interrupt capable |
| 4  | Encoder SW (`enc0_sw`) | KY-040 push button |
| 5  | Buzzer (`buzzer`) | Drives passive piezo via `tone()`. `tone()` retargets timer at runtime regardless of the pin |
| 6  | Servo 0 (`servo_0`) | Driven by Servo subsystem (`servo.0`); Arduino Servo library |
| 7  | Servo 1 (`servo_1`) | `servo.1` |
| 8  | Servo 2 (`servo_2`) | `servo.2` |
| 9  | Servo 3 (`servo_3`) | `servo.3` |
| 11 | Trackball ring LED, green (`led_trackball_green`) | Timer 1 PWM, off Timer 2 (which `tone()` uses) |
| 12 | Trackball ring LED, red (`led_trackball_red`) | Timer 1 PWM |
| 14, 15 | Serial3 TX/RX | Free for future use |
| 16, 17 | Serial2 TX/RX | Free for future use |
| 18, 19 | Serial1 TX/RX (also INT3, INT2) | Free for future use |
| 20, 21 | I2C SDA, SCL | Hardware I2C bus. Not in HAL (hardware-fixed pins). I2cLcd subsystem (`lcd.0`) lives here, default auto-detect address |
| 22 | Relay 0 (`relay_0`) | Default active-high |
| 23 | Relay 1 (`relay_1`) | Default active-high |
| 24 | Relay 2 (`relay_2`) | Default active-high |
| 25 | Relay 3 (`relay_3`) | Default active-high |
| 26 | Panel button 0 (`button_0`) | Active-low to GND, internal pull-up |
| 27 | Panel button 1 (`button_1`) | |
| 28 | Panel button 2 (`button_2`) | |
| 29 | Panel button 3 (`button_3`) | |
| 30 | Panel button 4 (`button_4`) | |
| 31 | Panel button 5 (`button_5`) | |
| 32 | Panel button 6 (`button_6`) | |
| 33 | Panel button 7 (`button_7`) | |
| 34 | Panel button 8 (`button_8`) | |
| 35 | Panel button 9 (`button_9`) | |
| 36 | Indicator LED 0 (`led_0`) | On/off in firmware today |
| 37 | Indicator LED 1 (`led_1`) | |
| 38 | Indicator LED 2 (`led_2`) | |
| 39 | Indicator LED 3 (`led_3`) | |
| 40 | WS2813 strip data (`ws_data`) | |
| 44 | VFD0 RS (`vfd0_rs`) | Noritake CU20025ECPB-W1J in 4-bit HD44780 mode |
| 45 | VFD0 EN (`vfd0_en`) | |
| 46 | VFD0 D4 (`vfd0_d4`) | |
| 47 | VFD0 D5 (`vfd0_d5`) | |
| 48 | VFD0 D6 (`vfd0_d6`) | |
| 49 | VFD0 D7 (`vfd0_d7`) | |
| 50, 51, 52, 53 | SPI MISO, MOSI, SCK, SS | Free for future SPI peripheral (SD card, SPI display, etc.) |
| 54 (A0) | LDR ambient-light sensor (`ldr_0`) | Analog input |
| 56 (A2) | VFD1 RS (`vfd1_rs`) | Second VFD via Vfd subsystem (`vfd.1`) |
| 57 (A3) | VFD1 EN (`vfd1_en`) | |
| 58 (A4) | VFD1 D4 (`vfd1_d4`) | |
| 59 (A5) | VFD1 D5 (`vfd1_d5`) | |
| 60 (A6) | VFD1 D6 (`vfd1_d6`) | |
| 61 (A7) | VFD1 D7 (`vfd1_d7`) | |

**Unused pins** in the default config: 10, 13 (also onboard LED), 41,
42, 43, and analog A1, A8-A15 (9 free analog channels). Pins 14-19
(Serial1/2/3) are reserved free for future UART expansion. Pins 50-53
(SPI bus) are reserved free.

**Per-pin polarity flags** are also EEPROM-stored. The default for all
outputs is active-high; flip the `ACTIVE_LOW` bit per slot for boards
that need inverted drive (some relay boards, some optocoupler-isolated
inputs).

**Caveats:**

- **Pins 0/1 are reserved** for the USB-serial protocol channel and
  intentionally hardcoded out of HAL: if they could be remapped, the
  daemon couldn't recover from a bricked config.
- **Servo library uses Timer 5.** Once the Servo subsystem attaches
  any servo, hardware PWM on pins 44/45/46 disappears. Today VFD0
  lives on those pins as plain digital outputs (no PWM needed for
  HD44780), so the conflict has no practical effect. Worth knowing
  if you ever want to repurpose those pins for PWM-driven loads.
  The Servo subsystem uses lazy attach: pins are not grabbed until
  the first SET servo.<n> command, so unattached servos do not
  steal Timer 5.
- **HAL EEPROM layout is versioned** (`HAL_EEPROM_VERSION` in
  `hal.cpp`). Bumping it invalidates older EEPROM contents, so a
  freshly-flashed Mega comes up on the new defaults. Operator-saved
  pin maps from a previous version are lost; re-issue any custom
  `SET hal pin ...` commands as needed.

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

## Pi 400 GPIO breakout

The Pi 400 exposes the standard 40-pin Raspberry Pi GPIO header on the
back edge. ZeroTX uses a passive breakout board for access. The header
follows the Pi 4 pinout and uses BCM GPIO numbering in software (which
does not match the physical pin numbers on the header).

| Header pin | Function | Notes |
|------------|----------|-------|
| 1 | 3V3 power | Reference for I2C pull-ups; also feeds the DS3231 RTC module |
| 3 | GPIO 2 (I2C1 SDA) | Shared I2C bus: DS3231 RTC at addr 0x68. Reserved for future I2C peripherals on the same bus |
| 5 | GPIO 3 (I2C1 SCL) | Shared I2C bus, paired with SDA above |
| 6 | GND | RTC and GPS ground; common with rest of breakout |
| 7 | GPIO 4 (UART3 TXD) | Pi -> GPS module RX. Enabled by `dtoverlay=uart3` |
| 9 | GND | Heartbeat LED ground return |
| 11 | GPIO 17 | Daemon heartbeat LED (active-high). Drive a 1k series resistor + LED to GND |
| 29 | GPIO 5 (UART3 RXD) | GPS module TX -> Pi |
| 14, 20, 25, 30, 34, 39 | GND | Additional ground points; use whichever is closest |

Software notes:

- Heartbeat LED is driven by `internal/heartbeat/` via the
  `github.com/warthog618/go-gpiocdev` library (Linux GPIO character
  device API). The daemon flag `-heartbeat-gpio 17` enables it; the
  default `-1` disables. While the daemon's 50Hz mapper loop is
  healthy, the LED blinks at 1Hz. Loop hang past 1.5s forces the LED
  low, daemon dead means the LED is dark.
- DS3231 RTC is an external module (typically a small board with the
  chip plus a CR2032 battery; e.g. the common Mercado Livre listing).
  Wired to header pins 1/3/5/6. Handled by the kernel via
  `dtoverlay=i2c-rtc,ds3231` in `/boot/firmware/config.txt`. The
  daemon does not read or write the RTC; it just logs whether the
  kernel detected one at startup. Setup procedure: `docs/BOOTSTRAP.md`.
- GPS is an optional Pi-attached serial module (u-blox M6/M7/M10 or
  equivalent NMEA TTL device) on UART3. The daemon flag `-gps-port`
  (e.g. `/dev/ttyAMA1`) enables reading; `-gps-baud` sets the rate
  (default 9600). Failure to open the port is non-fatal: the daemon
  logs and continues. UART3 needs `dtoverlay=uart3` in
  `/boot/firmware/config.txt`. Setup procedure: `docs/BOOTSTRAP.md`.

**Free pins** on the breakout that ZeroTX does not currently use:
GPIO 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 18, 19, 20, 21, 22, 23,
24, 25, 26, 27. SPI0 is on GPIO 8/9/10/11; UART0 is on GPIO 14/15;
PCM/I2S is on GPIO 18/19/20/21. Reserve those banks when planning
future expansions (I2S DAC, additional UARTs, etc.) rather than
picking pins by free-from-function logic alone.

## Pi 400 USB topology

The Pi 400 has 3 USB ports total. Allocation:

| Pi 400 port | Device | Notes |
|-------------|--------|-------|
| 1 | OS / code USB drive | The Pi boots and runs from this drive. Keep it on its own port so the OS isn't sharing bandwidth with anything chatty |
| 2 | RP2040 Zero | CRSF generator. Direct connection (no hub between Pi and RP2040) so the radio link doesn't share USB hub bandwidth or power with peripherals |
| 3 | 7-port powered USB hub | Everything else. Hub has its own 5V external power (does not pull from the Pi); see Power distribution |

### USB hub allocation

| Hub port | Device | Notes |
|----------|--------|-------|
| 1 | ESP32 DevKit V1 | HUB75 LED panel driver |
| 2 | Mega 2560 | IO board (VFD, trackball LEDs, buttons, relays, encoder, buzzer, LDR, WS2813 strip) |
| 3 | USB joystick passthrough | Routes via an internal USB-A extension cable to a panel-mount USB-A on the front of the case. Operator plugs in their X-HOTAS (or any class-compliant USB joystick) |
| 4 | Trackball | USB HID |
| 5 | USB audio interface | Generic class-compliant USB audio board, drives the case speakers |
| 6, 7 | unused | Headroom for future devices |

**Tracker note:** the experimental ESP32-S3 antenna tracker is NOT on
the case USB. It sits on the wired CRSF cable between the case and
the ELRS module, byte-pumping frames in both directions and sniffing
GPS telemetry. The case-side stack does not see the tracker over USB.

### Stable device naming

Use `/dev/serial/by-id/` paths in the daemon's flag arguments so a
USB enumeration shuffle doesn't break the launch. The current
`OPERATIONS.md` documents the canonical paths for each device.

## Power distribution

Single 12V input to the case. Two buck converters split it for the
two 5V loads; the 12V rail itself feeds the ELRS module externally
through the pole cable.

```
12V input
  |
  |-- 12V (case rail) --> pole cable --> ELRS module
  |
  |-- 12V/5V buck #1 --> Pi 400 (USB-C, 5V)
  |
  |-- 12V/5V buck #2 --> 7-port USB hub (5V)
```

| Rail | Source | Destination | Notes |
|------|--------|-------------|-------|
| 12V case | external input | ELRS module (via pole cable), bucks #1 and #2 | TODO: input current rating, fuse rating |
| 5V Pi | buck #1 | Pi 400 USB-C | Pi 400 needs 5V/3A nominal |
| 5V hub | buck #2 | 7-port USB hub | Powers everything on the hub plus the WS2813 strip if drawn from a hub port (TODO: confirm WS2813 power source) |
| 3.3V | (per device) | per-MCU onboard LDO from USB 5V | No case-level 3.3V rail needed |

The Pi 400 is not powered from a hub port: it has its own dedicated
buck. This keeps the Pi's supply clean and avoids the hub's
inrush/load transients showing up on the Pi's 5V.

**Open items / TODO:**

- 12V input current budget: Pi 400 (~3A at 5V = ~1.3A at 12V through
  buck), hub + USB devices (~2-3A at 5V = ~1A at 12V through buck),
  ELRS module (~0.5A peak at 12V down the cable). Round budget
  around 3A at 12V. Confirm against actual draw once everything is
  wired and warmed up.
- Buck converter models. Both should be sized for the worst-case
  draw plus headroom; cheap modules with marginal heatsinks have a
  habit of throttling under sustained load.
- Inrush limiting on the 12V input. Capacitor banks downstream of
  bucks and the WS2813 strip can pull a meaningful inrush on power-on.
- WS2813 strip power source: hub-port-derived 5V is fine for short
  strips; a dedicated 5V feed is better for longer ones. TODO: pick
  and document.

## External case I/O

### Pole cable to ELRS module

5m, 4-conductor shielded multi-core cable (cabo manga blindado
4x0.5mm² malha de cobre puro cobre flexível) running from the case
to the externally-mounted ELRS TX module on the antenna pole.

| Conductor | Function | Notes |
|-----------|----------|-------|
| Signal | CRSF (single-wire half-duplex) | Case-side TX merged via 470Ω series resistor; case-side RX direct. Both terminate on the ELRS module's single CRSF pin |
| Signal GND | CRSF ground reference | Separate from power GND inside the case for noise isolation; tied at the ELRS module end |
| 12V | ELRS module power | Direct off the case 12V rail. ~0.5V drop at 1A peak through 0.5mm² loop at 5m |
| Power GND | ELRS module ground return | |

The cable's outer shield is bonded to chassis ground at the case
end. Tracker (when present) sits inline at any point along the
CRSF path, byte-pumping transparently.

### Front-panel I/O

| Jack | Function | Wiring |
|------|----------|--------|
| USB-A (joystick port) | Operator's joystick connection | Internal USB-A extension cable from hub port 3 to a panel-mount USB-A jack on the front of the case |

### Power input

12V single rail. TODO: connector type (XT60, EC5, barrel jack), polarity,
fuse rating.
