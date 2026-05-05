# zerotx-io firmware (Mega 2560)

General IO board firmware. Replaces the Pro Micro VFD firmware with a
multi-subsystem design: Noritake VFD(s), trackball status LEDs,
WS2813 indicators, LDR ambient-light sensor, buttons, and buzzer all
share one Mega 2560 connected to the daemon over USB-CDC.

## Status

- Core framework: line-oriented protocol parser, subsystem base class,
  central dispatcher, watchdog, capability discovery (`GET caps`),
  version reporting (`GET version`), boot-cause logging.
- Working subsystem: `led.trackball` (active-low to ground via NPN,
  five canonical states with on-firmware animation rendering).
- Pending: VFD (port from existing Pro Micro firmware), WS2813 strip,
  LDR sensor push, button events, buzzer.

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
> version zerotx-io 0.1.0

GET caps
> caps led.trackball

SET led.trackball red-blink
GET led.trackball
> led.trackball red-blink

EVENT boot power-on
EVENT led.trackball ready
```

## Adding a subsystem

1. Create `src/subsystems/<name>.h` and `.cpp` with a class deriving
   from `zerotx::Subsystem`. Implement at minimum `name()` and
   `handle()`. Override `count()` for multi-instance, `begin()` for
   setup-time init, `tick()` for periodic work or animation.

2. Add `#include "subsystems/<name>.h"` and a static instance to
   `main.cpp`. Append the instance pointer to `kSubsystems[]`.

3. If new pins are involved, add them to `pinmap.h`.

That is the entire change. The dispatcher walks the registry, the
parser is generic, and `GET caps` automatically reflects the new
subsystem.

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

## Notes on porting from the Pro Micro VFD firmware

The existing `firmware/vfd/src/main.cpp` (596 lines) has the Noritake
display init sequence, the render loop at 30fps, and the animation
state machine for idle / armed / ambient modes. The port lives in
`subsystems/vfd.cpp` (not yet present). The pin map needs updating
for the Mega's pin layout, but the rendering logic is straightforward
since it uses standard Arduino primitives (digitalWrite, delayMicros,
etc.) that work identically on AVR-2560.

Once the VFD subsystem is up and tested against real hardware on the
Mega, the Pro Micro firmware retires.
