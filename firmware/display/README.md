# Display firmware (HUB75 on RP2040)

Firmware driving two chained P2.5 64x32 HUB75 panels for the ZeroTX
ground station instrument cluster. As of v0.19.0 this firmware runs
on a Waveshare RP2040-Zero (or any RP2040 board); the previous
ESP32-based version is preserved in git history if you ever need to
reference it.

## Why RP2040

The earlier ESP32 firmware also hosted the "Spectator" SoftAP for
field-side onlookers. That feature was decided to not belong on the
display device, and once removed there was no remaining reason to
keep the ESP32: WiFi was the only capability the panel work itself
didn't need. Migrating to RP2040 consolidates the project on a single
small-MCU platform (the joystick CPPM/CRSF generator is also RP2040)
and trades I2S DMA for PIO + DMA, which is a clean fit for HUB75
parallel-bit-banged refresh.

## Protocol

See [`docs/protocols/display.md`](../../docs/protocols/display.md).
The wire protocol is unchanged from the ESP32 version; the daemon
talks to the new firmware exactly as it did before.

## Project layout

```
firmware/display/
├── platformio.ini       PlatformIO build config (RP2040 + arduino-pico)
├── src/
│   └── main.cpp         Firmware source
└── README.md            This file
```

## Hardware

- **MCU**: Waveshare RP2040-Zero (or any RP2040 board).
- **Panels**: 2x Waveshare P2.5 64x32 HUB75, chained → 128x32 logical.
- **Driver library**: Adafruit Protomatter (PIO + DMA on RP2040).
- **Power**: panels are 5V, ~6A peak combined. Use a separate 5V rail;
  the RP2040 is powered separately via USB or its own regulator.

## Pin map (default)

```
RGB:  R1=GP2  G1=GP4  B1=GP3        # Note G/B swap for Waveshare panels
      R2=GP5  G2=GP7  B2=GP6
ADDR: A=GP8  B=GP15  C=GP26  D=GP27   # 1/16 scan = 4 lines, no E
CTRL: CLK=GP28  LAT=GP29  OE=GP14
```

This pin map deliberately avoids GP9-GP13, which sit on the edge of
the RP2040-Zero opposite the USB-C connector and are the hardest
edge to physically wire. Address/clock/latch lines live on
GP14-GP15 and GP26-GP29 instead.

The Waveshare P2.5 panels swap GREEN and BLUE channels at the wire
level; the firmware handles this by reordering the entries in
`rgbPins[]` (G and B positions exchanged) rather than by remapping
GPIOs. This means the RGB565 colors in the source mean what they
say.

If your wiring differs, edit `rgbPins[]`, `addrPins[]`, `clockPin`,
`latchPin`, `oePin` near the top of `main.cpp` and rebuild.

## Brightness

Adafruit Protomatter has no `setBrightness` method - bit depth is
fixed at construction. To preserve the original protocol's runtime
brightness control, the firmware applies software dimming: every
color computation goes through `dim565(r, g, b)`, which scales the
RGB components by `state.brightness / 100` before encoding to RGB565.

`DISP BRIGHTNESS <0..100>` from the daemon updates `state.brightness`
and the next render picks it up automatically. Costs a few cycles
per pixel and is well within budget at 30+Hz refresh.

## Bit depth

Currently `MATRIX_BIT_DEPTH = 4` (4 bits per channel = 4096 colors).
Higher depths cost proportionally more refresh time. 4 is the sweet
spot for the vintage-VFD aesthetic without flicker on 128x32. Bump
to 5 or 6 if the colors look banded; expect lower max refresh rate.

## Build and flash

```
cd firmware/display
pio run
pio run -t upload
pio device monitor
```

First flash typically requires putting the board into BOOTSEL mode
(hold BOOTSEL while plugging USB on Pico, or press the board's
button on RP2040-Zero). Subsequent flashes use the arduino-pico
auto-reset.

## Spectator note

The Spectator SoftAP feature was removed during the RP2040 port. The
RP2040 has no WiFi. If you want to revive that feature in the future,
do it on the Pi 400 (which already has WiFi and is closer to the
data) or on a dedicated ESP32 with no panel-driving duties.
