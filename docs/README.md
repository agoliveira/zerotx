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
- [`ipc_protocol.md`](ipc_protocol.md) — daemon ↔ RP2040 IPC framed protocol (will move under `protocols/`).
- [`protocols/`](protocols/) — wire protocol references (CRSF tee, VFD serial, display serial, spectator JSON, MSP/SITL).

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
