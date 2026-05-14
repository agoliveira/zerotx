# zerotx-bench

Hardware diagnostic and testing tool for the ZeroTX ground station.
Bench-only — never deployed in the field.

## What it does

A web UI listing every device the Pi 400 can talk to, plus interactive
test actions per device. Probes cover:

- **MCUs over USB-CDC**: Mega 2560, RP2040, ESP32 panel driver
- **Breakout-board peripherals**: DS3231 RTC (I2C), u-blox GPS (UART),
  heartbeat LED (GPIO 17)
- **USB peripherals**: joystick, audio interface
- **HDMI**: kiosk display presence
- **ELRS**: telemetry response via the RP2040

Each probe reports presence + diagnostic details and may offer
interactive test actions (blink an LED, beep the buzzer, write a test
message to a VFD, capture GPS NMEA, etc.).

## Coexistence

The tool refuses to start if zerotxd is detected running on the same
machine. The MCU probes need exclusive USB-CDC access — running both
at once would corrupt the channel buffer and could cause unsafe
arm-state transitions if an aircraft is connected.

Stop the daemon first:

```
sudo systemctl stop zerotxd
```

## Build and run

```
cd tools/zerotx-bench
go build -o /tmp/zerotx-bench
sudo systemctl stop zerotxd
/tmp/zerotx-bench
```

The web UI binds `0.0.0.0:8081` by default. Browse from a laptop on
the same network as the Pi, or use `-bind 127.0.0.1:8081` to bind
localhost-only (e.g. for an untrusted network).

## Status

This README describes the full intended scope. The tool ships
incrementally; current commit (D2) adds the binary-COBS MCU probes
(RP2040 CRSF generator, ELRS-via-RP2040). Nine probes registered.
Subsequent commits:

- E: HDMI displays + baseline export

The Mega, ESP32, and RP2040 probes need exclusive USB-CDC access
— the coexistence check earns its keep here. Running these probes
while zerotxd has the ports open would corrupt both sides of the
channel buffer.

The ELRS probe uses the RP2040 link to observe forwarded telemetry
frames. CAUTION: while the ELRS probe runs, the RP2040 emits live
CRSF at 50Hz with safe defaults (sticks centered, throttle low,
arm low). Bench-only by design, but if an aircraft is bound to
this ELRS link the frames reach the FC. Never run with an aircraft
powered on.

## Required system packages

Probes shell out to standard Pi/Linux tools rather than carrying
their own implementations. Install via apt:

```
sudo apt install i2c-tools gpiod alsa-utils
```

`i2c-tools` provides `i2cdetect` (RTC presence check) and `hwclock`
typically comes from the base `util-linux` package, so it's always
present. `gpiod` provides `gpioinfo` and `gpioset` for the LED probe.
`alsa-utils` provides `aplay` and `speaker-test` for the audio probe.

The GPS and joystick probes use kernel device files directly (no
extra packages).
