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

This section provisions the Pi 400 from a brick to a running ZeroTX host: Pi OS Lite to USB SSD, base packages, Go toolchain (optional), Piper TTS, audio, hardware overlays (RTC, GPS, heartbeat), udev rules, auto-login on tty1, kiosks via `~/.xinitrc`, daemon binary, and a systemd unit. Designed for the smallest practical install: no desktop, no greeter, no extras the daemon doesn't need. Pi OS Bookworm Lite (64-bit) is the assumed distribution; other versions are not tested.

Throughout this section, `<user>` is the username chosen during imaging. Pick one and stick with it; the daemon systemd unit, the home-directory paths, and the auto-login override all assume one consistent user account. The hostname in this manual is `zerotx`; choose your own if you prefer.

### 6.1 OS image to USB SSD

ZeroTX boots from a USB SSD, not an SD card. The SSD is faster, more reliable, and supports the larger working set the daemon and kiosks need.

On the provisioning machine:

1. Install `rpi-imager`.
2. Connect the SSD via USB.
3. Launch `rpi-imager`.
4. Choose:
   - **Device:** Raspberry Pi 400
   - **OS:** Raspberry Pi OS **Lite** (64-bit), Bookworm or current stable. **Not** the desktop image.
   - **Storage:** the USB SSD (be careful to pick the right device).
