# zerotx-io firmware (Mega 2560)

General IO board firmware. Replaces the Pro Micro VFD firmware with a
multi-subsystem design: Noritake VFD(s), trackball status LEDs,
WS2813 indicators, LDR ambient-light sensor, buttons, and buzzer all
share one Mega 2560 connected to the daemon over USB-CDC.

## Status

- Core framework: line-oriented protocol parser, subsystem base class,
  central dispatcher, watchdog, capability discovery (`GET caps`),
  version reporting (`GET version`), boot-cause logging.
- HAL with EEPROM-persisted pin map and runtime reconfiguration via
  protocol commands. No reflash needed to re-pin a buried Mega.
- VFD subsystem: Noritake CU20025ECPB-W1J 2x20 in 4-bit HD44780 mode.
  Six modes (BANNER, IDLE, AMBIENT, ARMED, TEXT, EVENT) ported from
  the Pro Micro firmware; same animation engine, same custom CGRAM
  glyph set, same activity-bar rendering.
- Trackball LEDs subsystem: five canonical states with on-firmware
  animation rendering.
- Pending: WS2813 strip, LDR push events, button events, buzzer.

## Protocol

Line-oriented text over Serial0 (USB-CDC), 115200 baud. Three verbs:

```
SET <subsystem>[.<instance>] <param> [args...]
GET <subsystem>[.<instance>] [<param>]
EVENT <subsystem>[.<instance>] [args...]    # firmware -> daemon only
```

Responses to `GET` look like `> body...`. Errors look like
`! subsystem message`. Lines starting with `#` are comments.

Examples:

```
GET version
> version zerotx-io 0.3.0-vfd

GET caps
> caps hal led.trackball vfd.0

SET led.trackball red-blink
SET vfd.0 mode armed
SET vfd.0 fmmode ANGLE
SET vfd.0 lq 95
SET vfd.0 tick 5
SET vfd.0 line 0 ZeroTX ready

GET hal map
> hal map source=eeprom count=8
> hal pin 0 led_trackball_green 8
> hal pin 1 led_trackball_red 9
> hal pin 2 vfd0_rs 30
> hal pin 3 vfd0_en 31
> hal pin 4 vfd0_d4 32
> hal pin 5 vfd0_d5 33
> hal pin 6 vfd0_d6 34
> hal pin 7 vfd0_d7 35

EVENT boot power-on
EVENT hal loaded-eeprom
EVENT vfd.0 ready
EVENT led.trackball ready
```

## VFD command summary

```
SET vfd.0 mode <banner|idle|ambient|armed>
SET vfd.0 brightness <0..3>           # 0 = brightest
SET vfd.0 line <row> <text...>        # multi-word text overlay (~2s hold)
SET vfd.0 clear
SET vfd.0 tick [<n>]                  # activity ping; default n=1
SET vfd.0 arm <0|1>                   # edge: triggers sweep transition
SET vfd.0 fmmode <text>               # cache flight-mode label, brief overlay
SET vfd.0 lq <0..100>                 # cache link quality
SET vfd.0 batt <text>                 # cache battery voltage string
SET vfd.0 alarm <warn|critical|failsafe>
SET vfd.0 disarmed                    # disarm without sweep
GET vfd.0                             # current display mode + cached state
```

The protocol grammar differs from the legacy Pro Micro VFD wire
format (`L<row><sp><text>`, `C`, `B<sp>level`, `E <kind>...`).
Daemon-side code that talked to the Pro Micro needs to be updated;
the firmware does not maintain the legacy commands.

## HAL: runtime pin configuration

The Mega's pin assignments live in EEPROM. At boot, `hal::begin()`
reads EEPROM. If the magic/version/CRC is valid AND the pin count
matches the compiled `HAL_PIN_COUNT`, those values are used.
Otherwise the firmware falls back to compiled defaults (in
`hal.cpp`, `kHalPinDefaults`) and writes them back to EEPROM so
the next boot is clean.

To re-pin a deployed Mega without reflash:

```
SET hal pin vfd0_rs 22                # stages in EEPROM
SET hal pin vfd0_en 23
SET hal reboot                        # apply
```

Other HAL commands:

```
GET hal map                # full pin map with current values + source
GET hal source             # "eeprom" or "defaults"
SET hal reset-defaults     # wipe EEPROM; reboot to take effect
SET hal reboot             # soft reset via watchdog
```

If a bad pin map ever bricks a subsystem, `SET hal reset-defaults`
followed by `SET hal reboot` recovers because the protocol channel
itself (USB Serial0) is hardcoded and can't be remapped.

## Adding a subsystem

1. Create `src/subsystems/<name>.h` and `.cpp` with a class deriving
   from `zerotx::Subsystem`. Implement at minimum `name()` and
   `handle()`. Override `count()` for multi-instance, `begin()` for
   setup-time init, `tick()` for periodic work or animation.

2. Add `#include "subsystems/<name>.h"` and a static instance to
   `main.cpp`. Append the instance pointer to `kSubsystems[]`.

3. If new pins are involved: add an entry to `HalPinId` in `hal.h`,
   add the default + name in `hal.cpp` (`kHalPinDefaults` and
   `kHalPinNames`). Subsystem reads pin numbers via `hal::pin(id)`.

That is the entire change. The dispatcher walks the registry, the
parser is generic, `GET caps` automatically reflects the new
subsystem, and the new pins flow through the existing HAL plumbing.

## Build and flash

PlatformIO. From this directory:

```
pio run
pio run -t upload
pio device monitor
```

`pio device monitor` shows protocol traffic on the serial line; you
can also issue commands by typing them.

## Watchdog

500ms hardware watchdog. Every loop iteration kicks it. Boot reason
is reported as the first event after reset (`EVENT boot power-on` or
`EVENT boot watchdog-reset`).

`SET hal reboot` triggers a watchdog reset deliberately to apply
staged HAL changes. The daemon should expect USB to drop briefly
and re-enumerate, then re-handshake.

## Pro Micro VFD firmware retirement

`firmware/vfd/src/main.cpp` is the legacy Pro Micro firmware. Once
the Mega-side VFD has been validated against real hardware, the Pro
Micro firmware retires. The Pro Micro hardware retires from the case
design alongside it.
