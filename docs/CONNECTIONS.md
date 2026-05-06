# ZeroTX Connections

## Purpose and scope

Physical wiring, USB topology, power distribution, and signal paths inside the ZeroTX case. Audience is me with the lid open and a multimeter handy.

For component responsibilities and data flows see `docs/ARCHITECTURE.md`. For per-firmware pinouts and the canonical Mega IO pin table see the firmware READMEs linked at the end.

This is a living document for an in-progress build. Sections and rows marked **TODO** need real measurements or final values plugged in. Update as the build settles.

## Power distribution

Primary rail: 13.8V CCTV PSU. Field-power alternate: 12V/AC dual-rail input on the rear bulkhead. Both feed the same internal regulation chain.

### Rails

```
TODO: confirm regulator topology, then redraw

13.8V CCTV PSU ----+---- buck to 5V ---- USB hub, Pi 400, MCUs, HUB75 panels,
                   |                     VFD, WS2813, lid LED panels, SSD
                   |
                   +---- 12V direct  ---- LCDs (TODO confirm voltage per panel)
                   |
                   +---- TODO: any 3.3V direct loads

12V/AC field input ----> [switchover: TODO ORing diode / relay / manual] ----> 13.8V bus
```

### Per-component budget

Initial estimates. Refine with bench measurements once the regulator topology is finalized.

| Component | Voltage | Est. current | Notes |
|---|---|---|---|
| Pi 400 | 5V | 1.5A typical, 2.5A peak | USB-C input |
| Boot SSD | 5V | 0.5A typical | via USB |
| HUB75 panels (2x P2.5 64x32) | 5V | TODO peak | dominant load, can hit 4-6A at full white |
| HUD LCD | TODO | TODO | model dependent |
| Map LCD | TODO | TODO | model dependent |
| Mega 2560 | 5V | ~0.2A | via USB |
| ESP32 (panel driver) | 5V | ~0.3A | via USB |
| RP2040 (CRSF) | 5V | ~0.1A | via USB |
| VFD (CU20025ECPB-W1J) | 5V | TODO | confirm against datasheet |
| Trackball ring LEDs | 5V | small | sourced from Mega |
| WS2813 strip (16 px) | 5V | 0.96A worst case | full white |
| Lid LED panels | TODO | TODO | TODO |
| Powered USB hub | TODO | TODO | self-powered vs internal-rail-powered: TODO |

**TODO**: measure actual peak with HUB75 at maximum brightness plus all LEDs lit. Confirm 5V rail headroom.

### Field vs lab power

Lab: bench supply or AC mains feeding the internal 13.8V CCTV PSU through the front IEC inlet.

Field: 12V/AC dual-rail input on the rear bulkhead, feeding the same internal regulation chain. Switchover mechanism: **TODO** (document the actual scheme once decided: passive ORing diodes, manual switch, auto-changeover relay, or other).

## USB topology

The Pi 400 has 3x external USB-A ports (2x USB 3.0, 1x USB 2.0) plus its internal keyboard. Three things hang directly off the Pi: the boot SSD, the RP2040 CRSF generator, and a powered USB hub. Everything else lives behind the hub.

The RP2040 gets a dedicated Pi port (not behind the hub) for jitter and reliability isolation. CPPM/CRSF generation is the most safety-critical USB path in the system; putting it behind a hub introduces failure modes (hub power glitch, bandwidth contention with HID, enumeration races on cold boot) that don't exist on a direct connection.

```
TODO: confirm which physical Pi port each device sits on after final assembly

Pi 400 USB port A (USB 3.0)
  +-- Boot SSD                  (root filesystem; system boots from this)

Pi 400 USB port B (USB 3.0)
  +-- RP2040 CRSF generator     (id: usb-Raspberry_Pi_Pico_E66138935F3C4824)

Pi 400 USB port C (USB 2.0)
  +-- Powered USB hub (model: TODO)
        +-- Mega 2560 IO board       (id: usb-Arduino_LLC_Mega_2560_R3_<serial>)
        +-- ESP32 panel driver       (id: usb-<vendor>_<chip>_<serial>)
        +-- ELRS TX backpack         (id: usb-<vendor>_<model>_<serial>)
        +-- USB joystick             (Thrustmaster <model>)
        +-- Trackball + 2 buttons    (USB HID composite)
        +-- Front-panel USB-A x N    (ad-hoc, charging, dev access)
```

The daemon launches against stable names under `/dev/serial/by-id/`. udev rules in `/etc/udev/rules.d/` further alias them where useful. See `docs/BOOTSTRAP.md` for udev setup, `docs/OPERATIONS.md` for the launch flag list.

**TODO**: hub model, and whether it's powered from the internal 5V rail or from its own external brick.

## Display signal paths

### HDMI

Pi 400 has 2x micro-HDMI ports labeled HDMI0 (next to USB-C power) and HDMI1.

