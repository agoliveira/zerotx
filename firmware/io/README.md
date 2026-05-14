# zerotx-io firmware (Mega 2560)

General IO board firmware. Multi-subsystem design: Noritake VFD,
indicator LEDs, panel buttons, WS2813 strip,
relays, LDR ambient-light sensor, piezo buzzer, and rotary encoder
all share one Mega 2560 connected to the daemon over USB-CDC.

## Status

- Core framework: line-oriented protocol parser, subsystem base class,
  central dispatcher, watchdog, capability discovery (`GET caps`),
  version reporting (`GET version`), boot-cause logging.
- HAL with EEPROM-persisted pin map AND per-pin flags. Both are
  reconfigurable at runtime; no reflash needed to re-pin or change
  polarity on a buried Mega.
- VFD subsystem: Noritake CU20025ECPB-W1J 2x20 in 4-bit HD44780 mode.
  Two instances supported (`vfd.0` and `vfd.1`) on independent pin
  groups, each running the animation engine independently.
- I2C LCD subsystem: HD44780-compatible character LCD on a PCF8574
  I2C backpack (LCM2002 default geometry, others configurable).
- Servo subsystem: 4 servo channels via the Arduino Servo library,
  lazy-attached to keep Timer 5 free until first use.
- Buttons: 10 panel buttons, 20ms polling debounce, edge events.
- LEDs: 4 generic indicator LEDs.
- Relays: 4 relays (separate from led for protocol clarity).
- WS2813: 16-pixel strip.
- LDR: analog light sensor with deadband + heartbeat-driven events.
- Buzzer: passive/active piezo via tone().
- Encoder: rotary encoder (KY-040 style), 4 transitions per detent,
  push-button included.

## Polarity convention

Default for every output (LEDs, relays): active-HIGH.
HIGH = active = energized. The HAL ACTIVE_LOW flag flips polarity
per-pin for boards wired through an inverting transistor stage.
Inverted logic is opt-in per pin, never assumed.

## Protocol

Line-oriented text over Serial0 (USB-CDC), 115200 baud. Three verbs:

```
SET <subsystem>[.<instance>] <param> [args...]
GET <subsystem>[.<instance>] [<param>]
EVENT <subsystem>[.<instance>] [args...]    # firmware -> daemon only
```

Responses to `GET` look like `> body...`. Errors look like
`! subsystem message`.

Examples:

```
GET version
> version zerotx-io 0.7.0-multi

GET caps
> caps hal vfd.0 vfd.1 lcd button.0..9 led.0..3 relay.0..3 ws.0 ldr.0 buzzer enc.0 servo.0..3

EVENT ldr.0 raw=412
EVENT enc.0 cw
EVENT enc.0 press
SET buzzer beep 2000 100
SET vfd.0 mode armed
SET led.0 on
```

## Subsystem command summary

### `vfd.<n>` - 2x20 character VFD (n = 0 or 1)
```
SET vfd.<n> mode <banner|idle|ambient|armed>
SET vfd.<n> brightness <0..3>
SET vfd.<n> line <row> <text...>
SET vfd.<n> clear
SET vfd.<n> tick [<n>]
SET vfd.<n> arm <0|1>
SET vfd.<n> fmmode <text>
SET vfd.<n> lq <0..100>
SET vfd.<n> batt <text>
SET vfd.<n> alarm <warn|critical|failsafe>
SET vfd.<n> disarmed
GET vfd.<n>
```

### `lcd.0` - I2C-attached HD44780 character LCD (LCM2002 default geometry)
```
SET lcd.0 line <row> <text...>
SET lcd.0 clear
SET lcd.0 backlight <0|1>
SET lcd.0 cursor <off|on|blink>
SET lcd.0 geom <cols> <rows>      # default 20 2; reconfigure for 16x2, 20x4, etc.
SET lcd.0 addr <0xNN>             # force I2C address; default is auto-detect
GET lcd.0
```

### `servo.<0..3>` - servo outputs (Arduino Servo library, lazy-attach)
```
SET servo.<n> angle <0..180>
SET servo.<n> us <500..2500>
SET servo.<n> detach
GET servo.<n>
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

### `button.<0..9>` - panel buttons
```
GET button.<n>            # > button.<n> <pressed|released>
EVENT button.<n> down     # press edge
EVENT button.<n> up       # release edge
```

### `ws.0` - WS2813 strip (16 pixels)
```
SET ws.0 pixel <index> <rrggbb>
SET ws.0 all <rrggbb>
SET ws.0 brightness <0..255>
SET ws.0 clear
GET ws.0
```

### `ldr.0` - ambient light sensor
```
SET ldr.0 deadband <0..1023>      # silence below this delta
SET ldr.0 heartbeat-ms <100..600000>
GET ldr.0                         # > ldr.0 raw=<n>
EVENT ldr.0 raw=<n>               # emitted on change > deadband or heartbeat
```

### `buzzer` - piezo buzzer
```
SET buzzer beep <freq_hz> <dur_ms>
SET buzzer silence
GET buzzer                        # > buzzer <sounding|idle>
```

### `enc.0` - rotary encoder + push button
```
GET enc.0                         # > enc.0 button=<pressed|released>
EVENT enc.0 cw                    # one detent clockwise
EVENT enc.0 ccw                   # one detent counter-clockwise
EVENT enc.0 press                 # button down edge
EVENT enc.0 release               # button up edge
```

## HAL: runtime pin and flag configuration

The Mega's pin assignments AND per-pin flags live in EEPROM. To
re-pin without reflash:

```
SET hal pin button_0 22                # stages new pin
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
- bit 0 (0x01): ACTIVE_LOW - invert output polarity. LOW = active.

USB Serial0 (pins 0/1) is hardcoded and cannot be remapped, so
`SET hal reset-defaults` is always reachable for recovery.

The companion `tools/zerotx-iohal-config` CLI automates JSON-driven
pin/flag management.

## EEPROM layout (v3)

```
[0..3]      magic = 0x5A455243
[4]         version = 3
[5]         pin count
[6..6+2N-1] N entries, 2 bytes each: { pin_number, flags }
[end..+1]   CRC16 over preceding bytes
```

Adding new pins to the firmware bumps the count, which forces a
fallback to defaults on first boot of the new firmware. Operator
re-applies any custom assignments via the iohal-config tool.

## Adding a subsystem

1. Create `src/subsystems/<name>.h` and `.cpp` with a class deriving
   from `zerotx::Subsystem`.

2. Add `#include "subsystems/<name>.h"` and a static instance to
   `main.cpp`. Append the instance pointer to `kSubsystems[]`.

3. If new pins are involved: add an entry to `HalPinId` in `hal.h`,
   add the default + name in `hal.cpp` (`kHalPinDefaults`,
   `kHalPinNames`). Subsystem reads pin number via `hal::pin(id)`
   and polarity via `hal::activeLow(id)`.

## Build and flash

```
pio run
pio run -t upload
pio device monitor
```

## Watchdog

500ms hardware watchdog kicked every loop iteration. Boot reason
reported via `EVENT boot power-on` or `EVENT boot watchdog-reset`.
`SET hal reboot` triggers a watchdog reset deliberately.
