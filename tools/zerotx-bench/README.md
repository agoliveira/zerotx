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
incrementally; current commit (A) is the framework only — web server,
probe registry, HTTP API, UI shell. No probes registered. Run it
and you'll see "No probes registered". Subsequent commits add probes
in this order:

- B: RTC, GPS, heartbeat LED (breakout peripherals)
- C: joystick, audio (USB peripherals)
- D: Mega, RP2040, ESP32, ELRS (MCU probes — the ones that need
  daemon-stopped)
- E: HDMI displays + baseline export
