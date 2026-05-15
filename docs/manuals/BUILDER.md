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

### Reading the wiring diagrams
### Power distribution
#### 12VDC input (external PSU + SLA UPS, operator-supplied)
#### Inline fuse on 12V rail
#### E-stop in module DC feed
#### Two 12V to 5V bucks (Pi 400, USB hub)
#### Voltmeter tap on 12V rail
#### ELRS module direct off 12V rail
### USB topology
#### Pi 400 root ports allocation
#### Powered hub layout
#### MCU enumerations (RP2040, Mega, ESP32, ESP32-S3)
#### Joystick on front-panel USB-A
#### USB DAC
### Signal paths
#### Pi to RP2040 (USB, framed IPC)
#### Pi to Mega (USB)
#### Pi to ESP32 panel (USB)
#### RP2040 to ELRS module (TTL via 5m manga blindado, 470Ω series on TX)
#### Pole-end tracker inline on the CRSF wire (optional, extended cable config)
### Panel wiring
#### Status row (VFD, voltmeter)
#### Control row (switches, encoders, 6POS, big button, keylock, e-stop)
#### Heartbeat LED
### Lid (HDMI + USB-C bundle through hinge)
### Wiring verification (continuity and voltage checks before first power-up)

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
