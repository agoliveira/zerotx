# zerotx-io firmware (Mega 2560)

General IO board firmware. Replaces the Pro Micro VFD firmware with a
multi-subsystem design: Noritake VFD(s), trackball status LEDs,
indicator LEDs, panel buttons, WS2813 strip, plus future LDR and
buzzer all share one Mega 2560 connected to the daemon over USB-CDC.

## Status

- Core framework: line-oriented protocol parser, subsystem base class,
  central dispatcher, watchdog, capability discovery (`GET caps`),
  version reporting (`GET version`), boot-cause logging.
- HAL with EEPROM-persisted pin map and runtime reconfiguration via
  protocol commands. No reflash needed to re-pin a buried Mega.
- VFD subsystem: Noritake CU20025ECPB-W1J 2x20 in 4-bit HD44780 mode.
  Six modes ported from the Pro Micro firmware.
- Trackball LEDs subsystem: five canonical states with on-firmware
  animation.
- Button subsystem: 5 panel buttons, polling-based with 20ms debounce,
  edge events pushed to the daemon.
- LED subsystem: 4 generic indicator LEDs (pure on/off).
- WS2813 strip: 16 individually-addressable pixels, daemon-driven
  animations.
- Pending: LDR sensor, buzzer.

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
> version zerotx-io 0.4.0-iox

GET caps
> caps hal led.trackball vfd.0 button.0 button.1 button.2 button.3 button.4 led.0 led.1 led.2 led.3 ws.0

# Buttons (events flow up unsolicited):
EVENT button.2 down
EVENT button.2 up

GET button.0
> button.0 released

# Indicator LEDs:
SET led.0 on
SET led.1 off
GET led.0
> led.0 on

# WS2813 strip:
SET ws.0 brightness 64
SET ws.0 pixel 0 ff0000           # red
SET ws.0 pixel 1 00ff00           # green
SET ws.0 all 0000ff               # all blue
SET ws.0 clear
GET ws.0
> ws.0 count=16 brightness=64

EVENT boot power-on
EVENT hal loaded-eeprom
```

## Subsystem command summary

### `vfd.0` - 2x20 character VFD
```
SET vfd.0 mode <banner|idle|ambient|armed>
SET vfd.0 brightness <0..3>           # 0 = brightest
SET vfd.0 line <row> <text...>        # multi-word, ~2s hold
SET vfd.0 clear
SET vfd.0 tick [<n>]
SET vfd.0 arm <0|1>
SET vfd.0 fmmode <text>
SET vfd.0 lq <0..100>
SET vfd.0 batt <text>
SET vfd.0 alarm <warn|critical|failsafe>
SET vfd.0 disarmed
GET vfd.0
```

### `led.trackball` - bicolor trackball ring
```
SET led.trackball <off|green-solid|green-pulse|red-solid|red-blink>
GET led.trackball
```

### `led.<0..3>` - generic on/off indicator LEDs
```
SET led.<n> <on|off|0|1>
GET led.<n>
```

### `button.<0..4>` - panel buttons
```
GET button.<n>            # > button.<n> <pressed|released>
EVENT button.<n> down     # emitted on press edge
EVENT button.<n> up       # emitted on release edge
```

### `ws.0` - WS2813 strip (16 pixels)
```
SET ws.0 pixel <index> <rrggbb>
SET ws.0 all <rrggbb>
SET ws.0 brightness <0..255>
SET ws.0 clear
GET ws.0
```

## HAL: runtime pin configuration

The Mega's pin assignments live in EEPROM. To re-pin without reflash:

```
SET hal pin button_0 22                # stages in EEPROM
SET hal reboot                         # apply
```

Other HAL commands:

```
GET hal map                # full pin map with current values + source
GET hal source             # "eeprom" or "defaults"
SET hal pin <name> <num>   # stage one pin override
SET hal reset-defaults     # wipe EEPROM; reboot to apply
SET hal reboot             # soft reset via watchdog
```

If a bad pin map ever bricks a subsystem, `SET hal reset-defaults`
followed by `SET hal reboot` recovers because USB Serial0 is
hardcoded and can't be remapped.

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

That is the entire change.

## Build and flash

```
pio run
pio run -t upload
pio device monitor
```

## Watchdog

500ms hardware watchdog kicked every loop iteration. Boot reason
reported as the first event after reset (`EVENT boot power-on` or
`EVENT boot watchdog-reset`).

`SET hal reboot` triggers a watchdog reset deliberately to apply
staged HAL changes. The daemon should expect USB to drop briefly
and re-enumerate, then re-handshake.

## Pro Micro VFD firmware retirement

`firmware/vfd/src/main.cpp` is the legacy Pro Micro firmware. Once
the Mega-side IO board is validated against real hardware, the Pro
Micro firmware retires.
