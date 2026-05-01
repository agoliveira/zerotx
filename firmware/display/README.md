# Display firmware (HUB75)

ESP32 firmware driving two chained P2.5 64x32 HUB75 panels for the
ZeroTX ground station instrument cluster.

## Protocol

See [`docs/protocols/display.md`](../../docs/protocols/display.md) for
the wire protocol the daemon and this firmware speak.

## Project layout

```
firmware/display/
├── platformio.ini       PlatformIO build config
├── src/
│   └── main.cpp         Firmware source (plain C++, not Arduino .ino)
├── include/             Reserved for future headers
├── .gitignore           Build artifacts and IDE state
└── README.md            This file
```

Standard PlatformIO project layout. VSCode + the PlatformIO extension
gives you full IntelliSense, navigation, and debugger integration.
The firmware is plain C++ that uses the Arduino framework's APIs;
nothing about it is `.ino`-specific.

## Hardware

- **MCU**: ESP32 classic (DevKit V1 or any 30-pin variant)
- **Panels**: 2x P2.5 64x32 HUB75, chained
- **Power**: separate 5V rail, sized for ~6A peak
- **Connection to daemon**: USB-CDC serial at 115200 8N1

## Default pinout

Standard ESP32-HUB75-MatrixPanel-DMA library pinout. If your wiring
differs, edit `setup()` in `src/main.cpp` and uncomment the
`mxconfig.gpio.<pin> = ...` overrides.

| HUB75 pin | ESP32 GPIO | DevKit V1 silkscreen |
| --------- | ---------- | -------------------- |
| R1        | 25         | D25                  |
| G1        | 26         | D26                  |
| B1        | 27         | D27                  |
| R2        | 14         | D14                  |
| G2        | 12         | D12                  |
| B2        | 13         | D13                  |
| A         | 23         | D23                  |
| B         | 19         | D19                  |
| C         | 5          | D5                   |
| D         | 17         | D17 / TX2            |
| E         | 22         | D22                  |
| LAT       | 4          | D4                   |
| OE        | 15         | D15                  |
| CLK       | 16         | D16 / RX2            |

**GPIO 12 caveat**: this pin has a boot strap function. If the ESP32
fails to enter download mode while the HUB75 cable is connected,
unplug the HUB75 ribbon, flash, then reconnect.

## Build

PlatformIO is the supported path:

```sh
cd firmware/display
pio run
pio run -t upload
pio device monitor -b 115200
```

VSCode users: install the **PlatformIO IDE** extension. Open the
`firmware/display/` folder as the project root. PlatformIO will
auto-detect the project, install dependencies, and generate
`c_cpp_properties.json` for IntelliSense.

## Power-on self-test

The firmware runs a visible self-test on boot before settling into
idle mode. Sequence (~5 seconds total):

| Stage | Duration | What you see                         |
| ----- | -------- | ------------------------------------ |
| 1     | 0.7s     | Solid red full screen                |
| 2     | 0.7s     | Solid green full screen              |
| 3     | 0.7s     | Solid blue full screen               |
| 4     | 0.9s     | RGBW vertical bars across 128px      |
| 5     | 0.9s     | Black with white seam, "A"/"B" labels|
| 6     | 1.2s     | "ZEROTX 0.1.0" big banner            |
| idle  | -        | Dim "ZEROTX" centered                |

If any stage looks wrong, you immediately know what's broken:

| Symptom                              | Likely cause                       |
| ------------------------------------ | ---------------------------------- |
| Nothing at all, no serial            | Boot strap pin issue (GPIO 12)     |
| Solid red OK, green/blue wrong       | G1/B1 or G2/B2 wire mismatch       |
| One panel dark                       | Chain ribbon or panel B power      |
| Right panel shows left content       | Chain plugged into wrong port      |
| Bars in wrong color order            | R/B pin swap                       |
| Text upside down or mirrored         | A/B/C/D address pin mismatch       |
| Half-height stripes                  | `PANELS_NUM` or scan rate wrong    |

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
iterate on layout, colors, animations, and additional content.

## Power notes

The panels can pull serious current at full brightness:

- 1 panel at full white: ~3-4A
- 2 panels chained at full white: ~6-8A peak

Recommendations:

- Use a 5V supply rated for at least 6A continuous (8-10A safer)
- Wire panel power directly from the supply, not through the ESP32
- Common ground between ESP32 and panel power is mandatory
- Firmware defaults to 80% brightness which keeps current under
  control for testing; increase via the `BRIGHTNESS` protocol
  message once you've verified your supply

## Future

- Faster refresh / double-buffering once a layout is settled
- Custom fonts (default 5x7 is readable but plain)
- Smoothed transitions on alarm fire (fade or pulse)
- Compass arrow for RTH mode (need bearing-to-home from daemon)
- Ambient idle animation
