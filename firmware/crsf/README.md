# ZeroTX RP2040 firmware (M1)

C firmware for the RP2040-Zero acting as the safety co-processor between the Pi 400 and the ELRS module.

## Responsibilities (M1)

- Read CHANNEL_INTENT and HEARTBEAT messages from Pi over USB-CDC
- Pack 16 channels into CRSF `RC_CHANNELS_PACKED` frames
- Emit those frames at 50 Hz on UART0 to the ELRS module
- Track Pi heartbeat. On loss: HOLD (last channels) for 600 ms, then FAILSAFE (stop emitting CRSF entirely)
- Drive the on-board WS2812 with state-coded color/blink

## Pin map (RP2040-Zero)

| Function   | GPIO | Note                                  |
|------------|------|---------------------------------------|
| CRSF TX    | 0    | UART0 TX -> JR-bay pin 5 (CRSF in)    |
| CRSF RX    | 1    | UART0 RX <- JR-bay pin 4 (CRSF out)   |
| Status LED | 16   | On-board WS2812                       |
| GND        | GND  | Common with module GND                |

CRSF baud: 400 000 (configurable via `CRSF_BAUD` in `src/main.c`).

## Status LED

| State     | Pattern                |
|-----------|------------------------|
| BOOT      | white slow pulse       |
| PENDING   | amber solid            |
| OK        | green solid            |
| HOLD      | amber rapid blink      |
| FAILSAFE  | red rapid blink        |

## Build

One-time setup on Ubuntu:

```
sudo apt install cmake gcc-arm-none-eabi libnewlib-arm-none-eabi libstdc++-arm-none-eabi-newlib
git clone --depth 1 https://github.com/raspberrypi/pico-sdk.git ~/pico-sdk
( cd ~/pico-sdk && git submodule update --init )
echo 'export PICO_SDK_PATH=~/pico-sdk' >> ~/.bashrc
```

Build:

```
mkdir -p build && cd build
cmake ..
make -j$(nproc)
```

Output: `build/zerotx-fw.uf2`

## Flash

Hold the BOOT button while plugging the RP2040-Zero into USB. It enumerates as a mass storage volume `RPI-RP2`. Then:

```
tools/flash.sh
```

The script finds the volume, copies the UF2, and the board reboots into the new firmware.

## Bench

The Pi-side bench tool drives channel intent and heartbeats over USB-CDC and prints anything the firmware logs back.

```
pip install pyserial   # one-time
tools/m1_bench.py
```

REPL commands: `set <ch> <val>`, `arm`, `disarm`, `throttle <val>`, `safe`, `sweep <ch>`, `pause`, `resume`, `state`, `help`, `quit`.

`pause` is the failsafe test: stop sending CHANNEL_INTENT, watch the LED transition green -> amber-blink (HOLD) within 200 ms, then red-blink (FAILSAFE) ~600 ms after that. CRSF emission stops at the FAILSAFE transition. The FC connected downstream should engage RX_LOSS / failsafe within another ~150 ms.

## Tests

Host-side tests cross-validate the C and Python framing implementations.

```
cd tests
gcc -std=c11 -Wall -Wextra -I../src -o test_ipc test_ipc.c ../src/ipc.c
./test_ipc            # round-trip + CRC check value
python3 test_cross.py # Python encoder vs C encoder, byte-exact
```

## Wire format

See `../../docs/protocols/ipc.md` for the COBS+CRC framing and message catalog.
