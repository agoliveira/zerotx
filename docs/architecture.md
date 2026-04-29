# ZeroTX Architecture (locked decisions)

## High-level

```
                +-----------------------------+
                | Pi 400                      |
HOTAS / USB --->| Go daemon (mixer, logic,    |
                | model mgmt, telem forward)  |
                |          ^                  |
                |          | localhost HTTP+WS|
                |          v                  |
                | Web GUI (embedded, HTTP/WS) |
                +--------------+--------------+
                               | USB-CDC
                               v
                +-----------------------------+
                | RP2040 (Pico)               |
GPIO inputs --->| - reads switches/encoders   |
WS2812 strip <--| - drives status surfaces    |
I2C OLED <------| - owns CRSF UART            |
                | - safety watchdog           |
                +--------------+--------------+
                               | UART (PL011 via JR-bay)
                               v
                +-----------------------------+
                | ELRS module (ES900TX/Ranger)|---RF--->aircraft
                +-----------------------------+
                               ^ telem back via CRSF
```

## Stack lock

| Component  | Language     | Notes |
|------------|--------------|-------|
| Daemon     | Go           | Lifts joystick reader + CRSF telem from gsbridge |
| GUI        | HTML+CSS+JS  | Single-file, embedded in daemon binary via go:embed |
| Firmware   | C++ Pico SDK | RP2040, Pico-W not required |
| IPC Pi-MCU | USB-CDC      | COBS framing, fixed structs hot path, MessagePack slow path |
| GUI-daemon | localhost    | HTTP for commands/config, WS for telem stream |

## I/O inventory

- 4x 3-position toggle switches (mode/aux)
- 2x 2-position toggle switches (aux on/off)
- 1x 2-position momentary (arm/disarm convention)
- 4x rotary encoders with push (S1/S2 dial role + LS/RS slider role)
- 1x 6-position rotary selector (flight mode select)
- WS2812 RGB status strip
- I2C OLED status panel
- Tertiary display TBD from parts bin
- 1x latched RTH hardware button

## Mixer location

**Pi side.** RP2040 stays small and reliable. Pi sends 16-channel intent at 50Hz, RP2040 packs CRSF locally and emits to module.

## Failsafe chain

1. Pi heartbeat lost (>200ms): RP2040 switches to safe defaults (centered sticks, throttle zero, disarmed channel low). Module link stays up.
2. Module link lost: ELRS-side failsafe.
3. FC sees no signal: INAV failsafe (RTH/land per FC config).

Three independent layers.

## Display layout

- Left 7" touch (1024x600): ZeroTX web GUI (PFD page default, control panes via vertical tabs)
- Right 7" touch (1024x600): mwp full-screen
- Aomway 7" HDMI: separated video pipeline from VRX, no Pi involvement (optional powered HDMI splitter for DVR/stream off VRX)

## Theme

Dark glass-cockpit aesthetic. Color palette and fonts in `pi/gui/units/u_theme.pas`.

## Phasing (milestones)

- **M0 scaffold** (this drop): Repo skeleton, GUI shell with vertical tabs, theme applied, empty pages
- **M1 safety floor**: RP2040 firmware emits safe CRSF, owns module UART, USB-CDC link with heartbeats, CLI from Pi commands channels
- **M2 joystick + model**: Go daemon with joystick reader, YAML mapping, EdgeTX YAML model parser, joystick -> daemon -> RP2040 -> module
- **M3 mixer + safety**: Mixer, logic switches, arm gate, pre-flight gate
- **M4 telemetry**: CRSF telem receive, MSP-over-CRSF passthrough to mwp, MSP pre-flight FC checks
- **M5 GUI v1**: Wire all GUI pages to daemon, channel monitor, telem HUD, model selector
- **M6 module config**: ELRS Lua v3 subset (power, rate, telem ratio, model match, switch mode, bind)
- **M7 quality of life**: ElevenLabs+piper voice, auto flight log -> INAV Toolkit, WiFi telem rebroadcast, per-model auto-load by RX ID, RTH button
- **M8 status surfaces**: RP2040 OLED, WS2812 strip, tertiary display
- **M9 enclosure & field**: Mounted controls, dual displays, power, JR-bay wiring
- **M10 first flight**: Bench RF validation, expendable airframe, in-air test

## Out of scope

- Failsafe channel positions (FC handles)
- Binding flow (use radio or web flasher one-off)
- Trim/subtrim
- Expo/curves/dual-rate (FC handles)
- Sub-millisecond latency
- Editing models from scratch in-app (import-only is enough)
