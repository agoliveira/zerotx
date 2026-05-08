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
                   +---- 12V direct  ---- LCDs (TODO confirm voltage per panel),
                   |                      pole cable (ELRS module direct in
                   |                      default cfg; pole-end project box
                   |                      in extended cfg)
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
| ELRS TX module (default) | 12V via cable | ~1A peak | direct off the case 12V rail through the pole cable |
| Pole-end project box (extended cfg only) | 12V via cable | TODO | tracker + ELRS + 2x servos under bucks; servos dominate |

**TODO**: measure actual peak with HUB75 at maximum brightness plus all LEDs lit. Confirm 5V rail headroom.

### Field vs lab power

Lab: bench supply or AC mains feeding the internal 13.8V CCTV PSU through the front IEC inlet.

Field: 12V/AC dual-rail input on the rear bulkhead, feeding the same internal regulation chain. Switchover mechanism: **TODO** (document the actual scheme once decided: passive ORing diodes, manual switch, auto-changeover relay, or other).

## USB topology

The Pi 400 has 3x external USB-A ports (2x USB 3.0, 1x USB 2.0) plus its internal keyboard. Three things hang directly off the Pi: the boot SSD, the RP2040 CRSF endpoint, and a powered USB hub. Everything else lives behind the hub.

The RP2040 gets a dedicated Pi port (not behind the hub) for jitter and reliability isolation. CRSF I/O is the most safety-critical USB path in the system; putting it behind a hub introduces failure modes (hub power glitch, bandwidth contention with HID, enumeration races on cold boot) that don't exist on a direct connection.

```
TODO: confirm which physical Pi port each device sits on after final assembly

Pi 400 USB port A (USB 3.0)
  +-- Boot SSD                  (root filesystem; system boots from this)

Pi 400 USB port B (USB 3.0)
  +-- RP2040 CRSF endpoint      (id: usb-Raspberry_Pi_Pico_E66138935F3C4824)

Pi 400 USB port C (USB 2.0)
  +-- Powered USB hub (model: TODO)
        +-- Mega 2560 IO board       (id: usb-Arduino_LLC_Mega_2560_R3_<serial>)
        +-- ESP32 panel driver       (id: usb-<vendor>_<chip>_<serial>)
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
| CRSF UART | RP2040 GP0 (TX) and GP1 (RX) | Case bulkhead, then case-to-pole cable | bidirectional (channel intents out, telemetry in) |
| GND | shared | shared with bulkhead and onward to pole | common ground reference |

In default cable configuration GP0 (TX) is joined to GP1 (RX) at the case end through a 470Ω series resistor on the TX side, and the resulting single-wire signal goes out on the cable to the ELRS module's CRSF pin. In extended configuration GP0 and GP1 connect to the case-end MAX490's DI and RO pins respectively, and the differential pair travels the cable. No firmware change is required to switch modes.

## Case-to-pole cable

Two configurations are supported. The default is a short single-wire CRSF cable terminating directly on the ELRS module. An extended configuration with RS-422 transceivers and a pole-end project box is documented after it; that configuration is required for cable runs longer than ~5m, and for use of the inline antenna tracker.

### Default configuration (single-wire, up to 5m)

5m, 4-conductor shielded multi-core cable (`cabo manga blindado 4x0.5mm² malha de cobre puro cobre flexível`) running from the case directly to the externally-mounted ELRS TX module. Detailed conductor table and termination wiring are in `docs/hardware-pinout.md` under "External case I/O".

```
Case end                                          Pole end
+----------+                                      +-----------+
| RP2040   | TX (GP0) ---- 470Ω ---+----- signal ------>|     |
|  (CRSF)  | RX (GP1) -------------+                    |ELRS |
|          |                                            | TX  |
|          | GND ---------- signal GND -------->        |     |
+----------+                                            |     |
12V rail ----------- 12V cable -------------->          |     |
GND ----------------- power GND -------------->         |     |
                                                        +-----+
