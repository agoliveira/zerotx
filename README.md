# ZeroTX

A direct-RF ground station that replaces a conventional RC radio. A Raspberry Pi
400 drives an ExpressLRS module over CRSF; an RP2040 acts as a safety co-processor
handling watchdog duty and IPC framing.

The aim is to provide a workstation-class ground station experience for FPV and
long-range fixed-wing flying without the ergonomic compromise of a handheld radio.

## Status

Active development. Daemon and firmware are functional on the bench. **Not yet
flight-tested.** Field testing is planned once the full hardware integration
is complete.

Current capability:

- ZeroTX daemon (Go) on the Pi reads USB joystick input, evaluates EdgeTX-format
  model logic (mixes, logic switches, custom functions), and emits CRSF channel
  intents to the RP2040 at 50 Hz
- RP2040 firmware translates intents to CRSF on the wire and watches the Pi-side
  link for failsafe
- Pre-flight model and joystick selection via REST API
- Web GUI (embedded in the daemon binary) with inspection tabs for channels,
  logic, panel, joystick, model, and logs
- IDLE/READY state machine: daemon refuses to emit channel intents until a
  model is explicitly loaded

## Layout

```
zerotx/
├── pi/daemon/    Go daemon and embedded web GUI
├── rp2040/       Pico SDK C++ firmware
├── docs/         Architecture and protocol notes
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

See `rp2040/README.md`.

## Run (daemon, no flight)

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

See `pi/daemon/configs/example_zerotx_model.yml` for the model overlay format
and `docs/architecture.md` for the bigger picture.

## Safety

Read `SAFETY.md` before modifying anything in the failsafe chain, the override
custom function logic, the IDLE/READY state machine, or the joystick lifecycle.
These contain decisions that protect the airframe and aren't obvious from
reading the code alone.

## License

GPLv3. See `LICENSE`.

## Acknowledgements

EdgeTX project (model YAML format), ExpressLRS project (RC link), INAV project
(flight controllers used for testing), SDL2 (joystick input), and the broader
FPV/RC community whose work this builds on.
