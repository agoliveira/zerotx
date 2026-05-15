# ZeroTX Builder's Manual

> **Status:** skeleton. H1/H2/H3 only, no prose yet. Sections fill in subsequent patches.

## Front matter

### Purpose and audience
### Scope: what this manual covers and what it doesn't
### Conventions (callouts, command blocks, version markers)
### Prerequisites (assumed skills: soldering, basic Linux, no programming required)
### Required tools and equipment (separate from BOM)
### Time and difficulty estimate
### Version compatibility (which firmware/daemon revisions this matches)
### Safety notice (mains wiring, LiPo handling, ESD; operational safety lives in User Manual)

## 0. Build workstation prep

### Host OS requirements
### Git and SSH
### Cloning the ZeroTX repo
### Go toolchain
### PlatformIO + Arduino core packages
### picotool (RP2040)
### rpi-imager
### Piper build / fetch dependencies
### Verification: build daemon, build all four firmwares from clean clone

## 1. System overview

### What ZeroTX is

![ZeroTX ground control station, opened](../images/zerotx-render.png)

ZeroTX is a portable ground control station for long-range fixed-wing FPV. It replaces a hand-held transmitter with a desktop-style station in an aluminum briefcase. A Raspberry Pi 400 runs the show, paired with three MCU satellites (Mega 2560, ESP32, RP2040). A Thrustmaster HOTAS joystick plugs into a front-panel USB-A port. Twin 7" HDMI LCDs in the lid drive a HUD and a map. A HUB75 LED panel, a 2x20 VFD, and a 128x64 graphic LCD give at-a-glance state on the body's front panel. The case is wired-only inside: no RF, no antennas, no transceivers. The ELRS TX module lives externally on a pole, connected to the case by a single cable.

The defaults are conservative: hardware kill in series with the module DC feed, a 950 ms end-to-end failsafe chain, recorded telemetry on every flight, and audio narration for non-trivial events. Nothing in the case is bespoke silicon; everything is off-the-shelf modules wired into a custom panel layout, with firmware in this repo.

### Topology block diagram

![ZeroTX topology](../images/topology.svg)

The case interior groups into four bands: the lid LCDs at the top, the Pi 400 brain in the middle, the three MCU satellites below it, and the front-panel surfaces driven by Mega and ESP32 along the bottom. The USB joystick plugs in externally via a front-panel USB-A jack. The single-wire CRSF cable exits the case to the externally-mounted ELRS TX module on a pole. Bidirectional arrows mark links where data flows both ways (USB-CDC to the MCUs, CRSF to the ELRS module); unidirectional arrows mark display-only or input-only paths (HDMI to the LCDs, USB HID from the joystick, drive lines from Mega and ESP32 to their panel surfaces).

The diagram shows the **default** cable configuration. The **extended** cable configuration replaces the single-wire CRSF run with an RS-422 differential pair (MAX490 transceivers on each end) and adds an inline ESP32-S3 antenna tracker between the cable's pole end and the ELRS module. The tracker byte-pumps frames transparently and is invisible to the daemon. Cable choices in Section 4, tracker firmware in Section 5.

### Subsystem responsibilities

#### Raspberry Pi 400 (brain)

Runs the `zerotxd` Go daemon and two Chromium kiosk browsers (HUD and Map). Owns the USB joystick, both HDMI displays, the audio output, and the three USB-CDC links to the MCU satellites. Boots from a USB SSD; built-in keyboard is the operator input device for menus, settings, and (during build) provisioning. Pi OS Lite, no desktop environment, no greeter. The daemon ingests CRSF telemetry from the RP2040, drives the LCDs through the kiosk browsers, orchestrates the HUB75 panel through the ESP32, talks to the Mega for buttons and status displays, plays audio, and sends joystick-derived channel intents back to the RP2040 for CRSF emission.

#### RP2040 (CRSF endpoint)

Bidirectional CRSF gateway. On the host side, USB-CDC to the Pi. On the wire side, half-duplex CRSF on the case-to-pole cable. Outbound: receives channel intents from the daemon, builds CRSF frames, drives the wire. Inbound: receives telemetry frames coming back from the link, forwards them to the daemon. Hardware watchdog enabled. In the default cable configuration, TX and RX are merged through a 470Ω series resistor at the case end for single-wire half-duplex. RP2040-Zero is the production board; original Pico is kept as a backup.

#### Mega 2560 (IO hub)

Drives the front-panel status and control surfaces. Currently fitted: one VFD (20x2, HD44780 4-bit), one 128x64 ST7920 graphic LCD (artificial horizon), six of the ten panel buttons in the button matrix. Firmware scaffolds additional peripherals on independent pin groups: a second VFD instance, an I2C LCD, four indicator LEDs, four relays, a 16-pixel WS2813 strip, an LDR, a passive piezo buzzer, and a KY-040 rotary encoder. Active-HIGH default; HAL flags opt individual pins into active-LOW. Single shared USB-CDC link to the daemon, multiplexed by the daemon's `iohub` subsystem.

#### ESP32 (HUB75 panel driver)

Drives the HUB75 LED panel: two Waveshare P2.5 64x32 panels chained for 128x32 logical resolution. USB-CDC link to the Pi. Owns its own state model (IDLE, PREFLIGHT, FLIGHT, ALARM, RTH, POSTFLIGHT) and renders modes based on commands from the daemon's `devices/display` subsystem. RP2040 was tried for this role and rejected: 3.3V signaling insufficient at the panel's input shift registers, level shifters explicitly ruled out per locked decision.

#### ESP32-S3 (antenna tracker, optional)

Pole-end add-on, not in the case. Sits inline on the wired CRSF path between the cable's pole-end MAX490 and the ELRS TX module's CRSF UART. Byte-pumps frames transparently in both directions on Core 1 at top priority (this is the safety floor; the only task registered with the hardware watchdog). Parses CRSF GPS telemetry on Core 0, computes az/el to the aircraft, drives a 2-DOF pan/tilt gimbal autonomously. Daemon-unaware. Removing the tracker (or hardware-bypassing the cable past it) requires zero daemon-side changes. Failsafe is hold-last-position by construction. Requires the extended cable configuration (RS-422), not deployable on single-wire CRSF.

### End-to-end signal path

Outbound (operator to aircraft):

```
USB joystick (HID) ----USB---> Pi 400 / zerotxd
                                    | reads axes & buttons
                                    | mixes against active EdgeTX model
                                    | (input map, expo, limits)
                                    v
                              channel intents
                                    |
                                    | USB-CDC (framed COBS + CRC)
                                    v
                                  RP2040
                                    | builds CRSF frame
                                    v
                              CRSF on the wire
                                    |
                                    | single-wire half-duplex
                                    | (5m manga blindado)
                                    v
                              ELRS TX module ----RF---> aircraft RX -> FC
```

Inbound (aircraft to operator) is the same path in reverse: ELRS TX emits CRSF telemetry on its UART, the RP2040 reads it off the wire and forwards over USB-CDC, the daemon parses frames into structured state, then fans out to the HUD (via WebSocket to the kiosk), to the HUB75 panel (via ESP32), to the VFD (via Mega), to the narrator (audio), and to the recorder (SQLite log).

The single-wire half-duplex CRSF is the default. In the extended cable configuration the wire is replaced by an RS-422 differential pair (MAX490 transceivers on each end), which lets cable runs go well beyond 5m cleanly and is also the substrate the inline antenna tracker requires. The RP2040 firmware is unchanged between configurations.

### Failsafe chain

Every wiring choice in this manual exists to make this chain reliable:

```
Pi daemon stops sending intents
    |  ~200ms
    v
RP2040 watchdog notices, stops emitting fresh CRSF
    |  ~600ms
    v
ELRS module sees no fresh data, declares link down
    |  ~150ms
    v
FC (INAV) failsafe triggers, executes its configured behavior
(RTH, land, hold; configured per airframe on the FC, not here)
```

Total roughly **950 ms** from "Pi-side daemon goes quiet" to "FC takes over."

Three things follow from this chain that the builder must respect:

