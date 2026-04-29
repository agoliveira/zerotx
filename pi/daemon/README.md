# ZeroTX daemon

Go daemon for the Pi side. Reads joysticks, runs the mixer, manages models,
talks to the RP2040 over USB-CDC, exposes (eventually) an HTTP+WebSocket API
for the GUI, and forwards telemetry to mwp.

Status: **M2.1**. Joystick reader, USB-CDC link, passthrough mapper, and a
runnable daemon binary that pumps stick movement to the RP2040 at 50 Hz.
HTTP+WS API and full mixer math land in M2.2 / M2.3.

## Layout

```
pi/daemon/
├── Makefile
├── cmd/
│   ├── zerotxd/         daemon binary
│   ├── zerotxctl/       CLI client (M2.3+)
│   └── zerotx-inspect/  model file inspector
├── internal/
│   ├── ipc/             USB-CDC framing (COBS+CRC), link state, byte-exact match w/ firmware
│   ├── joystick/        SDL2 joystick reader and mapper.InputState adapter
│   ├── model/           EdgeTX YAML parser + ZeroTX wrapper
│   ├── mapper/          source binding -> CRSF channel resolution (passthrough)
│   └── api/             HTTP+WS handlers (M2.3)
├── configs/             example model files
└── testdata/            EdgeTX models for parser tests
```

## One-time host setup

```sh
sudo apt install golang-go libsdl2-dev
sudo usermod -aG dialout $USER       # log out/in for /dev/ttyACM access
```

## Build

```sh
cd pi/daemon
go mod tidy        # first time only, fetches yaml.v3 + go-sdl2 + go-serial
make build         # produces ./bin/zerotxd and ./bin/zerotx-inspect
```

The first build pulls in the SDL2 cgo bindings and takes a minute or two.
Subsequent builds are seconds.

## Tests

```sh
make test          # full suite, recompiles cgo bindings on cold cache
make test-fast     # skips joystick (cgo); covers ipc, model, mapper
```

`internal/ipc` includes a byte-exact cross-validation against
`rp2040/tests/test_ipc.c` test vectors. The same wire frames produced by the
firmware unit test must match what the Go encoder produces.

## Running the daemon

List joysticks:

```sh
make list-joysticks
# or:
./bin/zerotxd -list-joysticks
```

Run with autodetect (RP2040 auto-found, first joystick opened):

```sh
./bin/zerotxd -v
```

Run with explicit choices:

```sh
./bin/zerotxd \
    -port /dev/ttyACM0 \
    -joystick-name "Thrustmaster" \
    -model configs/big_talon_zerotx.yml \
    -v
```

Or via Makefile:

```sh
make run JOYSTICK_NAME="Thrustmaster" MODEL=configs/big_talon_zerotx.yml
```

Ctrl-C to stop. The RP2040 should go through HOLD then FAILSAFE within
~950 ms of shutdown (same chain as M1).

## What M2.1 actually does

- Opens the RP2040 USB-CDC link, sends 100 ms heartbeats, reads incoming
  frames.
- Opens a joystick (SDL2 `SDL_Joystick` API: raw axes, buttons, hats).
- Loads a ZeroTX model file (optional). The `zerotx.source_bindings` section
  binds abstract names (Thr, Ail, SE, 6POS, etc) to physical inputs.
- 50 Hz loop: read joystick state, resolve each `mixData` entry's source via
  bindings, write CRSF raw channel values, send `CHANNEL_INTENT` to MCU.
- Without a model loaded: emits safe defaults (sticks centered, throttle
  low, arm low). The MCU still gets a heartbeat, so the link state stays
  green even before a model is configured.

What it does **not** do yet:

- No mixer math beyond `weight=100` passthrough (no curves, no expo, no
  multiplex modes).
- No logic switches (sources like `ls(3)` resolve to safe defaults).
- No special functions (no `OVERRIDE_CHANNEL`, no `PLAY_TRACK`).
- No HTTP+WS API. CLI flags only.
- No GCS panel switch input (the RP2040 doesn't emit `INPUT_STATE` yet;
  that's M2.2 firmware-side).

## Wire protocol

See `../../docs/ipc_protocol.md`. The Go side in `internal/ipc` and the C
side in `rp2040/src/protocol.h` share constants byte-for-byte; a Go test
verifies the encoders agree. Any change to either side is a coordinated
change to both.

## Inspecting an EdgeTX model

```sh
./bin/zerotx-inspect testdata/big_talon.yml
./bin/zerotx-inspect -zerotx configs/big_talon_zerotx.yml
```
