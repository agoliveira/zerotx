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

- `firmware/crsf/src/crsf.c` (lines 10-11): `CRSF_UART_TX`, `CRSF_UART_RX`
- `firmware/crsf/src/input_arm.h` (line 32): `INPUT_ARM_PIN`
- `firmware/crsf/src/input_momentary.h` (line 36): `INPUT_MOMENTARY_PIN`
- `firmware/crsf/src/status_led.h` (line 8): `STATUS_LED_PIN`

Pin numbers are compile-time `#define` values. Changing them requires
a firmware reflash.

| GPIO | Direction | Function | Notes |
|------|-----------|----------|-------|
| GP0  | output | UART0 TX to ELRS module (CRSF) | Hardware UART. Series resistor at the end of cable, see hardware-bom |
| GP1  | input  | UART0 RX from ELRS module (CRSF) | Hardware UART, telemetry path |
| GP14 | input  | Aviator-style arm key (SF-equivalent) | Internal pull-up. Switch to GND. Far from UART and LED, no timing-sensitive neighbours |
| GP15 | input  | Momentary push-button (SH-equivalent, arm confirm) | Internal pull-up. Switch to GND. Adjacent to GP14 so a single panel cable can carry both inputs plus shared GND |
| GP16 | output | Onboard WS2812 status LED | Hardwired on the Waveshare board, driven by PIO0 |

**Free GPIO** for future expansion: GP2-GP13, GP17-GP29 (subject
to which pads are accessible on the Zero footprint; GP17-GP25 are on
the bottom solder pads, not the edge headers).

**Caveats:**

- USB CDC is the IPC channel to the daemon. Don't repurpose USB.
- Watchdog hardware-enabled in firmware; main loop must kick within
  the watchdog timeout or the board resets. Relevant if anyone ever
  adds a long-blocking call.

### Device wiring

**Front-panel arm key + momentary** (3-wire panel cable)

Both inputs use internal pull-ups; one terminal of each switch goes
to its GPIO, the other to a shared GND. A single 3-conductor cable
from the panel back to the RP2040 is sufficient.

```
Front panel                       RP2040 Zero
┌─────────────────┐
│ Arm key SF      │
│   common      ──┼──────────► GP14
│                 │
│ Momentary SH    │
│   common      ──┼──────────► GP15
│                 │
│ shared GND    ──┼──────────► GND
└─────────────────┘
   (other terminal of each switch goes to the panel's GND rail)
```

Notes:
- No external resistors. Firmware sets `gpio_pull_up()` on both pins.
- Switch closed pulls the pin LOW; firmware translates to "logical
  ON" in the protocol.
- Arm key is a guarded ON/OFF (latching) switch. Momentary is a
  push-button (returns to open when released).
- Pins are adjacent on the Zero's edge header, so a Dupont 3-pin
  shell or a JST-XH-3 connector covers both signals plus GND.

**ELRS module** (CRSF, see `CONNECTIONS.md` for the full case-to-pole cable)

```
RP2040 Zero                       Bulkhead -> pole cable
   GP0 (TX) ──┐
              ├── (joined via 470Ω at the case end) ──► CRSF signal
   GP1 (RX) ──┘
   GND        ──────────────────────────────────────► CRSF GND
```

Notes:
- Default cable mode is single-wire half-duplex CRSF: TX and RX are
  joined at the case end with a 470Ω series resistor on the TX line
  to protect against bus contention. The single signal then runs the
  pole cable to the ELRS module's CRSF pin.
