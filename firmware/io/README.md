# zerotx-io firmware (Mega 2560)

General IO board firmware. Replaces the Pro Micro VFD firmware with a
multi-subsystem design: Noritake VFD(s), trackball status LEDs,
indicator LEDs, panel buttons, WS2813 strip, relays, plus future LDR
and buzzer all share one Mega 2560 connected to the daemon over
USB-CDC.

## Status

- Core framework: line-oriented protocol parser, subsystem base class,
  central dispatcher, watchdog, capability discovery (`GET caps`),
  version reporting (`GET version`), boot-cause logging.
- HAL with EEPROM-persisted pin map AND per-pin flags. Both are
  reconfigurable at runtime; no reflash needed to re-pin or change
  polarity on a buried Mega.
- VFD subsystem: Noritake CU20025ECPB-W1J 2x20 in 4-bit HD44780 mode.
- Trackball LEDs subsystem: five canonical states with on-firmware
  animation.
- Button subsystem: 5 panel buttons, polling-based with 20ms debounce.
- LED subsystem: 4 generic indicator LEDs (pure on/off).
- Relay subsystem: 4 relays (pure on/off, separate from led for
  protocol-level intent clarity).
- WS2813 strip: 16 individually-addressable pixels.
- Pending: LDR sensor, buzzer.

## Polarity convention

The firmware default for every output (LEDs, relays, trackball ring)
is **active-HIGH**: HIGH = active = energized. This is the universal
rule across the codebase. There is no built-in inversion logic.

Some hardware needs active-low (e.g., relay boards that wire their
input through an inverting transistor stage). For those cases the
HAL stores a per-pin ACTIVE_LOW flag that flips polarity at the
output level only:

```
SET hal flag relay_0 0x01    # relay_0 becomes active-low
SET hal reboot               # apply
```

Inverted logic is opt-in per pin, never assumed.

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
> version zerotx-io 0.5.0-relay

GET caps
> caps hal led.trackball vfd.0 button.0 button.1 button.2 button.3 button.4 led.0 led.1 led.2 led.3 relay.0 relay.1 relay.2 relay.3 ws.0

# Relays:
SET relay.0 on
SET relay.1 off
GET relay.2
> relay.2 off

# HAL map now shows pin AND flags:
GET hal map
> hal map source=eeprom count=22
> hal pin 0 led_trackball_green 8 0x00
> hal pin 1 led_trackball_red 9 0x00
> hal pin 2 vfd0_rs 30 0x00
...
> hal pin 18 relay_0 22 0x00
> hal pin 19 relay_1 23 0x00
...
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

### `relay.<0..3>` - relay outputs
```
SET relay.<n> <on|off|0|1>
GET relay.<n>
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

## HAL: runtime pin and flag configuration

The Mega's pin assignments AND per-pin flags live in EEPROM. To
re-pin without reflash:

```
SET hal pin button_0 22                # stages new pin in EEPROM
SET hal flag relay_0 0x01              # stages active-low for relay_0
SET hal reboot                         # apply both
```

Other HAL commands:

```
GET hal map                # full pin map: pin AND flags, with source
GET hal source             # "eeprom" or "defaults"
SET hal pin <name> <num>   # stage pin override
SET hal flag <name> <mask> # stage flag bitmask (decimal/hex/binary)
SET hal reset-defaults     # wipe EEPROM; reboot to apply
SET hal reboot             # soft reset via watchdog
```

Flag bit definitions:
- bit 0 (0x01): ACTIVE_LOW — invert output polarity. LOW = active.

If a bad pin map ever bricks a subsystem, `SET hal reset-defaults`
followed by `SET hal reboot` recovers because USB Serial0 is
hardcoded and can't be remapped.

## EEPROM layout (v2)

```
[0..3]      magic = 0x5A455243
[4]         version = 2
[5]         pin count
[6..6+2N-1] N entries, 2 bytes each: { pin_number, flags }
[end..+1]   CRC16 over preceding bytes
```

Upgrading from v1 (single-byte pin entries, no flags) is automatic:
the firmware sees the version mismatch on boot and reverts to
compiled defaults, then writes a v2 record. The operator just
re-applies any custom pin assignments they had.

## Adding a subsystem

1. Create `src/subsystems/<name>.h` and `.cpp` with a class deriving
   from `zerotx::Subsystem`.

2. Add `#include "subsystems/<name>.h"` and a static instance to
   `main.cpp`. Append the instance pointer to `kSubsystems[]`.

3. If new pins are involved: add an entry to `HalPinId` in `hal.h`,
   add the default + name in `hal.cpp` (`kHalPinDefaults`,
   `kHalPinNames`, and `kHalFlagDefaults` if non-zero default).
   Subsystem reads the pin via `hal::pin(id)` and polarity via
   `hal::activeLow(id)`.

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