Outer shield: chassis GND at case end only.
```

No transceivers, no pole-end electronics. CRSF is half-duplex on a single wire; the 470Ω series resistor on TX prevents driver contention with the module's telemetry direction.

### Extended configuration (RS-422, longer runs and tracker support)

Required when cable length exceeds ~5m or when the inline antenna tracker is in use. Replaces the single-wire signal pair with an RS-422 differential pair driven by MAX490 transceivers at each end, and adds a pole-end project box documented in the next section.

The cable carries:

- 12V power for the pole-end electronics (tracker, ELRS module, servos via downstream bucks)
- RS-422 differential pair (DI/RO on each MAX490; A/B on the wire)
- GND
- Optional spare conductor reserved for future use

CRSF frames travel as RS-422 between the case-end MAX490 and the pole-end MAX490 in both directions. RS-422 was chosen over native UART for cable noise immunity and length tolerance, and gives the tracker a clean inline insertion point.

```
Case end                                     Pole end (project box)
+----------+                                 +----------+
| RP2040   |  CRSF UART (TTL)                |  MAX490  |
|  (CRSF)  |---->----+                       |          |
|          |<----+   |    +-------------+    |          |    +------------+
+----------+     |   v    | RS-422 pair |    |          |    | ESP32-S3   |
                 |  +-------------------+--->|          |--->|  Tracker   |
                 +--| Case-end MAX490   |    |          |    | (optional) |
                    +-------------------+<---|          |<---|            |
                                             +----------+    +-----+------+
                                                                   |
                                                                   v
                                                           +-------+--------+
                                                           |  ELRS TX module |
                                                           |  (CRSF UART)    |
                                                           +-----------------+
                                                                   |
                                                                   v
                                                          (RF out via pole antenna)
```

If the tracker is not installed, the pole-end MAX490 connects directly to the ELRS module's CRSF UART; the case-side stack is unchanged.

## Pole-end project box (extended configuration only)

External to the case, mounted on the pole. Houses the ELRS TX module, local power conditioning (RS-422 transceiver, bucks), and optionally the antenna tracker plus its servos. Receives 12V from the case via the multi-conductor cable. The tracker is optional within this configuration; an RS-422 cable run without a tracker is a valid use of the extended layout when the only requirement is cable length.

| Component | Role | Notes |
|---|---|---|
| ESP32-S3 (QFN56) | Antenna tracker | 16MB QIO flash + 8MB QSPI PSRAM (3.3V, NOT octal); see `firmware/tracker/README.md` |
| MAX490 (or MAX3490) | RS-422 transceiver | terminates the cable's RS-422 pair, presents UART to the tracker's UART1 |
| 6V buck | Servo rail | feeds pan and tilt servos |
| 5V buck | Logic rail | feeds the ESP32-S3 |
| 2-DOF PTZ gimbal | Pan/tilt mount | Ø82mm pan bearing carries the load; 25kg/270° pan servo and 20kg/180° tilt servo (TODO confirm final part numbers after order) |
| ELRS TX module | RF link | RadioMaster Nomad / Ranger / HappyModel ES900TX, depending on band |
| Pole antenna | RF emitter | mounted external to the project box |

ESP32-S3 pin map (from `firmware/tracker/`):

| Function | GPIO |
|---|---|
| UART1 RX (cable / MAX490) | GP17 |
| UART1 TX (cable / MAX490) | GP18 |
| UART2 RX (ELRS module) | GP4 |
| UART2 TX (ELRS module) | GP5 |
| Pan PWM (LEDC ch 0) | GP6 |
| Tilt PWM (LEDC ch 1) | GP7 |
| I2C SDA (reserved, future magnetometer) | GP8 |
| I2C SCL (reserved, future magnetometer) | GP9 |

The tracker is the only component on the pole that needs USB-CDC access, and only for calibration. In normal operation the cable is the only connection between case and pole.

**TODO**: hardware bypass jumper for field recovery if the tracker firmware fails (route the cable's RS-422 pair around the tracker, directly to the ELRS module). Planned for the project box layout.

## External bulkhead inventory

Case-side connectors. **TODO**: confirm final list and locations after assembly.

| Connector | Type | Purpose |
|---|---|---|
| Lab power input | IEC C14 (front) | mains to internal 13.8V CCTV PSU |
| Field power input | TODO (XT60? Anderson Powerpole?) | 12V/AC dual-rail field input |
| Pole connector | Multi-conductor; default config carries CRSF + signal GND + 12V + power GND, extended config carries RS-422 pair + 12V + GND + spare | feeds the ELRS module directly (default) or the pole-end project box (extended) |
| Front-panel USB | USB-A x N | ad-hoc devices, charging, dev access |

## See also

- `docs/ARCHITECTURE.md`: system overview and component responsibilities
- `docs/OPERATIONS.md`: launch sequence and recovery procedures
- `docs/BOOTSTRAP.md`: udev rules, SSD provisioning, Pi setup
- `docs/DECISIONS.md`: locked decisions (wired-only case, RF on poles, etc.)
- `docs/protocols/display.md`: HUB75 wire protocol
- `firmware/display/README.md`: ESP32 panel driver, GPIO pinout, panel chain detail
- `firmware/io/README.md`: Mega IO firmware, canonical pin table, HAL flags
- `firmware/tracker/README.md`: ESP32-S3 antenna tracker firmware
- `rp2040/README.md`: CRSF endpoint firmware
- `tools/zerotx-iohal-config/`: HAL configuration CLI for Mega pin mapping