- Extended cable mode (RS-422 over longer runs) is documented in
  `CONNECTIONS.md`; GP0 and GP1 connect to a MAX490 transceiver
  instead. No firmware change required.

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
| 11, 12 | Free | Previously trackball ring LEDs; freed when the trackball was removed (see DECISIONS) |
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
| 10 | GLCD /RESET (`glcd_reset`) | Pulsed at boot to cold-start the ST7920 controller |
| 46 | VFD0 D4 (`vfd0_d4`) | |
| 47 | VFD0 D5 (`vfd0_d5`) | |
| 48 | VFD0 D6 (`vfd0_d6`) | |
| 49 | VFD0 D7 (`vfd0_d7`) | |
| 50 | SPI MISO | Reserved free; ST7920 GLCD doesn't read back |
| 51 | SPI MOSI | GLCD SID (`glcd_*` doesn't list this -- hardware SPI pins are not in HAL) |
| 52 | SPI SCK | GLCD CLK (same; hardware-fixed) |
| 53 | SPI SS / GLCD CS (`glcd_cs`) | Default SS used as ST7920 CS; HAL-remappable |
| 54 (A0) | LDR ambient-light sensor (`ldr_0`) | Analog input |
| 56 (A2) | VFD1 RS (`vfd1_rs`) | Second VFD via Vfd subsystem (`vfd.1`) |
| 57 (A3) | VFD1 EN (`vfd1_en`) | |
| 58 (A4) | VFD1 D4 (`vfd1_d4`) | |
| 59 (A5) | VFD1 D5 (`vfd1_d5`) | |
| 60 (A6) | VFD1 D6 (`vfd1_d6`) | |
| 61 (A7) | VFD1 D7 (`vfd1_d7`) | |

**Unused pins** in the default config: 13 (also onboard LED), 41,
42, and analog A1, A8-A15 (9 free analog channels). Pins 14-19
(Serial1/2/3) are reserved free for future UART expansion. Pin 50
(SPI MISO) is reserved free since the ST7920 GLCD doesn't read back.

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

### Device wiring

Pin numbers below are the HAL **defaults**. If you've remapped any
pin via the HAL config tool, use the value in EEPROM instead.

Power note: the Mega's onboard 5V regulator can supply the logic-level
inputs and a few mA of LED current, but anything that draws meaningful
current (servos, WS2813 strip, multi-LED bars, the VFDs at full
brightness) needs an external 5V rail. "5V (ext)" below means the
case 5V hub-rail, not the Mega's regulator.

**KY-040 rotary encoder** (5 wires)

```
KY-040                            Mega
┌───────────┐
│ CLK     ──┼──────────────────► Pin 2   (enc0_a, INT0)
│ DT      ──┼──────────────────► Pin 3   (enc0_b, INT1)
│ SW      ──┼──────────────────► Pin 4   (enc0_sw)
│ +       ──┼──────────────────► 5V
│ GND     ──┼──────────────────► GND
└───────────┘
```

Notes: hardware-interrupt pins for the quadrature pair (INT0/INT1).
Onboard pull-ups on the KY-040 module; firmware also enables internal
pull-ups defensively. Push-button on shaft press goes to pin 4.

**Passive piezo buzzer** (2 wires)

```
Buzzer                            Mega
┌──────┐
│ +  ──┼──────────────────────► Pin 5   (buzzer)
│ -  ──┼──────────────────────► GND
└──────┘
```

Notes: passive only. Active buzzers (with internal oscillator) won't
work — firmware uses Arduino's `tone()` to drive the frequency.

**Servos** (×4, generic; one block per servo)

```
Servo                             Mega
┌──────────┐
│ Signal ──┼──────────────────► Pin 6..9  (servo_0..servo_3, see lookup)
│ +      ──┼──────────────────► 5V (ext)
│ GND    ──┼──────────────────► GND (shared with Mega GND)
└──────────┘

Lookup: servo_N → Mega pin (6+N)
  servo_0 → 6     servo_2 → 8
  servo_1 → 7     servo_3 → 9
```

Notes: servos pull a lot of current (~150mA idle, peaks of 1A+ on
load). Power them from the case 5V rail, NOT from the Mega's
regulator. GND must be shared between the servo's external supply
and the Mega.

**I2C LCD** (4 wires)

The Mega supports an HD44780-on-I2C-backpack character LCD on the
hardware I2C bus. Address is auto-detected by the I2cLcd subsystem.

```
LCD I2C backpack                  Mega
┌────────────┐
│ VCC      ──┼──────────────► 5V
│ GND      ──┼──────────────► GND
│ SDA      ──┼──────────────► Pin 20  (hardware I2C SDA)
│ SCL      ──┼──────────────► Pin 21  (hardware I2C SCL)
└────────────┘
```

Notes: pins 20/21 are hardware I2C and not in HAL — you can't remap
them. The bus is shared, so additional I2C peripherals can hang off
the same two pins (with their own addresses).

**Relays** (×4, generic)

```
Relay module input                Mega
┌──────────┐
│ IN     ──┼──────────────► Pin 22..25  (relay_0..relay_3, see lookup)
│ VCC    ──┼──────────────► 5V (ext, if multi-channel module)
│ GND    ──┼──────────────► GND
└──────────┘

Lookup: relay_N → Mega pin (22+N)
  relay_0 → 22    relay_2 → 24
  relay_1 → 23    relay_3 → 25
```

Notes: default polarity is active-HIGH (drive HIGH to energize). If
your relay board needs active-LOW (some optocoupler-isolated boards
do), flip the `ACTIVE_LOW` HAL flag for the slot rather than
rewiring. Multi-channel relay boards have their own VCC pin to power
the coils; don't draw that current from the Mega.

**Panel buttons** (×10, generic)

```
Push-button                       Mega
┌────────┐
│ A    ──┼──────────────► Pin 26..35  (button_0..button_9, see lookup)
│ B    ──┼──────────────► GND
└────────┘

Lookup: button_N → Mega pin (26+N)
  button_0 → 26    button_5 → 31
  button_1 → 27    button_6 → 32
  button_2 → 28    button_7 → 33
  button_3 → 29    button_8 → 34
  button_4 → 30    button_9 → 35
```

Notes: firmware enables `INPUT_PULLUP` per pin. No external resistor
needed. Button closure pulls the pin LOW; HAL converts to logical
"pressed" with default active-LOW polarity.

**Indicator LEDs** (×4, generic)

```
LED                               Mega
┌──────────┐
│ Anode  ──┼──────────► Pin 36..39 + series resistor (led_0..led_3)
│ Cathode┼─┼──────────► GND
└──────────┘

Lookup: led_N → Mega pin (36+N)
  led_0 → 36    led_2 → 38
  led_1 → 37    led_3 → 39
```

Notes: firmware drives these as digital outputs (on/off, no PWM).
Series resistor sized for ~5-10mA: 470Ω for typical 2V red LEDs from
5V, 1kΩ for higher-Vf colors. Don't omit; bare LEDs on a digital pin
will pull more current than the pin can sustain and may damage the
MCU output.

**WS2813 strip** (3 wires)

```
WS2813 strip                      Mega
┌────────────┐
│ Data in  ──┼────────► Pin 40 (ws_data) + 470Ω series at the strip
│ +5V      ──┼────────► 5V (ext, see Power distribution)
│ GND      ──┼────────► GND (shared with Mega GND)
└────────────┘
```

Notes: do NOT power the strip from the Mega's regulator beyond
maybe 4-5 LEDs at low brightness. Each WS2813 LED can pull 60mA at
full white; even short strips need a dedicated 5V supply. The 470Ω
series resistor on the data line damps reflections and is good
practice on any signal run longer than a few cm. The strip's GND
must be tied to the Mega's GND for the data line to be referenced
correctly. Power source for the strip is still TODO (see Power
distribution).

**VFD modules** (×2, Noritake CU20025ECPB-W1J in 4-bit M68 mode, 6 signal wires + power)

```
VFD module (14-pin header; not the standard 16-pin LCD layout)
                                  Mega (VFD0 / VFD1)
┌───────┐
│ GND  1┼──────────────────► GND
│ VCC  2┼──────────────────► 5V (ext; ICC ~130mA typ, 2x at power-on inrush)
│ FNC  3┼──────────────────► (leave open; see notes)
│ RS   4┼──────────────────► Pin 44 (vfd0_rs) / 56 / A2 (vfd1_rs)
│ R/W  5┼──────────────────► GND (write-only; firmware never reads back)
│ E    6┼──────────────────► Pin 45 (vfd0_en) / 57 / A3 (vfd1_en)
│ D0   7┼──────────────────► (NC in 4-bit mode)
│ D1   8┼──────────────────► (NC)
│ D2   9┼──────────────────► (NC)
│ D3  10┼──────────────────► (NC)
│ D4  11┼──────────────────► Pin 46 (vfd0_d4) / 58 / A4 (vfd1_d4)
│ D5  12┼──────────────────► Pin 47 (vfd0_d5) / 59 / A5 (vfd1_d5)
│ D6  13┼──────────────────► Pin 48 (vfd0_d6) / 60 / A6 (vfd1_d6)
│ D7  14┼──────────────────► Pin 49 (vfd0_d7) / 61 / A7 (vfd1_d7)
└───────┘
```

Notes:
- **14-pin header, not 16.** The CU20025ECPB-W1J has no LED backlight
  (it's a self-luminous VFD), so pins 15 and 16 — the A and K
  backlight pins on a standard HD44780 LCD — are not present.
- **Pin 3 is `FNC`, not contrast.** Per datasheet: "normally open
  circuit. If pads JP1.1 and JP1.2 are linked, Pin 3 = /Reset". So
  leave it unwired unless you've explicitly bridged the JP1 jumper
  on the back of the module to expose external reset.
- **JP2 jumper selects the bus protocol.** Default (no jumper) is
  M68-style: pin 5 = R/W, pin 6 = E. Bridged is i80-style: pin 5 =
  /WR, pin 6 = /RD. **Leave JP2 open**; the firmware uses the
  duinoWitchery `hd44780_NTCU20025ECPB_pinIO` class which speaks M68.
- 6 signal wires per VFD: RS, E, D4-D7. R/W tied to GND so the chip
  is write-only.
- Two VFDs share the data lines? **No.** This wiring is for two
  *independent* VFDs each on its own 6-pin set (vfd0 vs vfd1). They
  share VCC + GND only.
- Power: ICC 130mA typical per VFD, can hit 260mA at power-on
  inrush. Use the case 5V rail; do not draw from the Mega's
  regulator.

**LDR ambient-light sensor** (2 wires + divider resistor)

```
LDR (assuming a raw photoresistor; if you're using a KY-018-style
module with onboard divider, wire its DO/AO pins per its silkscreen)

                                  Mega
LDR ── Pin A0 (ldr_0) ── 10kΩ ── GND
                              ┃
                              ┗── (other LDR leg) ── 5V
```

The LDR forms a voltage divider with a 10kΩ pull-down. As ambient
light rises the LDR resistance falls and pin A0 reads higher.

```
       5V
        │
       LDR (resistance varies with light)
        │
        ├──────────────► A0 (ldr_0, analog input)
        │
       10k
        │
       GND
```

Notes: A0 is `ldr_0` in HAL and read as a 10-bit ADC value. The
divider is sized so the swing covers a useful chunk of the 0-1023
range under indoor lighting; tune the 10k to taste if your conditions
are unusual (very dim or very bright). KY-018 modules wrap this
divider into a board with two outputs (digital threshold via
trim-pot, plus analog); use the analog output (AO) into A0 and ignore
DO.

**128x64 graphic LCD** (ST7920 controller, 3-wire serial mode; 8 signals)

```
ST7920 LCD module (14-pin connector; pins 7-14 are DB0-DB7, unused in serial mode)
                                  Mega
┌────────────┐
│ VSS    1 ──┼─────────────────► GND
│ VDD    2 ──┼─────────────────► 5V
│ V0     3 ──┼────┐  contrast divider (10k multi-turn trimpot)
│            │    │
│            │    └── 10k pot wiper ──── 5V
│            │                  │
│            │                  └─── GND
│ CS     4 ──┼─────────────────► Pin 53 (glcd_cs, hardware SPI SS)
│ SID    5 ──┼─────────────────► Pin 51 (MOSI, hardware SPI fixed)
│ CLK    6 ──┼─────────────────► Pin 52 (SCK, hardware SPI fixed)
│ PSB   15 ──┼─────────────────► GND  (forces 3-wire serial mode)
│ /RESET 17──┼─────────────────► Pin 10 (glcd_reset, firmware pulses on init)
│ A     19 ──┼─── R_bl ──────► 5V  (backlight; R_bl per module datasheet, often built-in)
│ K     20 ──┼─────────────────► GND (backlight cathode)
└────────────┘

   (DB0..DB7, NC, Vout — pins 7-14, 16, 18 — left unconnected)
```

Notes:
- **Serial mode only.** PSB tied to GND at the panel selects 3-wire
  mode at power-on. The module also supports parallel mode (PSB=H,
  8 data wires); we don't use that.
- **CS is active-HIGH** on the ST7920, unusual for SPI. The u8g2
  library's ST7920 constructor handles the inversion internally so
  the firmware just passes the pin number; do not invert in wiring.
- **Hardware SPI pins are fixed.** SID and CLK must go to Mega
  pins 51 (MOSI) and 52 (SCK) respectively; these aren't
  HAL-remappable. CS and /RESET are remappable via HAL (`glcd_cs`,
  `glcd_reset`).
- **Contrast trimpot.** The ST7920's V0 pin wants a stable voltage
  for character contrast. Standard wiring is a 10k multi-turn
  trimpot from 5V to GND with the wiper to V0. Adjust until the
  pixel contrast looks right; once dialed in, it doesn't drift.
  Some modules have an onboard trimpot soldered to the PCB; in
  that case V0 is internal and you don't wire pin 3.
- **Backlight current.** Modules vary: many have a built-in
  current-limit resistor on the A pin so you can wire +5V directly,
  others want an external 220-330Ω in series. Check the silkscreen
  or measure A→K resistance on the bare module. WS series modules
  (most common) have it built in.
- **Power draw.** ~50-80 mA at 5V depending on backlight type
  (LED or EL film). Within the Mega's 5V regulator capability for
  this single device, but if combined with the VFDs on the same
  rail, prefer the case 5V (ext) hub-rail.

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

### Device wiring

**HUB75 panel chain** (16 signals + power)

Two chained Waveshare P2.5 64×32 panels for a 128×32 logical surface.
Standard HUB75 16-pin connector at the input of panel 1; panel 1's
HUB75-OUT cable feeds panel 2's HUB75-IN.

```
HUB75 connector (16-pin IDC, viewed at the panel-1 input)

         ┌──────┬──────┐
   R1    │  1   │   2  │  G1   ← Waveshare swap: see notes
   B1    │  3   │   4  │  GND
   R2    │  5   │   6  │  G2   ← Waveshare swap
   B2    │  7   │   8  │  GND
    A    │  9   │  10  │   B
    C    │ 11   │  12  │   D
   CLK   │ 13   │  14  │  LAT
   OE    │ 15   │  16  │  GND
         └──────┴──────┘

Signal              ESP32 GPIO        Source
  R1 (top red)      → GPIO 25         explicit
  G1 (top green)    → GPIO 27         explicit (wired to library "B1")
  B1 (top blue)     → GPIO 26         explicit (wired to library "G1")
  R2 (bottom red)   → GPIO 14         explicit
  G2 (bottom green) → GPIO 13         explicit (wired to library "B2")
  B2 (bottom blue)  → GPIO 12         explicit (wired to library "G2")
  A  (addr 0)       → GPIO 23         library default
  B  (addr 1)       → GPIO 19         library default
  C  (addr 2)       → GPIO 5          library default
  D  (addr 3)       → GPIO 17         library default
  CLK               → GPIO 16         library default
  LAT               → GPIO 4          library default
  OE                → GPIO 15         library default
  GND (×3)          → ESP32 GND
  E (addr 4)        → not connected   1/16 scan; not used
```

Notes:
- **Waveshare G/B swap**. Waveshare 2.5mm-pitch panels swap GREEN
  and BLUE channels relative to standard HUB75. The firmware
  compensates: GPIO 27 carries G1 even though the library names that
  slot "B1", and similarly for the bottom half. **If you ever
  substitute non-Waveshare panels, remove this swap in
  `firmware/display/src/main.cpp:606-607`** or the colors will
  be wrong.
- **Strapping pins**. GPIO 5, 12, 15 are ESP32 boot strappers. GPIO
  12 must read LOW at boot or the chip selects the wrong flash
  voltage; the panel's idle HUB75 lines have been measured stable
  at boot for the current build, but if you see boot loops after
  rewiring, check 12 and 15 first.
- **Panel power**. Each Waveshare P2.5 panel pulls 1-2A at 5V at
  full white. Two panels in series = 4A peak from a dedicated 5V
  rail, NOT from the ESP32's USB power. Most panel kits include a
  spade-terminal pigtail for the 5V/GND power input — wire that to
  the case 5V hub-rail with appropriately-gauged wire (16AWG or
  larger for short runs).
- **HUB75-OUT to panel 2**: a second 16-pin IDC ribbon goes from
  panel 1's HUB75-OUT to panel 2's HUB75-IN. Order matters: panel 1
  is the "left half" of the logical 128×32 surface, panel 2 is the
  "right half". If they're swapped, the image displays mirrored;
  swap the cable rather than reconfiguring firmware.

**ESP32 USB** (1 cable)

```
ESP32 DevKit V1                   USB hub port (case)
   USB mini-B  ◄═══════════════════════►  hub port 1
```

Notes: provides both data (line protocol to the daemon at
115200 8N1) and 5V to the ESP32 board. NOT used to power the panels.

## Pi 400 GPIO breakout

The Pi 400 exposes the standard 40-pin Raspberry Pi GPIO header on the
back edge. ZeroTX uses a passive breakout board for access. The header
follows the Pi 4 pinout and uses BCM GPIO numbering in software (which
does not match the physical pin numbers on the header).

### Pin allocation

| Header pin | Function | Notes |
|------------|----------|-------|
| 1 | 3V3 power | Feeds the DS3231 RTC module. Can also power a 3V3-input GPS module (most u-blox M-series accept both 3V3 and 5V) |
| 3 | GPIO 2 (I2C1 SDA) | Shared I2C bus: DS3231 RTC at addr 0x68. Reserved for future I2C peripherals on the same bus |
| 4 | 5V power | Available if a GPS module needs 5V instead of 3V3. Otherwise unused |
| 5 | GPIO 3 (I2C1 SCL) | Shared I2C bus, paired with SDA above |
| 6 | GND | RTC and GPS ground return; common with rest of breakout |
| 7 | GPIO 4 (UART3 TXD) | Pi -> GPS module RX. Enabled by `dtoverlay=uart3` |
| 9 | GND | Heartbeat LED ground return |
| 11 | GPIO 17 | Daemon heartbeat LED (active-high). Drive a 1k series resistor + LED to GND |
| 29 | GPIO 5 (UART3 RXD) | GPS module TX -> Pi |
| 14, 20, 25, 30, 34, 39 | GND | Additional ground points; use whichever is closest |

### Device wiring

Per-module view of the same allocation, organized for wiring rather
than for reference. The 40-pin header is two rows of 20; pin 1 is at
the corner closest to the SD card slot (BCM 3V3), and odd-numbered
pins are on the row closer to the board edge.

```
                      Pi 400 GPIO header (back edge)
                  closer to board edge ─────────────────►

Row of odd pins:    1  3  5  7  9  11 13 15 17 19 21 23 25 27 29 31 33 35 37 39
                    ●  ●  ●  ●  ●   ●  ·  ·  ·  ·  ·  ·  ·  ·  ●  ·  ·  ·  ·  ·
                                          (used pins marked ●)
Row of even pins:   2  4  6  8  10 12 14 16 18 20 22 24 26 28 30 32 34 36 38 40
                    ·  ·  ●  ·  ·   ·  ·  ·  ·  ·  ·  ·  ·  ·  ·  ·  ·  ·  ·  ·

  ●  pin 1   3V3        -> DS3231 VCC, optional GPS VCC (3V3-input modules)
  ●  pin 3   GPIO 2     -> DS3231 SDA (I2C1)
  ●  pin 5   GPIO 3     -> DS3231 SCL (I2C1)
  ●  pin 6   GND        -> DS3231 GND, GPS GND (shared)
  ●  pin 7   GPIO 4     -> GPS RX (UART3 TX from the Pi)
  ●  pin 9   GND        -> Heartbeat LED cathode (any GND would work)
  ●  pin 11  GPIO 17    -> Heartbeat LED anode through 1k resistor
  ●  pin 29  GPIO 5     -> GPS TX (UART3 RX into the Pi)
```

**DS3231 RTC module** (4 wires)

```
DS3231 module                     Pi 400 GPIO header
┌──────────┐
│ VCC    ──┼────────────────► Pin 1   (3V3)
│ GND    ──┼────────────────► Pin 6   (GND)
│ SDA    ──┼────────────────► Pin 3   (GPIO 2, I2C1 SDA)
│ SCL    ──┼────────────────► Pin 5   (GPIO 3, I2C1 SCL)
└──────────┘
```

Notes: typical "DS3231 for Raspberry Pi" modules also expose SQW and
32K pins; both are unused. The CR2032 backup battery is on the module
itself; insert before first power-up so the RTC retains time across
reboots.

**GPS module** (4 wires)

```
u-blox-style GPS module           Pi 400 GPIO header
┌──────────┐
│ VCC    ──┼────────────────► Pin 1 (3V3) or Pin 4 (5V), per the module's spec
│ GND    ──┼────────────────► Pin 6   (GND)
│ TX     ──┼────────────────► Pin 29  (GPIO 5, UART3 RXD into the Pi)
│ RX     ──┼────────────────► Pin 7   (GPIO 4, UART3 TXD from the Pi)
└──────────┘
```

Notes: most u-blox M-series boards (NEO-6M, NEO-7M, NEO-M8N) accept
either 3V3 or 5V on VCC and have an onboard regulator. Check the
specific board before connecting. TX/RX are crossed (the GPS's TX
goes to the Pi's RX and vice versa). UART3 must be enabled in
`/boot/firmware/config.txt` with `dtoverlay=uart3` for the Pi to see
the GPS at `/dev/ttyAMA1`.

**Heartbeat LED** (2 wires + series resistor)

```
LED                               Pi 400 GPIO header
┌────────────┐
│ Anode (+)──┼─── 1kΩ ────────► Pin 11  (GPIO 17, daemon heartbeat output)
│ Cathode(-)─┼────────────────► Pin 9   (GND, any GND would work)
└────────────┘
```

Notes: 1kΩ is conservative for typical 2V red LEDs from 3.3V — gives
~1mA, dim but visible in indoor light. Drop to 470Ω or 220Ω if you
need a brighter indicator. The daemon drives this active-HIGH at 1Hz
while the 50Hz channel-mapper goroutine is alive; absence of blinking
means the daemon's not running or the mapper is wedged.

### Software notes

- Heartbeat LED is driven by `internal/heartbeat/` via the
  `github.com/warthog618/go-gpiocdev` library (Linux GPIO character
  device API). The daemon flag `-heartbeat-gpio 17` enables it; the
  default `-1` disables. While the daemon's 50Hz mapper loop is
  healthy, the LED blinks at 1Hz. Loop hang past 1.5s forces the LED
  low, daemon dead means the LED is dark.
- DS3231 RTC is an external module (typically a small board with the
  chip plus a CR2032 battery; e.g. the common Mercado Livre listing).
  Handled by the kernel via `dtoverlay=i2c-rtc,ds3231` in
  `/boot/firmware/config.txt`. The daemon does not read or write the
  RTC; it just logs whether the kernel detected one at startup. Setup
  procedure: `docs/BOOTSTRAP.md`.
- GPS is an optional Pi-attached serial module (u-blox M6/M7/M10 or
  equivalent NMEA TTL device) on UART3. The daemon flag `-gps-port`
  (e.g. `/dev/ttyAMA1`) enables reading; `-gps-baud` sets the rate
  (default 9600). Failure to open the port is non-fatal: the daemon
  logs and continues. UART3 needs `dtoverlay=uart3` in
  `/boot/firmware/config.txt`. Setup procedure: `docs/BOOTSTRAP.md`.

### Free pins

ZeroTX does not currently use GPIO 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
16, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27. SPI0 is on GPIO 8/9/10/11;
UART0 is on GPIO 14/15; PCM/I2S is on GPIO 18/19/20/21. Reserve those
banks when planning future expansions (I2S DAC, additional UARTs, etc.)
rather than picking pins by free-from-function logic alone.

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
| 2 | Mega 2560 | IO board (VFD, buttons, GLCD; firmware also supports indicator LEDs, relays, encoder, buzzer, LDR, WS2813 strip when fitted) |
| 3 | USB joystick passthrough | Routes via an internal USB-A extension cable to a panel-mount USB-A on the front of the case. Operator plugs in their X-HOTAS (or any class-compliant USB joystick) |
| 4 | USB audio interface | Generic class-compliant USB audio board, drives the case speakers |
| 5, 6, 7 | unused | Headroom for future devices |

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
