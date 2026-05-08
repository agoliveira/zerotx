# ZeroTX

![ZeroTX ground control station](docs/images/zerotx-render.png)

A workstation-class ground station for FPV and long-range fixed-wing flying, built into an aluminum briefcase. Replaces the ergonomic compromise of a handheld radio with twin LCDs (HUD plus moving map), an at-a-glance LED matrix panel, audio narration, an arcade trackball, and a Pi 400 keyboard. RF modules mount externally on poles; the case interior is wired-only.

A Raspberry Pi 400 runs the ZeroTX daemon and serves the web UIs that drive the LCDs. MCU satellites handle the rest: an RP2040 generates CRSF for the radio link, a Mega 2560 drives the IO board (VFD, buttons, LEDs, relays, trackball ring, rotary encoder), and an ESP32 drives the HUB75 LED panel. Telemetry returns from the ELRS module on the same CRSF wire, decoded by the RP2040 and forwarded to the daemon over USB-CDC.

## Why

Handheld radios are ergonomically optimized for racing. Long-range work is a different sport: you stand still, you watch a screen for minutes at a time, you want big numbers and a real map. ZeroTX is a workstation you set down, not a handset you hold.

The system is also designed to be modular. Models, audio dictionaries, HAL pin maps, and panel definitions are data, not code. A plugin system is planned.

## Status

Active development. Daemon and firmware are functional on the bench. **Not yet flight-tested.** Field testing is planned once the full hardware integration is complete.

Current capability:

- ZeroTX daemon (Go) on the Pi reads USB joystick input, evaluates EdgeTX-format model logic (mixes, logic switches, custom functions), and emits CRSF channel intents to the RP2040 at 50 Hz
- RP2040 firmware (Pico SDK and CMake, hardware watchdog enabled) translates intents to CRSF on the wire and watches the Pi-side link for failsafe
- Mega 2560 IO board drives VFD, trackball ring LEDs, buttons, indicator LEDs, relays, WS2813 strip, LDR, buzzer, and rotary encoder
- ESP32 drives the chained HUB75 LED matrix panel for at-a-glance state (IDLE, PREFLIGHT, FLIGHT, ALARM, RTH, POSTFLIGHT)
- Audio: pre-baked WAV samples for safety-critical alarms, Piper TTS (en_US-amy-medium) for narration
- HUD and Map web UIs driving twin LCDs via Chromium kiosk
- Offline satellite and OSM map tiles (PMTiles) for in-the-field operation without internet
- Per-flight recordings stored in SQLite, with automatic post-flight narration summaries
- IDLE/READY state machine: daemon refuses to emit channel intents until a model is explicitly loaded

The default cable run from the case to the ELRS module is single-wire CRSF over a short multi-conductor cable, no transceivers, no pole electronics. An optional extended configuration adds RS-422 transceivers and a pole-end project box for longer runs or for hosting an inline ESP32-S3 antenna tracker (firmware complete, integration deferred); the case-side stack is unchanged in either configuration.

## Layout

```
zerotx/
├── pi/daemon/         Go daemon, embedded web UI, configs
│   ├── cmd/           daemon entry and CLIs
│   ├── internal/      daemon packages (telemetry, audio, mapper, ...)
│   ├── web/           HUD and Map browser UIs
│   └── configs/       aircraft profiles, panel YAML
├── firmware/
│   ├── display/       ESP32 HUB75 panel driver
│   ├── io/            Mega 2560 IO board
│   └── tracker/       ESP32-S3 inline antenna tracker (optional add-on)
├── rp2040/            Pico SDK firmware (CRSF generator)
├── tools/             Tile builders, replay tools, HAL configurator
├── docs/              Architecture, connections, operations, protocols
├── SAFETY.md          Safety architecture and decisions
└── CHANGELOG.md       Recent changes (older history lives in git)
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

Minimal launch with no MCUs attached, useful for verifying the build:

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

For the full deployed launch (model, joystick, RP2040 port, IO board port, site coordinates, tile prefetch) see [`docs/OPERATIONS.md`](docs/OPERATIONS.md).

## Documentation

Architecture, connections, operations, bootstrap, and wire-level protocols live in [`docs/`](docs/). Start with [`docs/README.md`](docs/README.md) for the index.

## Contributing

Issues and PRs are welcome. Worth reading before non-trivial changes:

- [`SAFETY.md`](SAFETY.md): the failsafe chain, override CF logic, IDLE/READY state machine, and joystick lifecycle. Anything that touches the airframe-safety path goes through here.
- [`docs/DECISIONS.md`](docs/DECISIONS.md): why things are the way they are; consult before relitigating settled choices.
- [`docs/protocols/`](docs/protocols/): wire formats between daemon and each MCU.

## Safety

Read [`SAFETY.md`](SAFETY.md) before modifying anything in the failsafe chain, the override custom function logic, the IDLE/READY state machine, or the joystick lifecycle. These contain decisions that protect the airframe and aren't obvious from reading the code alone.

## License

GPLv3. See [`LICENSE`](LICENSE).

## Acknowledgements

EdgeTX (model YAML format), ExpressLRS (RC link), INAV (flight controllers used for testing), SDL2 (joystick input), Piper TTS (audio narration), the ESP32-HUB75-MatrixPanel-DMA and U8g2 libraries (panel rendering), the PMTiles project (offline map tiles), and the broader FPV and RC community whose work this builds on.