1. **The hardware kill (e-stop, NC contacts) is wired into the module DC feed**, not into the Pi or the RP2040. Cutting power to the ELRS module is faster and more deterministic than asking software to stop. The chain still fires after a hardware kill (the module's link drop is what propagates to the FC), but the case-side path is no longer required.
2. **The daemon goes silent on loss of joystick input.** It does not emit last-known values forever; going silent is what makes the chain fire. No wiring depends on this, but it affects acceptance criteria in Section 7.
3. **The FC owns the post-failsafe behavior**, not ZeroTX. ZeroTX cannot guarantee RTH executes correctly; that is INAV configuration per airframe, tested on the bench before first flight.

### Power tree

A single 12VDC input on a panel-mount jack feeds the entire case. Battery backup is external (operator-supplied 12V SLA + charger upstream of the jack). Two 12V to 5V bucks split the 5V loads: one for the Pi 400, one for the powered USB hub.

```
12VDC input (panel jack)
    |
    +---- inline fuse ---- keylock switch ----+---- 12V bus
                                              |
12V bus ----+---- buck #1: 12V -> 5V ---- Pi 400
            |
            +---- buck #2: 12V -> 5V ---- powered USB hub
            |                              (Mega, ESP32 panel, joystick,
            |                               USB DAC, front-panel USB)
            |
            +---- direct 12V ---- audio amplifier
            |
            +---- direct 12V ---- voltmeter (display only)
            |
            +---- direct 12V via e-stop (NC) ---- ELRS module
                                                  (modules tolerate up to 16V)
```

Current budgets, fuse sizing, and bench-measured draws are detailed in Section 4 (Wiring). For now, the points worth absorbing are: two bucks (not one), ELRS direct off 12V (no module-side regulation), and the e-stop in series with the module DC feed (hardware kill).

## 2. Bill of materials

This section is the complete parts list for replicating a ZeroTX ground station. Every part needed inside and outside the case is listed here, organized by subsystem. Tables use the columns **Item / Qty / Notes**. Where a specific brand or model is named, it is the part being used in the reference build; substitutes are fine if they meet the noted specs.

Operator-supplied external gear (12V SLA + charger, joystick) is listed but not part of the case-internal BOM. Marked clearly where it differs.

### 2.1 Case and mechanical

| Item | Qty | Notes |
|---|---|---|
| Aluminum case, ~450 x 320 x 150 mm external | 1 | Hinged on long edge, toolbox/pelican style, trunk latches, side carry handle. Lid is passive (displays only); body holds everything else. |
| Vent mesh, replaceable | 1 | Behind body back-wall vent slots, dust filter |
| 40 mm 5V fan + bracket | 0-1 | Designed-in slot but not populated by default. Add only if summer use turns out warm. |

The lid carries only the two 7" displays bonded against the inside aluminum (thermal pad turns the lid into a heatsink). The body holds Pi 400, MCUs, audio, power conversion, panel switches/encoders, status surfaces (VFDs, GLCD, voltmeter, HUB75), and all cabling.

### 2.2 Computer (Pi 400 and accessories)

| Item | Qty | Notes |
|---|---|---|
| Raspberry Pi 400 | 1 | The brain; integrated keyboard |
| USB SSD, 256 GB+ | 1 | Boot drive. Faster and more reliable than microSD; ZeroTX boots from SSD via the Pi's USB 3.0 port. |
| microSD card, 32 GB+ | 1 | Optional spare for recovery boot or initial provisioning if SSD route is troublesome |
| Powered USB hub, 4-port, USB 2.0 | 1 | Hosts Mega 2560, ESP32 panel, USB DAC, HDMI capture dongle, and front-panel USB-A breakout. Powered from internal buck #2. |
| USB HDMI capture dongle | 1 | For Pi-side ingest of the Walksnail VRX HDMI output (DVR, overlay, streaming) |

Pi 400 USB port allocation:

| Port | Use |
|---|---|
| USB 3.0 | Boot SSD |
| USB 3.0 | RP2040-Zero (USB-CDC link to CRSF endpoint) |
| USB 2.0 | Powered USB hub (Mega 2560 + ESP32 panel + joystick + USB DAC + HDMI capture + front-panel USB-A) |

The RP2040 gets a dedicated Pi root port (not behind the hub) for jitter and reliability isolation. CRSF I/O is the most safety-critical USB path in the system; putting it behind a hub introduces failure modes (hub power glitch, bandwidth contention with HID, enumeration races on cold boot) that don't exist on a direct connection.

### 2.3 Microcontrollers

| Item | Qty | Notes |
|---|---|---|
| RP2040-Zero | 1 | CRSF endpoint. USB-C to Pi over USB-CDC; CRSF over wire to ELRS module. Hardware watchdog firmware (m1.8-wdt). |
| Raspberry Pi Pico (original) | 1 | Spare/backup for the RP2040-Zero |
| Arduino Mega 2560 | 1 | IO hub: drives VFDs, GLCD, panel buttons, indicator LEDs, relays, encoders. Single shared USB-CDC link to the Pi multiplexed by the daemon's `iohub` subsystem. |
| ESP32 dev board with native USB | 1 | HUB75 panel driver. USB-CDC to the Pi. Specific board: TODO confirm during procurement. |
| USB-C right-angle cable, ~30 cm | 1 | Pi 400 to RP2040-Zero |
| Internal USB-A to USB-B cable, ~30-50 cm | 1 | Mega 2560 to USB hub |
| Internal USB-A to USB-C/micro cable, ~30-50 cm | 1 | ESP32 to USB hub (length and connector depend on chosen ESP32 board) |

### 2.4 Displays (HUD and Map LCDs)

| Item | Qty | Notes |
|---|---|---|
| 7" 1024 x 600 HDMI touchscreen | 2 | Identical units, purchased from the same seller, lid-mounted |
| Right-angle micro-HDMI to HDMI cable | 2 | Pi 400 micro-HDMI out to display HDMI in, internal run through hinge |
| USB-C cable for display power | 2 | Display power on the 5V rail from buck #1 |
| USB-C cable for display touch | 2 | Display touch to Pi 400 via the USB hub |
| Thermal pad, ~1 mm thick | 1 | Cut to match each display's metal backplate; turns the lid aluminum into the displays' heatsink |

Touch is low-bandwidth USB HID; routing it through the USB 2.0 hub is fine.

### 2.5 HUB75 LED panel

| Item | Qty | Notes |
|---|---|---|
| Waveshare P2.5 64x32 RGB LED panel | 2 | Chained for 128x32 logical resolution. P2.5 = 2.5 mm pixel pitch. |
| HUB75 ribbon cable (16-pin) | 1 | Panel-to-panel chain and panel-to-driver |
| 5V supply line to first panel | 1 | From buck #1 / 5V rail; current sized for full-white worst case (see Section 2.15) |
| Front-panel mounting frame, 3D-printed | 1 | Holds the chained 128x32 assembly behind a tinted bezel cutout |

The ESP32 drives the panel directly; no level shifters between ESP32 and panel inputs. RP2040 was tried for this role and rejected (3.3V signaling insufficient at the panel's input shift registers).

### 2.6 Status row (VFDs, GLCD, voltmeter, level shifter)

| Item | Qty | Notes |
|---|---|---|
| Noritake CU20025ECPB-W1J 2x20 VFD | 2 | HD44780 4-bit parallel; 8 conductors per unit (6 GPIO + 2 power). Japanese ROM A00 variant. `vfd.0` wired and driven; `vfd.1` is a reserved slot on independent Mega pin groups. |
| 128x64 ST7920 graphic LCD | 1 | 3-wire serial mode (CS/SID/CLK) over Mega hardware SPI. Hosts artificial-horizon "cool factor" HUD via the `glcd` Mega subsystem. Never on the safety path; loss of GLCD does not block flight. |
| 5V 8-channel level shifter (74AHCT125 or similar) | 1 | For VFD data/control lines |
| Self-contained 7-segment LED voltmeter | 1 | Direct to 12V rail; runs zero software; visible at-a-glance health check |

### 2.7 Control row (switches, encoders, big button, keylock, e-stop)

Panel-mount, classic instrument style. Knob style (knurled metal, pointer skirt, etc.) and exact mounting decisions deferred to procurement.

Switches:

| Item | Qty | Notes |
|---|---|---|
| 3-position toggle (ON-OFF-ON), 12 mm panel hole | 4 | |
| 2-position toggle (ON-ON), 12 mm panel hole | 2 | |
| Safety toggle with cover, missile-style | 1 | Red cover; second use of red after the e-stop |

Rotaries:

| Item | Qty | Notes |
|---|---|---|
| Rotary encoder with push, 6 mm shaft | 4 | KY-040 module style or equivalent |
| 6-position rotary selector, 1P6T mechanical | 1 | |

Buttons and safety:

| Item | Qty | Notes |
|---|---|---|
| Large momentary push, 16-22 mm, distinctive color | 1 | Arm-confirm momentary (three-input arming workflow). Wired to RP2040 GPIO 15, internal pull-up, switch to GND. Press-only; firmware does not emit a release event. Distinctive color, NOT red. |
| Keylock master power switch, 19-22 mm | 1 | On input rail downstream of fuse |
| Emergency stop, mushroom-head, latching, NC contacts | 1 | NC contacts inline on ELRS module DC feed; hardware kill path |

Internal supporting hardware:

| Item | Qty | Notes |
|---|---|---|
| Switch breakout perfboard | 1 | Aggregates panel inputs into the harness going to the Mega and RP2040 headers |

Optional: switches with embedded LEDs. If chosen, can be driven by the RP2040 GPIOs or hardwired to the keyed-on rail for a simple "system on" indicator.

### 2.8 Audio

| Item | Qty | Notes |
|---|---|---|
| Generic USB audio board (USB DAC) | 1 | Plugged into the USB hub; provides the Pi's audio output path. Specific model: TODO confirm. |
| Audio amplifier, 12V input | 1 | Class D or similar; drives the case speaker. Runs directly off the 12V rail (not the 5V buck). |
| Speaker, panel-mount | 1 | Specific size and impedance: TODO confirm. Sized for narration intelligibility, not high fidelity. |
| Speaker grille | 1 | Front-panel cutout aligned with the speaker; protective mesh |

The audio path is ALSA out of the Pi to the USB DAC, line-level into the amp, amp out to the speaker. Two audio tiers in software: pre-baked WAV samples for safety-critical alarms (link loss, failsafe), Piper TTS for everything else.

### 2.9 Power

External (operator-supplied, not part of the case BOM):

| Item | Qty | Notes |
|---|---|---|
| 12V SLA battery + charger / UPS unit | 1 | CCTV-style or equivalent. Capacity chosen for desired field runtime. Sits upstream of the case input. |

Internal:

| Item | Qty | Notes |
|---|---|---|
| Panel-mount DC barrel jack, 12VDC input | 1 | Rear panel; case-side 12V entry |
| Inline fuse holder + fuse, ~10 A | 1 | Fuse rating finalized after measured peak load |
| Buck converter, 12V to 5V at 3 A+ | 2 | One for Pi 400 (and downstream USB devices the Pi powers, including the boot SSD and HDMI displays), one for the powered USB hub |
| Terminal block, 12V distribution | 1 | Distributes 12V to: bucks (x2), audio amp, voltmeter, ELRS module DC feed (via e-stop) |
| Terminal block, 5V distribution | 1-2 | One per buck output |
| Schottky diodes (optional) | 0-2 | Two in series can drop ~0.8V on the module DC feed if a future ELRS module turns out to prefer lower than 12V |

The keylock master switch (listed in 2.7) is on the input rail downstream of the fuse. The e-stop (also listed in 2.7) has its NC contacts in series with the ELRS module DC feed for hardware kill. Neither of those is duplicated in this table.

### 2.10 Cabling and connectors

Case-to-pole (default cable configuration):

| Item | Qty | Notes |
|---|---|---|
| Shielded multi-core cable, 5m | 1 | "Manga blindada" / shielded mic-style cable. Conductor count: minimum 4 (signal CRSF, signal GND, V+ 12V, V- power GND). Default cable configuration is single-wire half-duplex CRSF; 470 Ω series resistor at the case end on RP2040 GP0/TX line merges TX and RX. |
| Cable gland or strain relief, case-end | 1 | Where the cable exits the rear bulkhead |
| Panel-mount connector for cable, case-end | 1 | Specific connector TBD; multi-pin DIN, M12, or similar |
| Mating connector, pole-end | 1 | Mates the cable to the ELRS module housing |

Note: Cat6 + RS-422 (MAX490) is the **extended** cable configuration used only when a pole-end antenna tracker is fitted. See 2.13.

Internal harnesses:

| Item | Qty | Notes |
|---|---|---|
| Hookup wire, 22-24 AWG, multiple colors | as needed | For Mega/RP2040 inputs, panel switches, LEDs |
| 12V distribution wire, 18 AWG | as needed | Higher current for ELRS module feed and audio amp |
| Heat-shrink, assorted | as needed | |
| JST or Dupont pin headers and crimps | as needed | For modular subsystem connections |

Front-panel and rear-panel connectors:

| Item | Qty | Notes |
|---|---|---|
| Panel-mount USB-A, front | 1-2 | One for joystick (always-present); second for ad-hoc / charging |
| Speaker grille | 1 | Listed in 2.8 |
| Vent mesh | 1 | Listed in 2.1 |
| Panel-mount DC jack, rear | 1 | Listed in 2.9 |
| Case-to-pole connector, rear | 1 | Listed above |

### 2.11 ELRS module and pole-mount RF

Mounted externally on a pole, connected to the case via the cable above.

| Item | Qty | Notes |
|---|---|---|
| ELRS TX module | 1 | HappyModel ES900TX (900 MHz) or RadioMaster Ranger 2.4 GHz. User picks per band/range requirements. |
| 3D-printed module housing | 1 | Pole-mount enclosure; protects module and connections from weather |
| Pole-mount hardware | 1 | Bracket, U-bolts, clamp; specifics depend on pole size |
| Antennas, pole-mounted | 1+ | Per ELRS module antenna requirements (typically 1x for TX) |

Operator supplies their own pole or tripod.

### 2.12 Video downlink (FPV)

Separate from the data link. Two parallel video paths: analog via the Aomway built-in receiver, and digital via the Walksnail Avatar HDMI out into a splitter.

| Item | Qty | Notes |
|---|---|---|
| Aomway 7" monitor (analog VRX + HDMI input) | 1 | Built-in analog receiver with antenna; also accepts HDMI input from the Walksnail splitter |
| Walksnail Avatar VRX | 1 | Digital video receiver |
| Powered HDMI splitter | 1 | Walksnail HDMI out feeds into the splitter; outputs to Aomway HDMI input AND USB HDMI capture dongle (the latter is listed in 2.2) |
| HDMI cable, Walksnail to splitter | 1 | |
| HDMI cable, splitter to Aomway HDMI input | 1 | |
| HDMI cable, splitter to capture dongle | 1 | |

The Aomway runs analog by default off its built-in antenna. The Walksnail provides digital. The splitter lets one Walksnail feed go to both the Aomway HDMI port AND the Pi for capture/DVR/overlay. Pick the active video feed at the operator's discretion in flight.

### 2.13 Antenna tracker pole-end (optional)

Optional pole-end add-on. Requires the **extended** cable configuration (RS-422 over Cat6 or equivalent) instead of the default single-wire CRSF. The tracker sits inline on the wired CRSF path between the cable's pole-end RS-422 transceiver and the ELRS module's CRSF UART.

Status: hardware partially specified, not yet integrated. Selections marked TODO.

| Item | Qty | Notes |
|---|---|---|
| ESP32-S3 dev board with native USB | 1 | Specific board: TODO confirm |
| MAX490 RS-422 transceiver module | 2 | One at the case end, one at the pole end |
| Cat6 or equivalent cable | 1 | Length set after measuring pole-mount distance. Conductor allocation: pair 1 (TX differential), pair 2 (RX differential), pair 3 (V+), pair 4 (V-). T568B color order. At 35 m worst case with 2 A peak draw, voltage drop is approx 2.8 V (still within ELRS module input window when fed from a 12V rail). |
| RJ45 keystone panel jack, case-end | 1 | |
| RJ45 locking boot, cable-end | 1 | |
| 2-DOF pan/tilt gimbal kit | 1 | Two hobby servos, 2-axis mount. Specific kit: TODO confirm |
| Pole-end project box | 1 | Houses tracker + pole-end MAX490 + servo wiring; weather-resistant. TODO specify. |

The tracker firmware byte-pumps frames transparently between MAX490 (case-side) and ELRS module on Core 1 at top priority; the only task registered with the hardware watchdog. Parser, math, servo loop, and console run on Core 0. Failsafe is hold-last-position by construction.

### 2.14 Fasteners, 3D-printed parts, panel fabrication

3D-printed parts (PETG):

| Item | Qty | Notes |
|---|---|---|
| Front panels (3D-printed) | as designed | Primary panel material. Print holds cutouts for VFDs, GLCD, voltmeter, switches, encoders, buttons, keyboard well, speaker grille. Labels modeled into the print or applied as overlays. Cut acrylic remains an option for any panel where 3D-printed quality is insufficient. |
| Lid panel (3D-printed) | 1 | Display cutouts; bonds against lid aluminum via thermal pad |
| Internal component supports | as designed | Brackets and standoffs for Pi 400, MCUs, level shifter, amp, bucks, terminal blocks |
| Module housing (pole-end) | 1 | Already listed in 2.11 |

Materials and hardware:

| Item | Qty | Notes |
|---|---|---|
| PETG filament | sufficient | For all printed parts |
| M3 standoffs, 10-15 mm | bulk | Mounting electronics to the case interior |
| M3 screws, washers, lockwashers | bulk | |
| Wood blocks | as needed | Alternative for select supports if more convenient than printing |

Optional panel alternative:

| Item | Qty | Notes |
|---|---|---|
| Black acrylic 3 mm sheet, lid panel | 0-1 | Fallback if 3D-printed lid panel doesn't satisfy fit / finish / optical needs. Laser-cut and engraved. |
| Black acrylic 3 mm sheet, body panel | 0-1 | Same fallback for body panel |
| 1 mm acrylic or cardboard test piece | 0-1 | Fit-check before committing to 3 mm panels |

### 2.15 Power budget

Per-component current/power draw, used to size the 12V fuse, the bucks, the case-to-pole cable conductor gauge, and the operator's external SLA capacity.

| Source | Steady state | Peak transient |
|---|---|---|
| Pi 400 | 3-5 W | 7 W |
| RP2040-Zero | <0.5 W | <1 W |
| 5V buck losses (~85% efficient) | 1-2 W | 3 W |
| Audio amp (idle) | <1 W | several W during narration / alarms |
| ELRS module | 5-8 W | up to 25 W full TX |
| 2x 7" displays | 5-8 W | 10 W |
| Voltmeter, VFD, GLCD | <1 W | <1 W |
| **Total inside the case** | **~10-16 W** | **~30 W** |

The aluminum case at ~0.5 m^2 surface area dissipates 15-20 W passively at modest temperature rise, so steady state runs cool without forced airflow. Transient peaks (full-power TX during link tests) are brief and not thermally significant.

For a 4-hour field session at ~15 W average, an external 12V SLA needs roughly 60 Wh; a 7 Ah SLA at 12V provides ~84 Wh, comfortable margin.

### 2.16 Hinge cable bundle

Four cables cross the hinge between body and lid. No power, no microcontroller, no status surfaces in the lid; the lid is passive.

| Cable | Conductors | Notes |
|---|---|---|
| HDMI display 1 | (cable) | Thin or flat HDMI preferred for hinge flex |
| HDMI display 2 | (cable) | Same |
| USB-C, display 1 | 4 | Power + touch on one cable |
| USB-C, display 2 | 4 | Same |

Bundled with spiral wrap, anchored at both halves with cable clamps. ~30 cm of slack to allow ~110 deg of hinge rotation without strain.

### 2.17 Outstanding decisions

Items deferred until parts arrive, the build progresses, or the design proves itself in early flights. None of these block first power-on or first flight; they refine the build.

- Knob style for encoders and 6POS selector (knurled metal, pointer skirt, etc.)
- Big button color (distinctive, non-red; the safety toggle and e-stop already use red)
- Whether any panel switches will be illuminated (depends on per-part LED availability)
- Hazard tape (yellow/black) around the e-stop
- Label engraving aesthetic (3D-printed embossed, laser-engraved black acrylic, vinyl overlay)
- Whether to populate the body cooling fan (added only if summer use proves warm)
- Whether to add an auxiliary instrument in the empty area below the LCDs in the lid
- Final case-to-pole cable length (set after measuring pole-mount distance)
- ESP32 board model (specific dev board not yet committed)
- ESP32-S3 board model for the tracker (not yet committed)
- 2-DOF pan/tilt gimbal kit for the tracker (not yet committed)
- Pole-end project box for the tracker (not yet committed)
- USB DAC model (generic, but specific board not yet committed)
- Case speaker model and impedance (not yet committed)

### 2.18 Sourcing notes and substitution guidance

What can be substituted freely, what should be matched closely, what is locked.

**Locked** (substitution will break the build or break a locked decision in DECISIONS.md):

- Raspberry Pi 400 (form factor, integrated keyboard, dual micro-HDMI, 3 USB ports are all assumed by the build)
- RP2040 (CRSF firmware targets RP2040 specifically; hardware watchdog use)
- Mega 2560 (Mega IO firmware uses Mega-specific pin counts and HAL EEPROM)
- ESP32 with native USB for the HUB75 driver (RP2040 was rejected at this role; 3.3V signaling insufficient at panel input shift registers)
- Noritake CU20025ECPB-W1J VFDs (HD44780-compatible 2x20 with the specific A00 Japanese ROM behavior)
- ST7920 128x64 graphic LCD (Mega `glcd` subsystem assumes this controller)
- ELRS protocol on the wire (CRSF-over-UART, half-duplex on default cable)

**Match closely** (substitution OK if specs match):

- 7" 1024 x 600 HDMI touchscreens (any pair of identical units with the same panel and same touch interface)
- Powered USB hub (any 4-port USB 2.0 hub with external power input; the Pi cannot power everything itself)
- Audio amp (any class D module rated for 12V input and the chosen speaker impedance)
- Bucks (any 12V to 5V converter rated for 3 A+ continuous, with input/output capacitors sized appropriately)
- Toggles, encoders, big button (any panel-mount parts matching the hole sizes listed)

**Free substitution** (any equivalent part):

- Cabling (any shielded multi-core for the case-to-pole run, any hookup wire for internal harness)
- Fasteners (M3 is the assumed thread, but lengths and finishes are flexible)
- 3D-printed parts (filament brand, color, layer settings all up to the builder)
- USB DAC (any USB-Audio Class compliant board the Pi enumerates and ALSA can drive)
- microSD / SSD brands

Total cost ballpark: building from zero (no parts on hand) the project sits roughly in the **mid hundreds of USD** range, dominated by the case, the two LCDs, the Pi 400, the video downlink gear (Walksnail VRX especially), and the ELRS modules. Exact figure depends heavily on regional pricing and what's already on the bench.

## 3. Mechanical assembly

> **Placeholder.** Section to be filled after first physical build is complete and the 3D-printed panels are fitted. Planned sub-sections: case prep, mounting plan, hinge bundle, lid bonding, panel fabrication (3D-printed primary, machined/cut alternative noted), 3D-printed internal supports, status/control row mounting.

## 4. Wiring

### 4.1 Reading this section

This section covers physical wiring inside and outside the ZeroTX case: power distribution, USB topology, MCU connections, the case-to-pole cable, and the panel and lid harnesses. Read it with the BOM (Section 2) and the topology diagram (Section 1.2) at hand.

Conventions:
- **Active-HIGH default** project-wide. The Mega IO HAL has per-pin flags to opt individual pins into active-LOW where wiring requires it; that detail is captured in the canonical Mega pin reference (Appendix A).
- **Pin tables** in this section are limited to what's needed to wire the subsystem. Full per-MCU pin tables and HAL flag inventories live in Appendix A.
- **Single 12VDC input** is the only power input to the case. Battery backup is external (operator-supplied 12V SLA + charger upstream). See Section 1.6 and 2.9 for the topology and BOM; this section is the wiring detail.
- **Items marked TODO** are values to confirm during physical assembly (specific connector models, exact cable lengths, bench-measured currents). Don't proceed past a TODO without a real value or an explicit decision to defer.

### 4.2 Power distribution

#### Rail topology

```
12VDC input (panel jack)
    |
    +---- inline fuse ---- keylock switch ----+---- 12V bus
                                              |
12V bus ----+---- buck #1: 12V -> 5V ---- Pi 400 (USB-C)
            |                              + downstream USB devices the Pi powers
            |                                (boot SSD, HDMI displays via Pi USB-C)
            |
            +---- buck #2: 12V -> 5V ---- powered USB hub
            |                              (Mega, ESP32 panel, joystick,
            |                               USB DAC, front-panel USB-A)
            |
            +---- direct 12V ---- audio amplifier
            |
            +---- direct 12V ---- voltmeter (display only)
            |
            +---- direct 12V via e-stop (NC) ---- ELRS module
                                                  (modules tolerate up to 16V)
```

Three rules that wiring choices in this section serve:

1. **The hardware kill cuts the ELRS module's DC feed directly.** The e-stop's NC contacts are in series between the 12V bus and the ELRS module. Pressing the e-stop physically opens the module's power. The kill is not routed through the Pi or any MCU; it's a pure mechanical circuit break.
2. **Two bucks, not one.** Buck #1 feeds the Pi 400 (and everything the Pi powers via USB-C: boot SSD on USB 3.0, HDMI displays on USB-C). Buck #2 feeds the powered USB hub (and all its downstream devices). Separate bucks isolate Pi brownout from hub-side transient loads (joystick hotplug, USB DAC spin-up, MCU resets).
3. **ELRS module runs direct off 12V.** No module-side buck. ELRS modules accept up to ~16V, so the 12V rail is within spec. If a future module turns out to prefer lower voltage, two Schottky diodes in series drop ~0.8V on the module DC feed without rebuilding the rail.

#### Safety wiring

| Element | Position | Why |
|---|---|---|
| Inline fuse | Between barrel jack and keylock switch | Protects against short-circuit faults in the rest of the case. Sized after measured peak (see budget below); ~10 A starting placeholder. |
| Keylock master switch | Between fuse and 12V bus | Operator-level on/off; key-removed = case is electrically off |
| E-stop, NC contacts | In series with ELRS module DC feed | Hardware kill of the RF transmitter; opens module supply when pressed (latching). |

The fuse is upstream of everything else, including the keylock. A short downstream of the keylock blows the fuse, not the operator's hand. The keylock removes operator-accessible power without requiring fuse handling.

#### Per-component power budget

Initial estimates. Refine with bench measurements once the assembly is complete and the regulator topology is finalized. The "TODO peak" rows are where the build expects to find a number after first power-on.

| Component | Voltage | Est. current | Notes |
|---|---|---|---|
| Pi 400 | 5V | 1.5A typical, 2.5A peak | USB-C input |
| Boot SSD | 5V | 0.5A typical | via USB |
| HUB75 panels (2x P2.5 64x32) | 5V | TODO peak | Dominant load; can hit 4-6A at full white |
| HUD LCD | TODO | TODO | Model-dependent |
| Map LCD | TODO | TODO | Model-dependent |
| Mega 2560 | 5V | ~0.2A | via USB |
| ESP32 (panel driver) | 5V | ~0.3A | via USB |
| RP2040 (CRSF endpoint) | 5V | ~0.1A | via USB |
| Noritake VFD (CU20025ECPB-W1J) | 5V | TODO | Confirm against datasheet |
| ST7920 GLCD (128x64) | 5V | <0.1A | |
| Self-contained voltmeter | 12V | <0.05A | |
| Audio amplifier | 12V | <1A idle, several A loud | |
| Powered USB hub | 5V (from buck #2) | TODO | Sizing depends on hub model and downstream load |
| ELRS TX module (default) | 12V via cable | ~1A peak | Direct off the case 12V rail through the pole cable |
| Pole-end project box (extended cfg only) | 12V via cable | TODO | Tracker + ELRS + 2x servos under bucks; servos dominate |

**Steady-state total** inside the case: ~10-16 W. **Peak transient**: ~30 W (full-power TX during link tests, all displays at full brightness, audio loud). The aluminum case at ~0.5 m² surface dissipates 15-20 W passively at modest temperature rise, so steady state runs cool without forced airflow.

**To confirm during first power-on:**
- HUB75 5V peak at maximum brightness with all LEDs lit. The 4-6A estimate is the worst case; the dominant uncertainty for buck #1 sizing.
- Powered USB hub's input current with all downstream devices enumerated and active.
- HUD/Map LCD inputs (some 7" panels take 12V via a barrel jack rather than 5V via USB-C; confirm against the actual unit purchased).

#### Same input, field or lab

Field or lab makes no difference to the case. The same 12VDC barrel jack accepts a bench supply, a 12V SLA + charger pack, or a vehicle 12V outlet; the case is source-agnostic above the jack.

### 4.3 USB topology

The Pi 400 has 3x external USB-A ports (2x USB 3.0, 1x USB 2.0), plus its internal keyboard. Three things hang directly off the Pi: the boot SSD, the RP2040 CRSF endpoint, and a powered USB hub. Everything else lives behind the hub.

**Why the RP2040 gets a dedicated port (not behind the hub):** CRSF I/O is the most safety-critical USB path in the system. Putting it behind a hub introduces failure modes (hub power glitch, bandwidth contention with HID, enumeration races on cold boot) that don't exist on a direct connection.

```
Pi 400 USB port A (USB 3.0)
  +-- Boot SSD                  (root filesystem; system boots from this)

Pi 400 USB port B (USB 3.0)
  +-- RP2040 CRSF endpoint      (id: usb-Raspberry_Pi_Pico_<serial>)

Pi 400 USB port C (USB 2.0)
  +-- Powered USB hub
        +-- Mega 2560 IO board       (id: usb-Arduino_LLC_Mega_2560_R3_<serial>)
        +-- ESP32 panel driver       (id: usb-<vendor>_<chip>_<serial>)
        +-- USB joystick             (Thrustmaster T.Flight Hotas X)
        +-- USB DAC                  (audio out, generic USB Audio Class)
        +-- USB HDMI capture dongle  (Walksnail VRX HDMI capture for the Pi)
        +-- Front-panel USB-A x N    (ad-hoc, charging, dev access)
```

**TODO:** confirm which physical Pi port each device lands on after final assembly. Document the final assignment in the udev rules and in this table.

**Stable enumeration names.** The daemon launches against stable names under `/dev/serial/by-id/`. udev rules in `/etc/udev/rules.d/` further alias them where useful (e.g., `/dev/zerotx-rp2040`). udev setup is covered in Section 6 (Pi provisioning).

**Powered USB hub power.** Buck #2's 5V rail feeds the hub. The hub does **not** run from the Pi's USB-C power; it gets its own 5V/3A+ supply from the dedicated buck. This protects the Pi from any hub-side transient.

**TODO:** finalize the hub model and confirm whether it's powered through its own external brick (then ignored) or fed from buck #2 (then wired in). Buck #2 is the documented choice; an external-brick hub is acceptable but introduces an extra power cord at the case.

### 4.4 Display signal paths

#### HDMI to lid LCDs

The Pi 400 has 2x micro-HDMI ports: **HDMI0** (next to the USB-C power connector) and **HDMI1**.

| Pi port | Cable | Destination |
|---|---|---|
| HDMI0 | micro-HDMI to HDMI, right-angle, through hinge | HUD LCD (left in lid) |
| HDMI1 | micro-HDMI to HDMI, right-angle, through hinge | Map LCD (right in lid) |

**TODO:** confirm which physical port maps to HUD vs Map after final assembly. The kiosk autostart in `~/.xinitrc` (covered in Section 6.10) sets the `xrandr` mapping that determines which page lands on which display. If the mapping turns out swapped, swap it in `~/.xinitrc`, not by relabeling the cables.

LCD power and touch are USB-C, not HDMI; covered separately in Section 4.9 (Lid wiring).

#### HUB75 panel chain

ESP32 drives two Waveshare P2.5 64x32 panels chained in series for 128x32 logical resolution. Standard HUB75 16-pin ribbon between ESP32 and Panel A IN, then Panel A OUT to Panel B IN. Panel B OUT is unused.

```
ESP32 GPIO --(IDC 16-pin ribbon)--> Panel A (IN)
                                    Panel A (OUT) --(ribbon)--> Panel B (IN)
                                                                Panel B (OUT) unused

5V high-current rail --(thick gauge, short run)--> Panel A power -> Panel B power
GND star-point --> shared with ESP32 GND, panel power return
```

ESP32 GPIO mapping (R1, G1, B1, R2, G2, B2, A, B, C, D, E, CLK, LAT, OE) is set in the ESP32 panel firmware; full pin assignment in Appendix A.

**Power for the panels** is a separate high-current 5V run (thick gauge, short length to minimize voltage drop) tapped from buck #1. Panel ground returns to the same buck's GND through a star-point.

#### VFD and GLCD on Mega

Noritake CU20025ECPB-W1J 20x2 VFD, driven by Mega via HD44780 4-bit interface. 6 Mega GPIOs (D4-D7, RS, E) drive the VFD data and control lines, through a 5V 8-channel level shifter (74AHCT125 or similar) to clean up the Mega's 5V logic to the VFD's 5V logic. Pin assignment in Appendix A.

ST7920 128x64 graphic LCD, driven by Mega via 3-wire serial mode over hardware SPI:

| Mega pin | LCD pin | Function |
|---|---|---|
| 51 (MOSI) | SID (D11) | Serial data in |
| 52 (SCK) | CLK (D10) | Serial clock |
| 53 (SS) | CS (D5) | Chip select |
| 5V | VCC | Power |
| GND | GND, PSB | PSB tied to GND selects serial mode |

GLCD pins 7-14 are unused.

The GLCD hosts the artificial-horizon HUD via the `glcd` Mega subsystem. It is never on the safety path; loss of the GLCD doesn't block flight (the LCD HUD page on the lid LCD is the authoritative attitude display).

### 4.5 Mega IO connections

Pin assignments for all Mega-attached peripherals are managed via the HAL EEPROM v3 system in the Mega IO firmware. The active configuration can be read and modified with `tools/zerotx-iohal-config/` (covered in Section 5.2).

**Currently fitted:** VFD (`vfd.0`), 6 of the 10 button slots (`button.0..button.5`), the 128x64 GLCD (`glcd`).

**Scaffolded** (firmware supports, no hardware fitted yet): second VFD on independent pin group (`vfd.1`), I2C LCD (`lcd.0`), four indicator LEDs (`led.0..3`), four relays (`relay.0..3`), 16-pixel WS2813 strip (`ws.0`), LDR (`ldr.0`), passive piezo buzzer, KY-040 rotary encoder (`enc.0`).

The canonical Mega pin table (subsystem to pin, with HAL flags) is reproduced in Appendix A. **Do not memorize pin numbers from this section**; pull them from Appendix A or read them off the running Mega via `zerotx-iohal-config`. Pin numbers may be reassigned between HAL versions; the firmware is the source of truth for the active build.

Project-wide convention: **active-HIGH default**, per-pin HAL flag opts into active-LOW where wiring requires it (typical for switches with internal pull-ups and a switch-to-GND wiring).

### 4.6 RP2040 wiring

The RP2040 has the smallest pin count of any MCU in the build and the most safety-critical responsibilities. Inline table here covers everything.

| Pin | Direction | Function | Wiring notes |
|---|---|---|---|
| USB-C | host | USB-CDC + 5V power | To Pi USB port B (direct, NOT through hub). Both data and power on the USB-C. |
| GP0 | output | UART0 TX to ELRS module (CRSF) | Hardware UART. 470 Ω series resistor at the case end of the cable merges TX and RX for single-wire half-duplex CRSF (default cable). In extended cable config, GP0 goes to MAX490 DI (no resistor). |
| GP1 | input | UART0 RX from ELRS module (CRSF) | Hardware UART, telemetry return path. In default cable config, GP1 connects to the same single wire as GP0 after the resistor. In extended config, GP1 comes from MAX490 RO. |
| GP14 | input | Aviator-style arm key (SF-equivalent) | Internal pull-up. Switch to GND. Far from UART and LED to avoid timing-sensitive neighbours. Logical UP = switch closed = pin LOW. |
| GP15 | input | Arm-confirm momentary (SH-equivalent) | Internal pull-up. Switch to GND. Press fires `armMachine.Confirm()` in the daemon. Press-only signal; release is not emitted. Adjacent to GP14 so one panel cable carries GP14, GP15, and shared GND. |
| GND | shared | Signal and chassis return | Shared with the case-to-pole cable GND. Star-point at the case-end bulkhead. |
| WS2812 status LED (onboard) | output | Onboard RP2040-Zero RGB LED | Status indicator: green = streaming intents, amber = idle, red = error/fault. No external wiring needed. |

**Single-wire CRSF wiring detail (default cable):**

The 470 Ω resistor sits at the case end on the TX line. TX and RX merge to a single wire on the cable. The resistor prevents the RP2040 from short-circuiting the ELRS module when the module drives telemetry back, and lets the wire be high-impedance enough for the module to override. Half-duplex contention is managed by the protocol (CRSF) which expects directional turnaround per frame.

```
RP2040 GP0 (TX) ----[470 Ω]----+-------+
                               |       |
                               |    case-to-pole single wire ----> ELRS module CRSF pin
                               |
RP2040 GP1 (RX) ---------------+
```

**Software fallback for the momentary (GP15):** when the physical button is unavailable (bench rigs, partial builds), pressing **Ctrl+Alt+A** in either kiosk (HUD or Map) POSTs `/api/v1/arm/confirm` to the daemon, the same call the daemon makes when it receives an IPC press event from the RP2040. Both paths converge on `armMachine.Confirm()`. Useful during build for verifying the arm flow without panel wiring complete.

### 4.7 Case-to-pole cable

Two configurations. The default is short, simple, and the daily-driver choice. The extended configuration is for long cable runs and is the substrate the antenna tracker requires.

#### 4.7.1 Default configuration (single-wire, up to 5m)

5m, 4-conductor shielded multi-core cable (`cabo manga blindado 4x0.5mm² malha de cobre puro flexível`). Runs from the case rear bulkhead directly to the pole-mounted ELRS TX module's CRSF connector.

| Conductor | Function | Notes |
|---|---|---|
| 1 | CRSF signal | Single wire; TX merged into RX through 470 Ω at case end |
| 2 | Signal GND | Pair with conductor 1 |
| 3 | 12V power | To ELRS module DC input |
| 4 | Power GND | Star-point at case GND |
| Outer shield | Chassis GND, case end only | Single-end shield termination to avoid ground loops |

```
Case end                                                            Pole end
+--------+                                                          +-----+
| RP2040 | TX (GP0) -----[470 Ω]-----+                              |     |
|        | RX (GP1) -----------------+----------- signal ---------> |ELRS |
|        | GND ------------------------- signal GND --------------> |     |
+--------+                                                          | TX  |
12V rail (via e-stop NC) ----------- 12V cable -------------------> |     |
GND ------------------------- power GND --------------------------> |     |
                                                                    |     |
Outer shield: chassis GND at case end only.                         +-----+
```

No transceivers, no pole-end electronics. CRSF is half-duplex on a single wire; the 470 Ω series resistor on TX prevents driver contention with the module's telemetry direction.

**Case-end termination:** the cable enters through a panel-mount connector (specific connector model: TODO; multi-pin DIN, M12, or aviation-style locking connector are all candidates). Strain relief and cable gland at the bulkhead.

**Pole-end termination:** terminates inside the 3D-printed module housing. Solder joints under the housing's strain relief; module CRSF and DC inputs wired direct.

#### 4.7.2 Extended configuration (RS-422, longer runs and tracker support)

Replaces the default single-wire CRSF with an RS-422 differential pair driven by MAX490 transceivers at each end. **Required** for the inline antenna tracker (the tracker firmware byte-pumps RS-422 between the pole-end MAX490 and the ELRS module's CRSF UART, so the substrate has to be RS-422). **Recommended but not required** for cable runs significantly longer than 5m where single-wire TTL CRSF signal integrity becomes uncertain; single-wire may continue to work at longer distances depending on the cable, the routing environment, and acceptable bit-error tolerance. Build it, bench it; upgrade to MAX490 if you see bit errors or telemetry dropouts in normal use.

Cable: Cat6 or equivalent shielded twisted pair, T568B color order.

| Pair | Color | Function |
|---|---|---|
| 1 | Orange / White-Or | RS-422 differential, signal A and B (twisted) |
| 2 | Green / White-Gn | reserved spare differential, or split for additional signal (twisted) |
| 3 | Blue / White-Bl | V+ paralleled (12V) |
| 4 | Brown / White-Br | V- paralleled (GND) |

At 35m worst case with 2 A peak draw, voltage drop is approximately 2.8 V, leaving the module within its input window when fed from the case 12V rail.

```
Case end                                     Pole end (project box)
+----------+                                 +----------+
| RP2040   |  CRSF UART (TTL)                |  MAX490  |
|  (CRSF)  |---->----+                       |          |
|          |<----+   |    +-------------+    |          |
+----------+     |   v    | RS-422 pair |    |          |
                 |  +-------------------+--->|          |
                 +--| Case-end MAX490   |    |          |
                    +-------------------+<---|          |
                                             +----+-----+
                                                  |
                                                  v (CRSF TTL)
                                          +-------+--------+
                                          | ESP32-S3       |
                                          | tracker        |
                                          | (byte-pump)    |
                                          +-------+--------+
                                                  |
                                                  v (CRSF TTL)
                                          +-------+--------+
                                          | ELRS TX module |
                                          | (CRSF UART)    |
                                          +----------------+
                                                  |
                                                  v RF out via pole antenna
```

If the tracker is not installed, the pole-end MAX490 connects directly to the ELRS module's CRSF UART. The case-side stack is identical whether or not the tracker is present.

**Case-end MAX490 wiring:**

| MAX490 pin | Connect to | Notes |
|---|---|---|
| DI (driver in) | RP2040 GP0 | TX from RP2040, no series resistor here (RS-422 driver handles contention) |
| RO (receiver out) | RP2040 GP1 | RX to RP2040 |
| DE (driver enable), RE# (receiver enable) | tied permanently active OR driven by RP2040 GPIO | Half-duplex bus; tie active for full-duplex on the differential pair, or drive from a spare GPIO for half-duplex |
| A, B | Cat6 pair 1 (Orange / White-Or) | Differential pair to pole-end MAX490 |
| VCC | 5V (from Pi via internal harness) | |
| GND | shared signal GND | |

**TODO:** decide on DE/RE# wiring. Full-duplex on the differential pair is simpler (tie both enables active); half-duplex is closer to native CRSF behavior but requires a GPIO and direction-flipping firmware. Default to full-duplex unless bench testing reveals contention.

#### 4.7.3 Pole-end project box (extended only)

External to the case, mounted on the pole. Houses the ELRS module, pole-end MAX490, local power conditioning, and (when present) the antenna tracker and its servos. Receives 12V from the case via the multi-conductor cable. The tracker is optional within this configuration; an RS-422 cable run without a tracker is a valid use of the extended layout when the only requirement is cable length.

| Component | Role | Wiring notes |
|---|---|---|
| Pole-end MAX490 | RS-422 to TTL | Mirror of the case-end MAX490. A/B from cable to MAX490; DI/RO to next stage (tracker or ELRS module). |
| ESP32-S3 (tracker, optional) | Byte-pump + tracker logic | Sits between pole-end MAX490 and ELRS module on the CRSF path |
| ELRS TX module | RF link | UART connection to tracker (if present) or directly to pole-end MAX490 |
| 6V buck | Servo rail | Feeds pan and tilt servos |
| 5V buck | Logic rail | Feeds the ESP32-S3 and the pole-end MAX490 |
| 2-DOF PTZ gimbal | Pan/tilt mount | Ø82mm pan bearing carries the load; servo specs TBD per BOM 2.13 |
| Pole antenna | RF emitter | External to the project box, ELRS TX module's antenna port |

ESP32-S3 pin map (tracker firmware):

| Function | GPIO |
|---|---|
| UART1 RX (cable / MAX490 RO) | GP17 |
| UART1 TX (cable / MAX490 DI) | GP18 |
| UART2 RX (from ELRS module CRSF TX) | GP4 |
| UART2 TX (to ELRS module CRSF RX) | GP5 |
| Pan PWM (LEDC ch 0) | GP6 |
| Tilt PWM (LEDC ch 1) | GP7 |
| I2C SDA (reserved, future magnetometer) | GP8 |
| I2C SCL (reserved, future magnetometer) | GP9 |

The tracker is the only component on the pole that needs USB-CDC access, and only for calibration. In normal operation the cable is the only connection between case and pole.

**TODO:** hardware bypass jumper for field recovery if the tracker firmware fails. Routes the cable's RS-422 pair around the tracker, directly to the ELRS module. Planned for the project box mechanical layout.

### 4.8 Front-panel wiring

The front panel of the case body holds the status row, the control row, the keyboard well, and the speaker grille. All status surfaces are Mega-driven; all control surfaces are split between Mega (most switches and encoders) and RP2040 (the two arm-related inputs).

#### 4.8.1 Status row (Mega-driven)

- **VFD `vfd.0`** (Noritake CU20025ECPB-W1J 20x2): Mega HD44780 4-bit interface plus 5V power. Through the 5V 8-channel level shifter on the data and control lines. Pin assignment in Appendix A.
- **GLCD `glcd`** (ST7920 128x64): Mega hardware SPI (pins 51 MOSI / 52 SCK / 53 SS) plus 5V power, PSB tied to GND for serial mode. See 4.4 for the pin table.
- **Voltmeter** (self-contained 7-segment): wired directly across the 12V rail. No software, no Mega connection. Two wires to the rail, mounted in a panel cutout.

#### 4.8.2 Control row (Mega + RP2040)

- **Switches and encoders** (Mega-driven via the button matrix and `enc.0..n` subsystems): each switch terminal goes to a Mega GPIO with internal pull-up; the other terminal to shared GND. Per-pin HAL flags configure active-HIGH or active-LOW polarity. Specific pin assignments in Appendix A.
- **6-position rotary selector**: 1P6T mechanical; common pin to a Mega analog input, six output pins to a resistor divider, or six discrete digital inputs. Implementation choice depends on Mega pin budget; canonical wiring in Appendix A.
- **Arm key (SF-equivalent)**: aviator-style toggle, ON-OFF, on the upper face of the panel guarded by the safety cover. NC contact closes on UP. Wired to **RP2040 GP14** + shared GND (NOT Mega). This is the safety-significant input; RP2040 owns it directly so the daemon's arm machine reads it via the same path as channel intents.
- **Arm-confirm momentary (SH-equivalent)**: large distinctive-colored push-button. Wired to **RP2040 GP15** + shared GND. Press-only, release ignored. Co-located with the arm key on the panel so both fall under one hand.
- **Keylock master switch**: keyed power switch on the input rail downstream of the fuse. Two terminals: 12V in, 12V out. Not connected to any MCU.
- **E-stop**: mushroom-head, latching, NC contacts. The NC contacts sit in series between the 12V bus and the ELRS module DC feed (refer to 4.2). Not connected to any MCU; pure mechanical break of the module's power.

#### 4.8.3 Front-panel USB-A

One or two panel-mount USB-A jacks on the front face, wired internally to the powered USB hub (NOT to the Pi directly). The joystick lives in the primary front-panel USB-A; the secondary jack is ad-hoc (charging, debug USB-serial, etc.).

#### 4.8.4 Heartbeat LED (optional, Pi GPIO breakout)

Optional. A discrete LED on a Pi GPIO pin lights when `zerotxd` is alive (kicked by a heartbeat goroutine). Wiring: Pi GPIO pin to LED anode through a ~330 Ω resistor; LED cathode to GND. Specific GPIO pin in Appendix A. Adds confidence at a glance that the Pi side is running; never on the safety path (the LED can be dark and the system still operating, e.g., if the heartbeat goroutine has crashed but the daemon is otherwise functional).

### 4.9 Lid wiring (hinge bundle)

The lid is passive: it holds the two 7" displays and nothing else. No MCU, no power conditioning, no status surfaces in the lid.

Four cables cross the hinge:

| Cable | Function | Notes |
|---|---|---|
| HDMI display 1 | Video to HUD LCD | Thin or flat HDMI preferred for hinge flex |
| HDMI display 2 | Video to Map LCD | Same |
| USB-C display 1 | Power (5V) + touch | One cable carries both; 4 conductors minimum |
| USB-C display 2 | Power (5V) + touch | Same |

Bundle the four cables with spiral wrap, anchor at both halves with cable clamps. ~30 cm of slack inside the bundle accommodates ~110° of hinge rotation without strain or kinking. The strain-relief anchors take the mechanical load; the cables themselves should never see tension.

Display power on the 5V rail comes from buck #1 (the Pi-side buck). USB-C cables exit the body interior wired to a 5V terminal block; touch lines run from the same USB-C cables to the powered USB hub. Inside the lid, the cables terminate at each display's USB-C input.

### 4.10 External case I/O (bulkhead inventory)

Case-side connectors. **TODO:** confirm final list and locations after physical assembly.

| Connector | Type | Position | Purpose |
|---|---|---|---|
| Power input | DC barrel jack | Rear | 12VDC from operator-supplied source (bench supply, SLA+charger pack, vehicle 12V) |
| Pole connector | Multi-pin (default: 4-conductor, extended: 8-conductor) | Rear | Default: CRSF + signal GND + 12V + power GND; extended: RS-422 pair + 12V + GND + spare. Feeds the ELRS module directly (default) or the pole-end project box (extended). |
| Front-panel USB | USB-A x 1-2 | Front | Joystick (primary); ad-hoc devices, charging, dev access (secondary) |
| Speaker grille | Cutout | Front | Acoustic opening for the case speaker |
| Vent slots | Cutouts + mesh | Rear (lower) | Convective cooling for the body interior |
| Hinge bundle exit | Strain-relief gland | Top, body-side | Cable bundle to the lid |

No RF, antennas, or SMA bulkheads anywhere on the case. All RF lives on the pole.

### 4.11 Wiring verification (pre-power-on checks)

Before applying 12V for the first time, run through these checks with a multimeter and the case open. They catch the failure modes that turn first power-on into an electrical incident.

**Continuity checks (case unpowered, both fuse and keylock removed):**

1. No continuity between 12V bus and GND anywhere. If there is, find the short before applying power.
2. Continuity from barrel jack center pin, through the empty fuse holder, through the keylock switch in its OFF position, to the 12V bus. The keylock should break the path in OFF.
3. Continuity from 12V bus to buck #1 input, buck #2 input, audio amp 12V input, voltmeter, and the e-stop's NC contact terminal that faces the 12V bus.
4. Continuity from the other side of the e-stop's NC contacts, through to the ELRS module DC feed at the pole connector. Press the e-stop and verify continuity breaks.
5. No continuity from 12V bus to chassis GND on the case. Power GND and chassis GND are isolated except at the single-point bond at the barrel jack's mounting hardware.

**Buck output check (case unpowered, multimeter on buck output terminals):**

6. Confirm both bucks' output terminals show open circuit between V+ and V- (no shorted output caps).

**RP2040 USB direction check:**

7. RP2040 USB-C connects to Pi USB port B (USB 3.0), NOT through the hub. Trace the cable. If routed through the hub, move it.

**E-stop wiring direction check:**

8. With e-stop released (normal): NC contact closed = continuity from 12V bus to ELRS DC feed.
9. With e-stop pressed (latched): NC contact open = ELRS module DC isolated.

**Panel switch initial states:**

10. Keylock OFF, e-stop released, arm key DOWN (disarmed), all toggles in their visually-OFF positions, all encoders unrotated. The case should start in a known idle posture before first power-on.

**First power-on procedure** (only after the checks above pass):

11. Fuse out, keylock OFF. Apply 12V at the jack. Multimeter on 12V bus: expect 12V. If anything else (no voltage, low voltage, fluctuating voltage), remove power and investigate.
12. Install fuse. Voltage still on 12V bus, nothing on buck outputs (keylock still OFF). Confirm.
13. Turn keylock ON. 12V bus stays at 12V. Buck outputs come up to 5V each. The 7-segment voltmeter lights up showing ~12.0V.
14. Verify the Pi 400 powers on (boot SSD activity light blinks, displays show Pi boot output once HDMI cables and displays are connected; this is also the start of Section 7 first-boot verification).
15. With Pi up, the MCU satellites enumerate (visible in `dmesg | tail` over SSH or directly on the Pi). VFD shows boot banner; HUB75 panel runs self-test or idle pattern; the voltmeter still shows 12V; audio amp may emit a brief turn-on pop.
16. Press the e-stop. ELRS module DC drops; CRSF emission ceases at the wire (verifiable later when the module is connected). Pi side continues running.
17. Release/twist the e-stop. ELRS module DC restores.
18. Turn keylock OFF. Everything in the case loses power except the voltmeter (still on 12V upstream of the keylock). Wait, then remove the 12V at the jack.

If any step diverges from expected, stop and investigate before continuing.

The full first-boot verification (daemon up, kiosks loading, MCU subsystems responding, audio path tested) lives in Section 7. Steps 14-17 above are the *electrical* portion; everything past "Pi boots" is logical and belongs in Section 7.

## 5. MCU firmware flashing

### RP2040 (CRSF generator)
#### Build
#### Flash via BOOTSEL
#### Verify (serial banner, expected behavior under no daemon)
### Mega 2560 (IO board)
#### Build (PlatformIO)
#### Flash
#### HAL EEPROM configuration via zerotx-iohal-config
#### Verify
### ESP32 (HUB75 panel driver)
#### Build
#### Flash
#### Verify (panel self-test pattern, mode cycling)
### ESP32-S3 (antenna tracker, optional)
#### Build
#### Flash
#### Bench self-test on bare board
#### NVS configuration
#### Verify

## 6. Pi 400 provisioning

### Pi OS Lite image to USB SSD
### Boot order EEPROM tweak
### First boot: user, hostname, locale
### Networking: Ethernet/Wi-Fi, masking blocking targets
### Base packages
### Go toolchain
### Piper TTS install and voice fetch (en + pt)
### Audio: ALSA, USB DAC selection
### Hardware overlays
#### I2C and DS3231 RTC
#### GPS UART
#### Heartbeat LED (optional)
### udev rules for MCUs (RP2040, Mega, ESP32, ESP32-S3)
### Auto-login on tty1
### .xinitrc kiosk launcher (HUD + Map, dual display)
### Daemon binary deploy
### zerotxd systemd user unit
### Pi 400 optimizations
#### GPU memory split
#### Disable unused services
#### CPU governor
#### Browser flags
#### tmpfs for noisy directories
### SSD backup procedure

## 7. First-boot verification

### Pre-power checklist
### Power-on sequence
### udev symlinks present (/dev/zerotx-rp2040, -mega, -esp32, -tracker)
### zerotxd active, HTTP responds
### Both Chromium kiosks load /status
### Mega reports HAL EEPROM v2 valid
### VFD shows boot banner
### HUB75 panel runs self-test
### Joystick enumerates and binds
### Audio: Piper synthesizes test phrase
### RTC reads correct time
### GPS UART produces NMEA
### Telemetry frames via SITL bench mode
### Pass/fail acceptance summary

## 8. Bench test before field deployment

### SITL end-to-end test (daemon to INAV SITL via X-Plane or stub)
### Failsafe chain bench test (cut Pi heartbeat, observe CRSF stop, observe RX timeout)
### Hardware kill test (e-stop trips module DC)
### Antenna tracker self-test (if installed)
### Recording and replay round-trip
### Acceptance: bench-ready means field-ready

## 9. Troubleshooting (build-time)

### Firmware won't flash
### MCU enumerates but daemon doesn't see it (udev)
### Daemon won't start
### Kiosks blank or wrong display
### Audio silent
### VFD blank
### HUB75 panel dark or scrambled
### CRSF link to FC fails on bench
### Tracker doesn't track

## Appendices

### A. Full pinout reference (Pi 400 header, RP2040, Mega 2560, ESP32, ESP32-S3)
### B. USB device IDs and udev rule templates
### C. zerotxd.service unit reference
### D. Daemon config files (paths, schemas, examples)
### E. Wiring diagrams and case-layout reference
### F. Schematic and source file inventory (what's in the repo, where)
### G. Glossary
### H. Changelog
