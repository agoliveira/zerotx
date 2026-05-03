# ZeroTX Wiring Reference

Single source of truth for connections inside the ZeroTX case. Every
signal that crosses a module boundary lives in a table here.

The case is wired-only inside: ELRS and any other RF modules are
external (mounted on poles, connected by cable). The case has no
internal antennas.

## Topology

```
                     +-------- ESP32 (HUB75 panel + spectator AP)
                     |
                     |  USB-CDC
                     |
   Pi 400 (USB hub) ---- RP2040 (CRSF, panel buttons, arm key)
                     |
                     +-------- Pro Micro (VFD diagnostic display)
                     |
                     +-------- HOTAS-X joystick
                     |
                     +-------- Trackball (USB)
                     |
                     +-------- Keyboard (Pi 400 internal)

   External pole (cabled to case bulkhead):
                     +-------- ELRS TX module (USB or UART)

   Hardware controls (RP2040 GPIO):
                     - Arm key (panel)
                     - Mushroom button (panel; also hardware-cuts ELRS power)

   Power:
                     - AC PSU -> 13.8V CCTV rail (lights, VFD)
                     - 12V/AC dual-rail input
                     - 5V from CCTV PSU rail (VFD VDD, panel logic)
                     - Pi 400 USB-C from its own rail
```

## RP2040 (zerotx-fw)

Custom panel MCU, manages CRSF uplink + panel buttons + arm key.
Source: `rp2040/src/`.

| Pin | Function | Notes |
|---|---|---|
| GP0 | UART0 TX | CRSF to ELRS module (420000 baud) |
| GP1 | UART0 RX | CRSF from ELRS module |
| GP14 | Arm key input | Active-low; pulled up internally |
| GP15 | Confirm button input | Active-low |
| GP16 | Mushroom button input | Active-low; ALSO wired to power-cut relay (independent of MCU) |
| USB-CDC | IPC to daemon | Custom framed protocol, see `protocols/ipc.md` |

Power: USB from Pi (5V via USB connector).

## ESP32 (display firmware)

HUB75 LED panel driver + spectator WiFi AP.
Source: `firmware/display/src/`.

HUB75 panel pinout (default library mapping):

| ESP32 GPIO | HUB75 signal | Notes |
|---|---|---|
| 25 | R1 | Top half red |
| 26 | G1 | Top half green |
| 27 | B1 | Top half blue |
| 14 | R2 | Bottom half red |
| 12 | G2 | Bottom half green |
| 13 | B2 | Bottom half blue |
| 23 | A | Row select bit 0 |
| 19 | B | Row select bit 1 |
| 5  | C | Row select bit 2 |
| 17 | D | Row select bit 3 |
| 32 | E | Row select bit 4 (1/32 scan) |
| 4  | LAT | Latch |
| 15 | OE | Output enable |
| 16 | CLK | Pixel clock |

Panel: 2x chained 64x32 HUB75, total 128x32.

USB-CDC to Pi: text-line protocol, see `protocols/display-serial.md`.

WiFi AP for spectators:
- SSID: `ZeroTX-Spectator`
- Password: `pédogalo` (WPA2)
- Channel: 1 (fixed)
- Max clients: 4
- Dashboard: `http://192.168.4.1/`

## Pro Micro VFD (sparkfun_promicro16, 5V/16MHz)

Drives Noritake CU20025ECPB-W1J 2x20 character VFD in 4-bit HD44780 mode.
Source: `firmware/vfd/src/`.

| VFD pin | Function | Connect to | Notes |
|---|---|---|---|
| 1 | VSS | GND | red wire on supplied flat cable |
| 2 | VDD | +5V | from CCTV PSU 5V rail |
| 3 | VO | GND | contrast unused on VFD; tie to GND so input doesn't float |
| 4 | RS | Pro Micro D4 | |
| 5 | R/W | GND | write-only mode |
| 6 | E | Pro Micro D5 | |
| 7-10 | D0-D3 | NC | 4-bit mode |
| 11 | D4 | Pro Micro D6 | |
| 12 | D5 | Pro Micro D7 | |
| 13 | D6 | Pro Micro D8 | |
| 14 | D7 | Pro Micro D9 | |

Pro Micro power: USB from Pi (independent USB endpoint, 5V).

USB-CDC to Pi: ASCII line protocol, see `protocols/vfd-serial.md`.

Caveat: SparkFun Pro Micro 5V breaks out D0-D10, D14-D16, D18-D21
on the headers; D11/D12/D13 exist on the 32u4 die but are not
accessible. Use D4-D9 (six contiguous header pins on the left edge).

## Power tree

```
AC mains -> Case AC PSU -+-> 13.8V rail -> CCTV-style fixtures + status lights
                          |
                          +-> 5V rail ----+-> VFD VDD (~130 mA typ.)
                                           +-> Pro Micro VCC (~50 mA)

12V/AC dual-rail input  -> redundant feed; same rails as above

Pi 400 -> USB-C from its own rail -+-> Pi internal 5V
                                    +-> USB hub
                                          +-> RP2040 (USB power)
                                          +-> ESP32 (USB power)
                                          +-> Pro Micro (USB power; if not
                                              powered from CCTV 5V rail)
                                          +-> HOTAS-X
                                          +-> Trackball
                                          +-> ELRS module (USB or UART
                                              with separate supply)
```

GND is common across all modules.

## External cable bulkheads

Aviation-style connectors on the case rear panel for the pole-mounted
modules. Pinout per cable depends on the specific connector chosen
during the case build; document here once finalized.

## Hardware emergency cut

The mushroom button is wired in two places:
1. RP2040 GP16 (so the daemon sees the press and can react)
2. In line with the ELRS module's power feed via a relay or simple
   normally-closed contact, so even if the MCU has hung, pressing
   the mushroom physically removes power from the ELRS module.
   Aircraft enters its configured failsafe (RTH/land) on link loss.

This is the only fully-independent emergency path; everything else
(GUI disarm, software channel intent) depends on the daemon being
alive and the link being up.

## Permissions / groups

For the daemon to access hardware as a regular user:

```
sudo usermod -aG dialout,input,audio $USER
```

- `dialout`: `/dev/ttyACM*` (RP2040, ESP32, Pro Micro)
- `input`: `/dev/input/js*` (HOTAS-X)
- `audio`: ALSA / PulseAudio playback
