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

```
                              AIRCRAFT
                                 ^
                                 |  RF (ELRS)
                                 |
                          +------+------+
                          |  ELRS TX    |   externally pole-mounted
                          +------+------+
                                 |
                                 |  single-wire CRSF (default config)
                                 |  + 12V + GND, 5m shielded cable
                                 |
+ZeroTX case ====================|===========================================+
|                                |                                           |
|                                v                                           |
|                          +-----+----+                                      |
|                          | RP2040   |  CRSF endpoint                       |
|                          +-----+----+                                      |
|                                ^                                           |
|                                | USB-CDC                                   |
|                                v                                           |
|       +----------------------- + -------------------------------+          |
|       |                  zerotxd  (Go daemon)                   |          |
|       |          on Raspberry Pi 400, Pi OS Lite                |          |
|       |    + two Chromium kiosks (HUD, Map) on dual HDMI        |          |
|       |    + ALSA audio out (samples + Piper TTS)               |          |
|       +---+----------+-----------+------------+-----------------+          |
|           |          |           |            |                            |
|           | USB-CDC  | USB-CDC   | USB HID    | HDMI x2                    |
|           v          v           |            v                            |
|       +---+----+ +---+----+      |       +----+-----+                      |
|       | Mega   | | ESP32  |      |       | HUD LCD  |                      |
|       | 2560   | | panel  |      |       | Map LCD  |   in case lid        |
|       | IO hub | | driver |      |       +----------+                      |
|       +--+--+--+ +---+----+      |                                         |
|          |  |        |           |                                         |
|     +----+  |        v           v                                         |
|     |       |    +---+-------+  +-----------------+                        |
|     v       v    | HUB75     |  | USB joystick    |                        |
|   VFD,    panel  | 128x32    |  | (Thrustmaster)  |                        |
|   GLCD    buttons| LED panel |  | front-panel USB |                        |
|   on      (6/10) +-----------+  +-----------------+                        |
|   front          (front panel)                                             |
|   panel                                                                    |
|                                                                            |
+============================================================================+

Optional, not pictured: extended cable configuration replaces the single-wire
CRSF run with RS-422 (MAX490 pair on each end), which enables an inline
ESP32-S3 antenna tracker between the cable's pole end and the ELRS module.
The tracker byte-pumps frames transparently and is invisible to the daemon.
See Section 4 for cable choices and Section 5 for tracker firmware.
```

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

### Case and mechanical
### Pi 400 and peripherals
### Microcontrollers
### Displays and panel
### Status row (VFD, voltmeter)
### Control row (switches, encoders, 6POS, big button, keylock, e-stop)
### Audio (USB DAC, amp, speaker)
### Power (external 12VDC supply, external SLA UPS, internal bucks, fuses, terminal blocks)
### Cabling, connectors, panel-mount jacks
### Antenna tracker pole-end (optional)
### Fasteners and 3D-printed parts
### Sourcing notes and substitution guidance

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
