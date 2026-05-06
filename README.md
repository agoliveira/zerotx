# ZeroTX

![ZeroTX ground control station](docs/images/zerotx-render.png)

A workstation-class ground station for FPV and long-range fixed-wing flying, built into an aluminum briefcase. Replaces the ergonomic compromise of a handheld radio with twin LCDs (HUD plus moving map), an at-a-glance LED matrix panel, audio narration, an arcade trackball, and a Pi 400 keyboard. RF modules mount externally on poles; the case interior is wired-only.

A Raspberry Pi 400 runs the ZeroTX daemon and serves the web UIs that drive the LCDs. MCU satellites handle the rest: an RP2040 generates CPPM and CRSF for the radio link, a Mega 2560 drives the IO board (VFD, buttons, LEDs, relays, trackball ring, rotary encoder), and an ESP32 drives the HUB75 LED panel. Telemetry comes in over USB serial from an external ELRS TX backpack.

## Status

Active development. Daemon and firmware are functional on the bench. **Not yet flight-tested.** Field testing is planned once the full hardware integration is complete.

The system is designed for plugin extensibility; concrete plugin APIs are in development.

Current capability:

- ZeroTX daemon (Go) on the Pi reads USB joystick input, evaluates EdgeTX-format model logic (mixes, logic switches, custom functions), and emits CRSF channel intents to the RP2040 at 50 Hz
- RP2040 firmware (Pico SDK and CMake, hardware watchdog enabled) translates intents to CPPM or CRSF on the wire and watches the Pi-side link for failsafe
- Mega 2560 IO board drives VFD, trackball ring LEDs, buttons, indicator LEDs, relays, WS2813 strip, LDR, buzzer, and rotary encoder
- ESP32 drives the chained HUB75 LED matrix panel for at-a-glance state (IDLE, PREFLIGHT, FLIGHT, ALARM, RTH, POSTFLIGHT)
- Audio: pre-baked WAV samples for safety-critical alarms, Piper TTS (en_US-amy-medium) for narration
- HUD and Map web UIs driving twin LCDs via Chromium kiosk
- Offline satellite map tiles (pmtiles) for in-the-field operation without internet
- Pre-flight model and joystick selection via REST API
- IDLE/READY state machine: daemon refuses to emit channel intents until a model is explicitly loaded
- Automatic antenna tracker (AAT) integrated inline at the pole. An ESP32-S3 sits on the wired CRSF path between the case and the ELRS TX module, byte-pumps frames transparently in both directions, sniffs GPS telemetry, and drives a 2-DOF pan/tilt gimbal autonomously. Daemon-unaware; the case-side stack does not know the tracker exists.

## Layout

```
zerotx/
├── pi/daemon/    Go daemon and embedded web GUI
├── firmware/
│   ├── display/  ESP32 HUB75 panel driver
│   ├── io/       Mega 2560 IO board
│   └── tracker/  ESP32-S3 inline antenna tracker (pole-end)
├── rp2040/       Pico SDK C++ firmware (CRSF generator)
├── web/          HUD and Map browser UIs
├── tools/        Tile builders, replay tool, HAL configurator
├── configs/      Aircraft profiles, HAL configs
├── docs/         Architecture, connections, operations, protocols
├── SAFETY.md     Safety architecture and decisions
└── CHANGELOG.md  Milestone history
```

## Build

### Daemon (on a Linux dev machine or the Pi itself)

```sh
cd pi/daemon
sudo apt-get install -y libsdl2-dev   # native SDL2 headers
go mod tidy                           # first time only, generates go.sum
go build -o bin/zerotxd ./cmd/zerotxd
```

### RP2040 firmware

See [`rp2040/README.md`](rp2040/README.md).

### ESP32 panel and Mega IO firmware

See [`firmware/display/README.md`](firmware/display/README.md) and [`firmware/io/README.md`](firmware/io/README.md).

## Run (daemon, bench test)

```sh
cd pi/daemon
./bin/zerotxd -api 127.0.0.1:8080
```

Then point a browser at <http://127.0.0.1:8080/> for the GUI, or drive the
daemon directly via the REST API:

```sh
# Pick a joystick
curl -s http://127.0.0.1:8080/api/v1/joysticks | jq
curl -X POST http://127.0.0.1:8080/api/v1/joystick/select \
     -H 'Content-Type: application/json' -d '{"index":0}'

# Load a model
curl -X POST http://127.0.0.1:8080/api/v1/model/load \
     -H 'Content-Type: application/json' \
     -d '{"path":"configs/big_talon_zerotx.yml"}'

# Check state
curl -s http://127.0.0.1:8080/api/v1/preflight | jq
```

For the full deployed launch (model, joystick, RP2040 port, VFD port, site coordinates, tile prefetch) see [`docs/OPERATIONS.md`](docs/OPERATIONS.md).

## Documentation

Architecture, connections, operations, bootstrap, and wire-level protocols live in [`docs/`](docs/). Start with [`docs/README.md`](docs/README.md) for the index.

## Safety

Read [`SAFETY.md`](SAFETY.md) before modifying anything in the failsafe chain, the override custom function logic, the IDLE/READY state machine, or the joystick lifecycle. These contain decisions that protect the airframe and aren't obvious from reading the code alone.

## License

GPLv3. See [`LICENSE`](LICENSE).

## Acknowledgements

EdgeTX (model YAML format), ExpressLRS (RC link), INAV (flight controllers used for testing), SDL2 (joystick input), Piper TTS (audio narration), mwp (telemetry tee target), the ESP32-HUB75-MatrixPanel-DMA and U8g2 libraries (panel rendering), the PMTiles project (offline map tiles), and the broader FPV and RC community whose work this builds on.
