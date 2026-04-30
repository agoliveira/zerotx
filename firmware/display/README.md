# Display firmware (HUB75)

ESP32 firmware driving two chained P2.5 64x32 HUB75 panels for the
ZeroTX ground station instrument cluster.

## Protocol

See [`docs/protocols/display.md`](../../docs/protocols/display.md) for
the wire protocol the daemon and this firmware speak.

## Hardware

- **MCU**: ESP32 classic (any variant with enough GPIO)
- **Panels**: 2x P2.5 64x32 HUB75, chained
- **Power**: separate 5V rail, sized for ~6A peak
- **Connection to daemon**: USB-CDC serial at 115200 8N1

## Default pinout

Standard ESP32-HUB75-MatrixPanel-DMA library pinout. If your wiring
differs, edit `setup()` in `display.ino` and uncomment the
`mxconfig.gpio.<pin> = ...` overrides.

| HUB75 pin | ESP32 GPIO |
| --------- | ---------- |
| R1        | 25         |
| G1        | 26         |
| B1        | 27         |
| R2        | 14         |
| G2        | 12         |
| B2        | 13         |
| A         | 23         |
| B         | 19         |
| C         | 5          |
| D         | 17         |
| E         | 22         |
| LAT       | 4          |
| OE        | 15         |
| CLK       | 16         |

## Build

PlatformIO is the easiest path:

```sh
cd firmware/display
pio run
pio run -t upload
pio device monitor -b 115200
```

Arduino IDE works too: install the ESP32 board package, install the
`ESP32-HUB75-MatrixPanel-DMA` library, open `display.ino`, and flash.

## Testing without the daemon

The `disptest` CLI in `pi/daemon/cmd/disptest/` connects to the ESP32
over USB serial and lets you fire individual protocol messages by
hand. Use it for firmware iteration before wiring this into the live
daemon.

```sh
cd pi/daemon
go build -o /tmp/disptest ./cmd/disptest
/tmp/disptest -port /dev/ttyACM1
```

Then at the prompt:

```
> mode flight
> state bat=11.7 batpct=73 alt=124 dist=430
> alarm critical "BATTERY EMPTY"
> clear-alarm
> mode idle
```

You should see the panels react in real time. Inbound messages from
the firmware (READY, HEARTBEAT, PONG, ERROR) print with a `<--`
prefix.

## Modes

The firmware implements all six protocol modes with conservative
rendering. Iterate as needed:

- **IDLE**: dim "ZEROTX" centered
- **PREFLIGHT**: status text + GPS info if known
- **FLIGHT**: 3-tile cluster (BAT / ALT / DIST)
- **ALARM**: full-width colored banner with alarm text
- **RTH**: distance to home + arrow (placeholder; no compass yet)
- **POSTFLIGHT**: flight time + peak altitude

The first round is intentionally minimal. Once it works on hardware,
we iterate on layout, colors, animations, and additional content.

## Power notes

The panels can pull serious current at full brightness:

- 1 panel at full white: ~3-4A
- 2 panels chained at full white: ~6-8A peak

Recommendations:

- Use a 5V supply rated for at least 6A continuous (8-10A is safer)
- Wire the panel power directly from the supply, not through the
  ESP32 USB rail
- Common ground between ESP32 and panel power is mandatory
- The firmware defaults to 80% brightness which keeps current under
  control for testing; increase with the `BRIGHTNESS` protocol
  message once you've verified your supply

## Future

- Faster refresh / double-buffering once a layout is settled
- Custom fonts (the default 5x7 is readable but plain)
- Smoothed transitions on alarm fire (fade or pulse)
- Compass arrow for RTH mode (need bearing-to-home from the daemon)
- Ambient idle animation (subtle, for "the system is alive")