| Pi port | Cable | Destination |
|---|---|---|
| HDMI0 | micro-HDMI to HDMI | HUD LCD |
| HDMI1 | micro-HDMI to HDMI | Map LCD |

**TODO**: confirm which physical port maps to HUD vs Map after final assembly. Set the corresponding `xrandr` (or kiosk autostart) mapping in `docs/BOOTSTRAP.md`.

### HUB75 panel chain

ESP32 drives the chained panels via the standard HUB75 pinout. Two Waveshare P2.5 64x32 panels chained in series, 128x32 logical resolution.

```
ESP32 GPIO --(IDC 16-pin ribbon)--> Panel A (IN)
                                    Panel A (OUT) --(ribbon)--> Panel B (IN)
                                                                Panel B (OUT) unused

5V high-current rail --(thick gauge, short run)--> Panel A power -> Panel B power
GND star-point --> shared with ESP32 GND, panel power return
```

GPIO mapping (R1, G1, B1, R2, G2, B2, A, B, C, D, E, CLK, LAT, OE) is defined in `firmware/display/`. See `firmware/display/README.md` and `firmware/display/platformio.ini` for the canonical pin assignment.

Wire protocol from daemon to ESP32 (over USB-CDC): `docs/protocols/display.md`.

### VFD signal

Noritake CU20025ECPB-W1J (20x2, blue/white) is driven by Mega via HD44780 4-bit interface. Mega pin assignment: see `firmware/io/README.md` (vfd.0 subsystem). VFD power from 5V rail. Confirm any contrast or brightness control pins against the CU20025ECPB-W1J datasheet.

## Mega IO connections

Pin assignments for all Mega-attached peripherals (VFD, trackball ring LEDs, 4 buttons, 4 LEDs, 4 relays, WS2813 strip, LDR, passive piezo buzzer, KY-040 rotary encoder) are managed via the HAL EEPROM v2 system in `firmware/io/`. The active configuration can be read and modified with `tools/zerotx-iohal-config/`.

The canonical pin table lives in `firmware/io/README.md`. This doc deliberately does not duplicate it.

Project-wide convention: active-HIGH default, per-pin HAL flag opts into active-LOW where wiring requires it.

## RP2040 wiring

| Connection | From | To | Notes |
|---|---|---|---|
| USB-CDC + 5V | Pi USB port B (direct, not hub) | RP2040 USB port | data and power; isolated from hub |
| CPPM or CRSF out | RP2040 GPIO **TODO** | Radio TX trainer port | confirm pin and electrical level (3.3V tolerance of target radio) |
| GND | shared | shared | common ground with radio |

**TODO**: document the specific output GPIO pin and any inline level shifter or buffer. Cross-check against `rp2040/` source. Confirm radio TX trainer port pinout and protocol selection (CPPM vs CRSF).

## ELRS TX backpack connection

ELRS modules (HappyModel ES900TX, RadioMaster Ranger 2.4GHz) live externally on poles. The case has no internal antennas, no SMA bulkhead passthroughs, no RF shielding (locked decision: see `docs/DECISIONS.md`).

```
External pole
  |
  +-- ELRS TX module
        |
        +-- USB cable (data + power) ----- bulkhead USB pass-through ----- internal powered USB hub
        |
        +-- antenna stays on pole
```

The daemon's `source` subsystem ingests CRSF/MAVLink telemetry directly from the module's USB serial.

## External bulkhead inventory

Case-side connectors. **TODO**: confirm final list and locations after assembly.

| Connector | Type | Purpose |
|---|---|---|
| Lab power input | IEC C14 (front) | mains to internal 13.8V CCTV PSU |
| Field power input | TODO (XT60? Anderson Powerpole?) | 12V/AC dual-rail field input |
| ELRS pole 1 | USB pass-through | ES900TX or Ranger module |
| ELRS pole 2 | USB pass-through | second ELRS module (if both fitted) |
| Front-panel USB | USB-A x N | ad-hoc devices, charging, dev access |
| Radio link out | TODO | CPPM/CRSF out from RP2040 to external radio |

## See also

- `docs/ARCHITECTURE.md`: system overview and component responsibilities
- `docs/OPERATIONS.md`: launch sequence and recovery procedures
- `docs/BOOTSTRAP.md`: udev rules, SSD provisioning, Pi setup
- `docs/DECISIONS.md`: locked decisions (wired-only case, RF on poles, etc.)
- `docs/protocols/display.md`: HUB75 wire protocol
- `firmware/display/README.md`: ESP32 panel driver, GPIO pinout, panel chain detail
- `firmware/io/README.md`: Mega IO firmware, canonical pin table, HAL flags
- `rp2040/README.md`: CRSF generator firmware
- `tools/zerotx-iohal-config/`: HAL configuration CLI for Mega pin mapping
