# ZeroTX Documentation

Living docs. Each file targets a specific reader and purpose.

## Field & operations

- [`operation.md`](operation.md) — power-on, normal indicators, arming, in-flight surfaces, common failures. Read this first when you arrive at the field.
- [`hardware/wiring.md`](hardware/wiring.md) — every connection in the case, by module. Tables, no narrative.

## How-to

- [`howto/bench-test-sitl.md`](howto/bench-test-sitl.md) — INAV SITL + X-Plane bench setup; run the daemon's full flight pipeline without an aircraft.

## Architecture & internals

- [`architecture/overview.md`](architecture/overview.md) — system map, MCU split, daemon package map, data flows, persistence, failure model. Read this when you want to know how the whole thing fits together.
- [`hardware-bom.md`](hardware-bom.md) — case BOM and parts list.
- [`edgetx-yaml-notes.md`](edgetx-yaml-notes.md) — EdgeTX model YAML format notes (used by the model parser).

## Pending docs

- `architecture/overview.md` — system map, MCU split, daemon package structure, data flows.
- `protocols/{display-serial,vfd-serial,spectator}.md` — extracted from comments in the firmware sources.
- `formats/{model-yaml,recordings,narrate,geo-db}.md` — file formats consumed/produced by the daemon.
- `tools/{zerotx-inspect,geobuild,build-geo,fetch-voices}.md` — each CLI tool, what it does, when to use it.
- `interfaces/{api,gui,hud,led-panel}.md` — API surface, GUI tab map, HUD glossary, LED panel content reference.

## House style

- Terse on purpose; this is reference material, not a tutorial.
- Tables for anything that benefits from columns (pinouts, flags, error/cause/fix).
- Code blocks for commands that are meant to be copy-pasteable verbatim.
- Mermaid diagrams where component relationships matter; GitHub renders them inline.

## Protocols

- [`protocols/README.md`](protocols/README.md) — index across all wire-level protocol docs.
- [`protocols/ipc.md`](protocols/ipc.md) — daemon ↔ RP2040 framed binary (COBS + CRC, channel intent + telemetry passthrough).
- [`protocols/display.md`](protocols/display.md) — daemon ↔ ESP32 line-text protocol for the HUB75 panel.
- [`protocols/vfd.md`](protocols/vfd.md) — daemon → Pro Micro ASCII protocol with animation events.
- [`protocols/sitl.md`](protocols/sitl.md) — daemon ↔ INAV SITL via raw CRSF over TCP.
- [`protocols/crsf-tee.md`](protocols/crsf-tee.md) — daemon → mwp telemetry tee.
- [`protocols/spectator.md`](protocols/spectator.md) — ESP32 SoftAP + WebSocket dashboard for spectators.
