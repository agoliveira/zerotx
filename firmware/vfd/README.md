# ZeroTX VFD diagnostic firmware

Cool-glow-nerd-factor display. Firmware for the SparkFun Pro Micro
5V/16MHz that drives the Noritake CU20025ECPB-W1J 2x20 character
VFD wired in 4-bit HD44780 mode.

## Wiring

See the comment block at the top of `platformio.ini`. Summary:

| VFD pin | VFD function | Pro Micro |
| ------- | ------------ | --------- |
| 1       | VSS          | GND       |
| 2       | VDD          | +5V       |
| 3       | VO (contrast)| GND (unused on VFD) |
| 4       | RS           | D7        |
| 5       | R/W          | GND (write-only) |
| 6       | E            | D8        |
| 7-10    | D0-D3        | NC (4-bit mode) |
| 11      | D4           | D9        |
| 12      | D5           | D10       |
| 13      | D6           | D11       |
| 14      | D7           | D12       |
| 15-16   | LED+/-       | NC (no backlight on VFD) |

Power the VFD VDD from the case 5V CCTV PSU rail. The Pro Micro
draws its own power from the Pi USB host port.

## Wire protocol

Pi -> Pro Micro, ASCII over USB-CDC, one command per `\n`:

| Command          | Effect                                       |
| ---------------- | -------------------------------------------- |
| `L0 <content>`   | Write `<content>` to row 0 (top), pad/trunc to 20 |
| `L1 <content>`   | Write `<content>` to row 1 (bottom)          |
| `C`              | Clear display                                |
| `B <level>`      | Brightness 0..3 (0 = max, 3 = 25%)           |
| `V`              | Show firmware version banner                 |

Unknown commands are silently ignored to tolerate version skew.

## Build

```
cd firmware/vfd
pio run
```

## Upload

With the Pro Micro plugged in (it should appear as `/dev/ttyACM*`
on Linux):

```
pio run -t upload
```

## Boot banner

On boot, the firmware shows:

```
ZEROTX VFD
fw 0.1.0 awaiting
```

Until the daemon takes over with its first `L0`/`L1` write.

## Brightness command

The Noritake CU20025ECPB-W1J supports 4 brightness levels. The
firmware sends them via the lower bits of the standard FUNCTION
SET command. If the brightness doesn't change on bench testing,
swap to the extended sequence noted in the `setBrightness()`
comment in `src/main.cpp`.

## Manual test

With the Pro Micro on `/dev/ttyACM2` (substitute your path):

```
echo 'V' > /dev/ttyACM2          # banner
echo 'L0 Hello world' > /dev/ttyACM2
echo 'L1 second row    ' > /dev/ttyACM2
echo 'B 2' > /dev/ttyACM2        # half brightness
echo 'C' > /dev/ttyACM2          # clear
```

Then point the daemon at it:

```
zerotxd ... -vfd-port /dev/ttyACM2
```
