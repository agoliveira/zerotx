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

### What ZeroTX is (one paragraph + hero render)
### Topology block diagram
### Subsystem responsibilities
#### Pi 400 (daemon, displays, audio)
#### RP2040 (CRSF generator)
#### Mega 2560 (IO board, VFD, status row, control row)
#### ESP32 (HUB75 panel)
#### ESP32-S3 (antenna tracker, optional pole-end)
### End-to-end signal path (joystick to aircraft)
### Failsafe chain (preview; full treatment in User Manual)
### Power tree (preview; full treatment in Section 4)

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