5. Open advanced options (gear icon). Set:
   - **Hostname:** `zerotx` (or your choice)
   - **SSH:** enabled, public key auth (paste your key)
   - **Username:** `<user>` (your choice; will be referenced in this manual as `<user>`)
   - **Password:** any (you'll mostly use SSH key auth)
   - **Wi-Fi credentials:** optional. Set if you want network for setup. Field operation does not require Wi-Fi.
   - **Locale, keyboard layout, timezone:** set appropriately
6. Write the image. Eject when done. Plug the SSD into one of the Pi 400's USB 3.0 ports.

### 6.2 Boot order EEPROM tweak

The Pi 400 default `BOOT_ORDER=0xf41` (SD, then USB, then loop) already falls through to USB when no SD card is inserted; ZeroTX boots from the SSD without any EEPROM change. To skip the SD probe and shave a couple of seconds off cold boot:

```
sudo rpi-eeprom-config --edit
```

Set:

```
BOOT_ORDER=0xf14
```

Read right-to-left: 4 = USB, 1 = SD, f = restart loop. Save and reboot.

Verify:

```
vcgencmd bootloader_config | grep BOOT_ORDER
```

### 6.3 First boot

After first boot, SSH in (or use the keyboard/HDMI directly):

```
sudo apt update
sudo apt -y full-upgrade
sudo timedatectl set-timezone America/Sao_Paulo   # or your zone
sudo hostnamectl set-hostname zerotx
sudo reboot
```

Confirm the SSD is the root filesystem (not the SD card path that wouldn't exist anyway since there's no SD):

```
findmnt /
```

Should report a USB-attached `nvme0n1pX` or `sdaX`, not `mmcblk0pX`.

Confirm swap is sane:

```
df -h /
free -h
```

If swap is missing or undersized:

```
sudo dphys-swapfile swapoff
sudo sed -i 's/^CONF_SWAPSIZE=.*/CONF_SWAPSIZE=2048/' /etc/dphys-swapfile
sudo dphys-swapfile setup
sudo dphys-swapfile swapon
```

### 6.4 Networking: non-blocking

Field use means no Wi-Fi available. Without changes, `multi-user.target` waits up to 90 seconds for `*-wait-online.service` to give up. Disable both services so cold-boot to operational kiosks isn't blocked on a network that's never coming up:

```
sudo systemctl disable --now NetworkManager-wait-online.service
sudo systemctl mask NetworkManager-wait-online.service
sudo systemctl disable --now systemd-networkd-wait-online.service
sudo systemctl mask systemd-networkd-wait-online.service
```

The daemon's systemd unit (Section 6.15) uses `Wants=network.target` rather than `network-online.target`, so it does not block on network either. Online features (tile fetches, weather, NTP) degrade gracefully when offline.

### 6.5 Disable unused services

Pi OS Lite ships fewer services than the desktop image, but a few common ones still load:

```
for svc in bluetooth.service hciuart.service \
           triggerhappy.service \
           ModemManager.service \
           avahi-daemon.service avahi-daemon.socket \
           cups.service cups-browsed.service ; do
    sudo systemctl disable --now "$svc" 2>/dev/null || true
done
```

The `2>/dev/null || true` swallows "service not found" for the ones that aren't installed on Lite. The script is idempotent across image variants.

Skip the `bluetooth.service` and `hciuart.service` entries if you actually use Bluetooth on this Pi.

### 6.6 Base packages

Kiosk and audio packages:

```
sudo apt -y install \
    xserver-xorg-core xserver-xorg-input-libinput \
    xserver-xorg-video-fbdev xserver-xorg-video-vc4 \
    xinit x11-xserver-utils \
    unclutter \
    chromium-browser \
    alsa-utils \
    curl ca-certificates
```

What that pulls in:

- `xserver-xorg-core`, `xinit`: the X server and `startx`
- `xserver-xorg-input-libinput`: input driver (needed even if no keyboard at runtime)
- `xserver-xorg-video-fbdev`, `xserver-xorg-video-vc4`: video drivers; the right one is auto-selected at start
- `x11-xserver-utils`: provides `xset` and `xrandr`
- `unclutter`: hides the cursor when idle
- `chromium-browser`: the only kiosk renderer
- `alsa-utils`: `aplay`, `amixer` for the audio path
- `curl`, `ca-certificates`: health-check probe in `.xinitrc`, plus any HTTPS the daemon does

Notably absent: no Wayfire, no labwc, no LXDE, no greetd, no plymouth, no display manager. X starts from the user's shell on tty1 and Chromium does its own window management.

Build-time and utility packages (only needed if you intend to build the daemon or firmware on the Pi itself; otherwise skip):

```
sudo apt -y install \
    build-essential git pkg-config \
    libusb-1.0-0-dev libudev-dev \
    wget jq xdotool
```

PlatformIO for occasional firmware reflash from the Pi (optional):

```
pip install --user --break-system-packages platformio
```

### 6.7 Go toolchain

Required only if you build the daemon on the Pi. Cross-compiling from a workstation (Section 0.4) is the recommended path and skips this section.

Use upstream Go, not the apt version. The apt package is typically a major release behind, and on some Debian/Ubuntu derivatives `apt install golang-go` silently installs `gccgo` which doesn't parse modern `go.mod` files.

```
GO_VERSION=1.25.10
cd /tmp
curl -L -O https://go.dev/dl/go${GO_VERSION}.linux-arm64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go${GO_VERSION}.linux-arm64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
sudo chmod +x /etc/profile.d/go.sh
source /etc/profile.d/go.sh
go version
```

Pinned to 1.25.10 here as known-good. The daemon's `go.mod` has both a `go 1.25.0` floor (minimum) and a `toolchain go1.25.10` directive (auto-fetch target if your installed Go is older). Installing any Go >= 1.21 above will work; installing 1.25.10 up front saves the auto-fetch round trip.

### 6.8 Piper TTS install and voice fetch

Piper is third-party and lives under `~/zerotx/third_party/piper/`, alongside the ONNX voice models in `~/zerotx/third_party/voices/`.

```
mkdir -p ~/zerotx/third_party/piper
cd ~/zerotx/third_party/piper
```

Fetch the Piper release for arm64. Filename and version vary by release; check `https://github.com/rhasspy/piper/releases` and adjust:

```
PIPER_VERSION=2023.11.14-2
PIPER_TARBALL=piper_linux_aarch64.tar.gz
curl -L -O https://github.com/rhasspy/piper/releases/download/${PIPER_VERSION}/${PIPER_TARBALL}
tar xzf ${PIPER_TARBALL}
rm ${PIPER_TARBALL}
```

**TODO**: confirm Piper version and tarball name on next refresh of this manual.

Voice models: `scripts/fetch-voices.sh` in the repo is the supported way to install them. It puts the `.onnx` + `.onnx.json` files under `~/zerotx/third_party/voices/`. For an ad-hoc smoke test you can also fetch one model manually:

```
mkdir -p ~/zerotx/third_party/voices
cd ~/zerotx/third_party/voices
curl -L -O https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/amy/medium/en_US-amy-medium.onnx
curl -L -O https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/amy/medium/en_US-amy-medium.onnx.json
```

For Portuguese narration, the en + pt voice pair is the canonical pair; the daemon's narrator selects between them based on configured language. Adjust paths in `scripts/fetch-voices.sh` to install both.

Smoke test once the audio path (Section 6.9) is up:

```
echo "ZeroTX online" | ~/zerotx/third_party/piper/piper \
  --model ~/zerotx/third_party/voices/en_US-amy-medium.onnx \
  --output_file /tmp/test.wav
aplay /tmp/test.wav
```

### 6.9 Audio

The case audio path: Pi → USB DAC (plugged into the powered USB hub) → analog line out → audio amplifier (on the 12V rail) → speaker. ALSA is the kernel-side interface; PulseAudio is not required for the daemon's audio pipeline, but is installable if you prefer.

List available cards:

```
aplay -l
```

The USB DAC should show as a `card N` entry where N is typically 1 or 2 (the onboard audio is `card 0` unless disabled, and 0 indexing depends on enumeration order). Pick the DAC's card index and set ALSA default in `~/.asoundrc`:

```
pcm.!default { type hw card 1 device 0 }
ctl.!default { type hw card 1 }
```

(Adjust `card N` to the index aplay reports for your DAC.)

System volume (assuming card 1):

```
amixer -c 1 set PCM 80%
sudo alsactl store
```

`alsactl store` persists the volume across reboots.

Onboard Pi audio can be disabled if you want only the USB DAC to enumerate. Edit `/boot/firmware/config.txt`:

```
dtparam=audio=off
```

(Or leave `dtparam=audio=on` and just point `.asoundrc` at the DAC; both approaches work, the explicit-disable removes ambiguity.)

Verify audio path end-to-end:

```
speaker-test -t sine -f 440 -l 1
```

Should produce a 1-second 440 Hz tone through the DAC → amp → speaker.

**TODO:** confirm the DAC card index after final assembly and update `~/.asoundrc`.

### 6.10 Hardware overlays

Edits to `/boot/firmware/config.txt` enable Pi-side peripherals (RTC, GPS UART, heartbeat LED, GPU memory split). After editing, reboot. Some apply on next boot regardless; the RTC and GPS specifically require reboot.

#### 6.10.1 I2C and DS3231 RTC

The Pi 400 has no battery-backed RTC of its own; without network the system clock resets to a default value at every boot. A DS3231 module on the I2C bus gives ZeroTX accurate timestamps in flight recordings without network access at the field.

Enable I2C:

```
sudo raspi-config nonint do_i2c 0
sudo apt -y install i2c-tools
```

Reboot or `sudo modprobe i2c-dev`. Verify:

```
ls /dev/i2c-1
i2cdetect -y 1
```

`i2cdetect` should show the bus with no devices yet (DS3231 not wired or not powered).

Wire the DS3231 module: VCC to header pin 1 (3V3), GND to header pin 6, SDA to header pin 3 (GPIO 2), SCL to header pin 5 (GPIO 3). Some modules ship with an EEPROM at 0x57; that's harmless and unused.

After wiring, confirm detection:

```
i2cdetect -y 1
```

Address `0x68` should show. Add the kernel overlay so the RTC is exposed as a hardware clock device:

```
sudo sed -i '/^dtparam=i2c_arm=on/a dtoverlay=i2c-rtc,ds3231' /boot/firmware/config.txt
```

Or hand-edit `/boot/firmware/config.txt` and add `dtoverlay=i2c-rtc,ds3231` near the existing `dtparam` lines.

Disable the userspace fake-hwclock that would otherwise compete:

```
sudo apt-get -y remove fake-hwclock
sudo update-rc.d -f fake-hwclock remove
sudo systemctl disable fake-hwclock
```

Edit `/lib/udev/hwclock-set` and comment out the three lines that return early when `systemd` is in use:

```
#if [ -e /run/systemd/system ] ; then
#    exit 0
#fi
```

The kernel's `hctosys` already handles the RTC-to-system sync at boot; the udev rule is harmless to leave intact on most setups but comment-out is the conservative move documented in the kernel RTC howto.

Reboot. Verify the RTC is recognized:

```
sudo dmesg | grep -i rtc
sudo hwclock -r
```

`dmesg` should show `rtc-ds1307 ... registered as rtc0`. `hwclock -r` should print the current time. If the RTC battery is fresh and the chip has never been written, the time will be wrong; set it from the network-synced kernel clock once (do this while online):

```
sudo hwclock -w
```

After this, the kernel reads the RTC at boot before chrony or any network is available, so flight recordings get accurate timestamps even with no network at the field.

#### 6.10.2 GPS UART (optional)

ZeroTX supports an optional Pi-attached serial GPS module (u-blox M6, M7, M10, or any NMEA TTL device) on UART3 (header pins 7/29). The daemon parses NMEA in-process and exposes a state snapshot to other subsystems. Failure to open the device is non-fatal: the daemon logs and continues, and consumers fall back to other position sources.

Wire the module: GPS VCC to header pin 1 (3V3) or pin 4 (5V, depending on the module's input range), GPS GND to header pin 6 or 9, GPS TX to header pin 29 (GPIO 5, UART3 RX), GPS RX to header pin 7 (GPIO 4, UART3 TX). Most modules are 3V3-compatible on both rails; check the datasheet before connecting 5V power.

Enable UART3 in the Pi's device tree:

```
echo 'dtoverlay=uart3' | sudo tee -a /boot/firmware/config.txt
```

Reboot. After boot the device appears as `/dev/ttyAMA1`. (The Pi's primary mini-UART, `/dev/ttyAMA0`, stays where it is and is normally used by Bluetooth or the serial console.)

Verify raw NMEA flows:

```
ls /dev/ttyAMA*
sudo cat /dev/ttyAMA1
```

If you see garbage rather than readable text, the baud is probably wrong. Common GPS baud rates: 9600 (default for u-blox M6/M7/M10), 38400, 115200. Set explicitly:

```
stty -F /dev/ttyAMA1 9600 raw -echo
sudo cat /dev/ttyAMA1
```

Lines beginning with `$GP...` or `$GN...` arriving at 1 Hz (default) or faster mean the GPS is talking.

Daemon flags (added to the systemd unit in Section 6.15):

```
-gps-port /dev/ttyAMA1
-gps-baud 9600
```

Default `-gps-port` is empty (disabled). When set, the daemon opens the port at startup and runs an internal NMEA parser. The reader silently absorbs malformed sentences and rate-limits parse-error logs (one per minute) so a flaky cable doesn't flood the journal.

#### 6.10.3 Heartbeat LED (optional)

A small LED on a Pi GPIO pin gives at-a-glance feedback that `zerotxd` is alive. Wire a low-current LED + ~1k resistor from header pin 11 (GPIO 17) to any ground pin (e.g., pin 9). Active-high: pin 11 high turns the LED on.

The daemon enables the heartbeat with `-heartbeat-gpio 17`. Default is `-1` (disabled), so the daemon runs identically without a breakout.

Verify with the GPIO line tool while the daemon is stopped:

```
sudo apt -y install gpiod
gpioget gpiochip0 17
gpioset gpiochip0 17=1   # LED on
gpioset gpiochip0 17=0   # LED off
```

When the daemon runs with `-heartbeat-gpio 17`, the LED blinks at 1 Hz while the 50 Hz mapper loop is healthy and goes dark on hang.

Optional but worth fitting: gives one-bit confidence that the Pi side is running, independent of whether the kiosks are visible. Add `gpu_mem=128` and any other config.txt tweaks in the same edit pass; see Section 6.16.

### 6.11 udev rules for MCUs

The daemon launches against `/dev/serial/by-id/` paths, which are vendor-stable on their own. udev SYMLINKs are optional but ergonomic, especially when scripts or systemd units need short paths.

Create `/etc/udev/rules.d/99-zerotx.rules`:

```
# RP2040 CRSF generator
SUBSYSTEM=="tty", ATTRS{idVendor}=="2e8a", ATTRS{idProduct}=="000a", \
  ATTRS{serial}=="<RP2040_SERIAL>", SYMLINK+="zerotx-rp2040"

# Mega 2560 IO board
SUBSYSTEM=="tty", ATTRS{idVendor}=="2341", ATTRS{idProduct}=="0042", \
  SYMLINK+="zerotx-mega"

# ESP32 panel driver
SUBSYSTEM=="tty", ATTRS{idVendor}=="<ESP32_VID>", ATTRS{idProduct}=="<ESP32_PID>", \
  SYMLINK+="zerotx-esp32"

# ESP32-S3 antenna tracker (optional; only needed if calibrating via USB on the Pi)
SUBSYSTEM=="tty", ATTRS{idVendor}=="<S3_VID>", ATTRS{idProduct}=="<S3_PID>", \
  SYMLINK+="zerotx-tracker"
```

Replace the `<...>` placeholders with the actual values from your hardware. Find them by plugging each MCU into the Pi (one at a time, to avoid ambiguity) and running:

```
udevadm info --query=property --name=/dev/ttyACM0 | grep -E 'ID_VENDOR_ID|ID_MODEL_ID|ID_SERIAL_SHORT'
```

The RP2040-Zero in particular has a unit-specific serial number; pin the exact one in the rule so a replacement board (different serial) doesn't silently take the symlink.

Reload udev:

```
sudo udevadm control --reload-rules
sudo udevadm trigger
```

After replug or reboot, verify:

```
ls -l /dev/zerotx-*
```

The symlinks should point at the appropriate `/dev/ttyACM*` or `/dev/ttyUSB*` device nodes.

### 6.12 Auto-login on tty1

The Pi needs to land in a shell session on tty1 with no greeter so `~/.bash_profile` (Section 6.13) can fire `startx` automatically.

Drop a getty override:

```
sudo systemctl edit getty@tty1
```

Editor opens. Add:

```
[Service]
ExecStart=
ExecStart=-/sbin/agetty --autologin <user> --noclear %I $TERM
```

Save and exit. The empty `ExecStart=` line is required: it clears the inherited value, then the second one replaces it. Substitute your username for `<user>`.

Reload:

```
sudo systemctl daemon-reload
sudo systemctl restart getty@tty1
```

Reboot to verify login lands on tty1 as `<user>` without prompting.

### 6.13 .xinitrc kiosk launcher

In `~/.bash_profile` (create if it doesn't exist), trigger `startx` on tty1:

```
[ -f ~/.bashrc ] && . ~/.bashrc

if [ -z "$DISPLAY" ] && [ "$(tty)" = "/dev/tty1" ]; then
    exec startx
fi
```

Only on tty1, only when no X session is already running. Other ttys (e.g., SSH) get a normal shell.

`startx` runs `~/.xinitrc`. This file is the entire X session: no window manager, just disable screensaver, position the displays, wait for the daemon, launch two Chromium kiosks. Create `~/.xinitrc`:

```
#!/bin/sh
# Disable screensaver, DPMS, and screen blanking. Without these the
# kiosks would dim themselves after a few minutes of no input.
xset s off
xset -dpms
xset s noblank

# Hide the mouse cursor after 1s idle. Even though there's no mouse
# at runtime, X draws a cursor at startup and it sits there.
unclutter -idle 1 -root &

# Position the two displays side by side. The output names depend on
# how the kernel labels the Pi 400's two micro-HDMI ports. Run
# `xrandr` once to see what your hardware reports (typical: HDMI-1
# and HDMI-2, or HDMI-A-1 and HDMI-A-2). Adjust the names below.
xrandr --output HDMI-1 --auto --pos 0x0 \
       --output HDMI-2 --auto --right-of HDMI-1

# Wait for the daemon's HTTP server. zerotxd.service starts in
# parallel with the X session, so the kiosks would otherwise fail
# to load on the first try and depend on Chromium's own retry. A
# couple of curl probes is faster and quieter.
until curl -s -o /dev/null http://127.0.0.1:8080/api/v1/health ; do
    sleep 0.5
done

# Common Chromium flags shared by both kiosks.
common_flags="--kiosk --noerrdialogs --disable-infobars \
    --disable-translate --no-first-run --no-default-browser-check \
    --disable-features=TranslateUI \
    --disable-component-extensions-with-background-pages \
    --disable-background-networking \
    --disable-renderer-backgrounding \
    --disable-extensions \
    --disk-cache-size=33554432"

# HUD on the left display. Each kiosk needs its own user-data-dir;
# Chromium locks the profile and refuses to launch a second instance
# against the same one. /tmp is tmpfs so the dirs vanish on reboot,
# which is what we want for a stateless kiosk.
#
# Both kiosks land on /status first. The operator clicks "Proceed
# to flight" once the system check is satisfied; the daemon flips
# the syscheck gate, both pages observe the transition over the
# WebSocket stream, and each navigates to the path encoded in its
# ?dest= query (?dest=hud -> /hud/, ?dest=map -> /map/). A daemon
# reboot resets the gate so the operator sees /status again on
# next boot.
chromium-browser $common_flags \
    --user-data-dir=/tmp/chromium-hud \
    --window-position=0,0 --window-size=1920,1080 \
    "http://127.0.0.1:8080/status/?dest=hud" &

# Map on the right display. Adjust the X offset to match the left
# display's width.
chromium-browser $common_flags \
    --user-data-dir=/tmp/chromium-map \
    --window-position=1920,0 --window-size=1920,1080 \
    "http://127.0.0.1:8080/status/?dest=map" &

# Keep the X session alive as long as either kiosk is running. If
# both exit, X exits and the user gets dropped back to the shell on
# tty1, at which point .bash_profile re-launches startx.
wait
```

Make it executable:

```
chmod +x ~/.xinitrc
```

**Display arrangement:** if your displays land in different positions or different resolutions than the script assumes, SSH in while X is running and check actual output names and modes:

```
DISPLAY=:0 xrandr
```

Edit `~/.xinitrc` to match (output names, `--auto` vs explicit `--mode 1920x1080`, `--pos` and `--window-position`/`--window-size` values).

**HUD/Map mapping:** if the HUD lands on the Map physical screen and vice versa, swap the two `--window-position` lines in `.xinitrc` rather than relabeling the cables. The kiosk-to-display assignment is purely a software config; cables stay as wired.

### 6.14 Daemon binary deploy

Cross-compile from a workstation (preferred; keeps the Go toolchain off the Pi):

```
# On the workstation, in the repo:
GOOS=linux GOARCH=arm64 go build -o /tmp/zerotxd ./pi/daemon/cmd/zerotxd
scp /tmp/zerotxd <user>@<pi-host>:/tmp/

# On the Pi:
sudo install -m 0755 /tmp/zerotxd /usr/local/bin/zerotxd
```

The daemon expects a working-directory layout under `~/zerotx/`:

```
~/zerotx/
  pi/daemon/web/          # web UI static assets (HUD + Map kiosks)
  third_party/piper/       # piper binary (from Section 6.8)
  third_party/voices/      # piper voice models
  recordings/              # flight recordings written here
  sounds/                  # pre-baked alarm samples
  configs/                 # EdgeTX model YAML files
  maptiles/                # PMTiles files for offline tile serving
```

If your repo lives elsewhere, adjust the paths in the systemd unit (Section 6.15) accordingly.

### 6.15 zerotxd systemd unit

Create `/etc/systemd/system/zerotxd.service`:

```
[Unit]
Description=ZeroTX daemon
Wants=network.target
After=network.target

[Service]
Type=simple
User=<user>
WorkingDirectory=/home/<user>
ExecStart=/usr/local/bin/zerotxd \
    -api 127.0.0.1:8080 \
    -port /dev/zerotx-rp2040 \
    -iohub-port /dev/zerotx-mega \
    -web-dir /home/<user>/zerotx/pi/daemon/web \
    -recordings-dir /home/<user>/zerotx/recordings \
    -sounds-dir /home/<user>/zerotx/sounds \
    -piper-binary /home/<user>/zerotx/third_party/piper/piper \
    -model /home/<user>/zerotx/configs/big_talon_zerotx.yml \
    -site-lat -22.91 -site-lon -47.06 \
    -gps-port /dev/ttyAMA1 \
    -heartbeat-gpio 17

Restart=on-failure
RestartSec=5

# Resource hygiene: the daemon doesn't need elevated privileges and
# shouldn't touch root-owned files. ProtectSystem= and friends guard
# against accidents in development; remove if they conflict with a
# specific feature added later.
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/home/<user>/zerotx/recordings /tmp /run /var/run
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

`Wants=network.target` (not `network-online.target`) is the non-blocking line. The daemon doesn't need internet to start; weather and tile fetches degrade gracefully when offline.

Substitute your username for `<user>` throughout. Adjust:
- `-site-lat` and `-site-lon` to your home field
- `-model` to your active EdgeTX model YAML (drop the flag if you don't use one yet)
- `-gps-port /dev/ttyAMA1` — drop this flag if no Pi-attached GPS is fitted
- `-heartbeat-gpio 17` — drop this flag if no heartbeat LED is fitted

Enable and start:

```
sudo systemctl daemon-reload
sudo systemctl enable --now zerotxd.service
```

Verify:

```
systemctl status zerotxd.service
journalctl -u zerotxd -f
```

Should be `active (running)`. Logs should show the daemon opening the RP2040 and Mega ports, starting its HTTP server on 8080, and beginning the mapper loop.

### 6.16 Pi 400 optimizations

Reduce CPU load and tighten boot. Two Chromium kiosks on a Pi 400 sit around 70-80% CPU; the optimizations below trim that, mostly by trimming GPU/CPU contention and reducing background work.

#### GPU memory split

`/boot/firmware/config.txt`:

```
gpu_mem=128
```

128 MB to the GPU helps both Chromium kiosks run smoothly. Combine with the RTC/UART/audio overlays from Section 6.10 in one edit pass.

#### CPU governor

For consistent performance under the kiosk load:

```
echo 'performance' | sudo tee /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor
```

This applies to one CPU; the kernel propagates the policy across all four cores. To persist across reboot, either install `cpufrequtils` (apt) and edit `/etc/default/cpufrequtils`, or drop a small unit:

```
sudo tee /etc/systemd/system/cpu-governor.service <<EOF
[Unit]
Description=Set CPU governor to performance
After=multi-user.target

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'echo performance | tee /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor'
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl enable cpu-governor.service
```

#### Browser flags

Already included in the `common_flags` variable in `~/.xinitrc` (Section 6.13). Listed here for reference:

```
--disable-features=TranslateUI
--disable-component-extensions-with-background-pages
--disable-background-networking
--disable-renderer-backgrounding
--disable-extensions
--disk-cache-size=33554432
```

#### tmpfs for noisy directories (optional)

Trades durability for write reduction. With a USB SSD, this is mostly belt-and-suspenders.

`/etc/fstab`:

```
tmpfs /tmp                tmpfs defaults,noatime,size=512M       0 0
tmpfs /var/log            tmpfs defaults,noatime,size=64M        0 0
```

Note: `journalctl` history is lost on reboot if `/var/log` is tmpfs. ZeroTX's own log buffer (the `/api/v1/logs` endpoint) doesn't depend on disk-persisted journals, so this is fine.

#### Boot speed measurement

Once everything is wired, measure:

```
systemd-analyze
systemd-analyze blame | head -20
systemd-analyze critical-chain
```

Typical Pi 400 + Pi OS Lite + this setup: 18-25 seconds from kernel start to `multi-user.target`, plus 3-5 seconds for X + Chromium to reach the kiosk pages. Total cold boot to operational kiosks: roughly 25-30 seconds.

If `systemd-analyze blame` flags a service taking >5 s and you don't need it, mask it. Common culprits on Lite:

- `apt-daily.service`, `apt-daily-upgrade.service` — disable if the Pi is rarely online
- `man-db.service` — slow first-run, mask
- `e2scrub_all.service` — mask
- `dpkg-db-backup.service` — mask

Mask with `sudo systemctl mask <name>`. Re-run `systemd-analyze blame` to confirm the bottleneck moved.

### 6.17 SSD backup

Once provisioning is complete and the system is verified working (the verification checklist lives in Section 7), image the SSD on another machine:

```
# On a workstation, with the SSD plugged in via USB:
sudo dd if=/dev/<ssd_device> of=zerotx-bootstrap-$(date +%Y%m%d).img bs=4M status=progress
gzip zerotx-bootstrap-$(date +%Y%m%d).img
```

Store the compressed image off-Pi (NAS, cloud backup, secondary SSD, whatever you trust).

This is the canonical baseline for cloning to a fresh SSD. Re-image after any major Pi-side change: kernel update, daemon dependency change, audio reconfig, addition of new udev rules, etc. The cost is ~30 minutes; the value is "back to a known good state in 15 minutes when something gets corrupted at the field."

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

### A. Full pinout reference

This appendix reproduces the canonical pinouts for the three MCUs (RP2040, Mega 2560, ESP32 panel driver) and the Pi 400 GPIO header. Section 4 (Wiring) tells you what to wire to what; this appendix is where you look up *which specific pin* during build, debug, or repair. The ESP32-S3 tracker pinout is already in Section 4.7.3 and is not duplicated here.

Pin numbers for the RP2040 and ESP32 are compile-time `#define` values in the firmware; reflashing is the only way to change them. **Mega 2560 pin numbers are runtime-configurable** via the HAL EEPROM v3 system; the table below is the *default* map, but the active map on a running Mega may differ and is the source of truth. Use `tools/zerotx-iohal-config/` to read or modify the active map.

The source-of-truth files cited under each section are paths into this repository as of the build that produced this manual; pin assignments are stable across versions but file paths and line numbers may shift.

#### RP2040 Zero (CRSF generator + arm key)

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
| GP0  | output | UART0 TX to ELRS module (CRSF) | Hardware UART. 470 Ω series resistor at the case end of the cable merges TX and RX for single-wire half-duplex CRSF |
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

##### Device wiring

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

**ELRS module** (CRSF, see Section 4.7 for the full case-to-pole cable)

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
  Section 4.7.2; GP0 and GP1 connect to a MAX490 transceiver
  instead. No firmware change required.

#### Arduino Mega 2560 (IO board)

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

##### Device wiring

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

#### ESP32 DevKit V1 (HUB75 LED panel driver)

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

##### Device wiring

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

#### Pi 400 GPIO breakout

The Pi 400 exposes the standard 40-pin Raspberry Pi GPIO header on the
back edge. ZeroTX uses a passive breakout board for access. The header
follows the Pi 4 pinout and uses BCM GPIO numbering in software (which
does not match the physical pin numbers on the header).

##### Pin allocation

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

##### Device wiring

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

##### Software notes

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

##### Free pins

ZeroTX does not currently use GPIO 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
16, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27. SPI0 is on GPIO 8/9/10/11;
UART0 is on GPIO 14/15; PCM/I2S is on GPIO 18/19/20/21. Reserve those
banks when planning future expansions (I2S DAC, additional UARTs, etc.)
rather than picking pins by free-from-function logic alone.

### B. USB device IDs and udev rule templates
### C. zerotxd.service unit reference
### D. Daemon config files (paths, schemas, examples)
### E. Wiring diagrams and case-layout reference
### F. Schematic and source file inventory (what's in the repo, where)
### G. Glossary
### H. Changelog
