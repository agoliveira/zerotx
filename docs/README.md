# ZeroTX Documentation

Documentation for the ZeroTX ground control station. Each top-level doc has a single audience and a single purpose; no doc duplicates content found in another.

![ZeroTX ground control station, opened](images/zerotx-render.png)

## Top-level docs

| Doc | Audience | What's in it |
|---|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | future-me, 18 months out | system overview, components, data flows, daemon subsystem map |
| [CONNECTIONS.md](CONNECTIONS.md) | me with the lid open | physical wiring, USB topology, power distribution, signal paths |
| [OPERATIONS.md](OPERATIONS.md) | me at the box | cold start, daemon launch, pre-flight, recovery procedures |
| [BOOTSTRAP.md](BOOTSTRAP.md) | me reflashing the Pi | bare-metal provisioning from blank SSD |
| [DECISIONS.md](DECISIONS.md) | future-me | locked decisions, do not re-litigate |
| [ROADMAP.md](ROADMAP.md) | future-me | pinned work, B-tier backlog, open questions |

## Wire-level protocols

- [protocols/README.md](protocols/README.md): index across protocol docs
- [protocols/display.md](protocols/display.md): daemon to ESP32 HUB75 panel command grammar
- [protocols/ipc.md](protocols/ipc.md): daemon to RP2040 framed binary (COBS + CRC, channel intent and telemetry passthrough)
- [protocols/sitl.md](protocols/sitl.md): daemon to INAV SITL via raw CRSF over TCP
- [protocols/crsf-tee.md](protocols/crsf-tee.md): daemon CRSF tee output
- [protocols/spectator.md](protocols/spectator.md): ESP32 SoftAP plus WebSocket spectator dashboard (currently removed from display firmware; see DECISIONS.md)

## Reference docs

- [hardware-bom.md](hardware-bom.md): case BOM and parts list
- [hardware-pinout.md](hardware-pinout.md): MCU pin allocations (RP2040, Mega 2560, ESP32)
- [edgetx-yaml-notes.md](edgetx-yaml-notes.md): EdgeTX model YAML format notes
- [howto/bench-test-sitl.md](howto/bench-test-sitl.md): INAV SITL plus X-Plane bench setup

## Per-firmware READMEs (linked, not duplicated)

- `firmware/display/README.md`: ESP32 HUB75 panel driver
- `firmware/io/README.md`: Mega 2560 IO board firmware, canonical pin table, HAL flags
- `firmware/tracker/README.md`: ESP32-S3 antenna tracker firmware (optional pole-end add-on)
- `rp2040/README.md`: RP2040 CRSF generator firmware

## Tools

- `tools/zerotx-iohal-config/`: HAL pin and flag configurator for Mega IO
- `tools/maps/`: satellite tile downloader, OSM tile builder, regional download wrappers
- `tools/zerotx-replay/`: log replay tool

## Conventions

- **TODO** markers identify in-progress build details that need real values plugged in.
- The canonical Mega pin table lives in `firmware/io/README.md`. Top-level docs do not duplicate it.
- The canonical HUB75 wire protocol lives in `protocols/display.md`. Top-level docs do not duplicate it.
- Locked decisions live in `DECISIONS.md`. Cross-referenced, not duplicated, in topical docs.
