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
- Working subsystem: `led.trackball` (active-low to ground via NPN,
  five canonical states with on-firmware animation rendering).
- Pending: VFD (port from existing Pro Micro firmware), WS2813 strip,
  LDR push events, button events, buzzer.

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
> version zerotx-io 0.2.0-hal

GET caps
> caps hal led.trackball

SET led.trackball red-blink
GET led.trackball
> led.trackball red-blink

GET hal map
> hal map source=eeprom count=2
> hal pin 0 led_trackball_green 8
> hal pin 1 led_trackball_red 9

EVENT boot power-on
EVENT hal loaded-eeprom
EVENT led.trackball ready
```

## HAL: runtime pin configuration

The Mega's pin assignments live in EEPROM. At boot, `hal::begin()`
reads EEPROM. If the magic/version/CRC is valid AND the pin count
matches the compiled `HAL_PIN_COUNT`, those values are used.
Otherwise the firmware falls back to compiled defaults (in
`hal.cpp`, `kHalPinDefaults`) and writes them back to EEPROM so
the next boot is clean.

To re-pin a deployed Mega without reflash:

```
SET hal pin led_trackball_green 22     # stages in EEPROM
SET hal pin led_trackball_red 23
SET hal reboot                         # apply
```

Other HAL commands:

```
GET hal map         # full pin map with current values + source
GET hal source      # "eeprom" or "defaults"
SET hal reset-defaults    # wipe EEPROM; reboot to take effect
SET hal reboot      # soft reset via watchdog
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

## Notes on porting from the Pro Micro VFD firmware

The existing `firmware/vfd/src/main.cpp` (596 lines) has the Noritake
display init sequence, the render loop at 30fps, and the animation
state machine for idle / armed / ambient modes. The port lives in
`subsystems/vfd.cpp` (not yet present). VFD pin assignments will be
added to the HAL so the parallel-bus pin block is reconfigurable
along with everything else.

Once the VFD subsystem is up and tested against real hardware on the
Mega, the Pro Micro firmware retires.
