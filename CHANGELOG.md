# Changelog

This file captures the milestone history of ZeroTX up to the point this
repository was created. Future changes are tracked via git history.

## 0.11.1 — Joystick reattach on hot-plug

- Daemon detects `JOYDEVICEADDED` events and reattaches automatically
  when a previously-disconnected joystick returns (matched by GUID).
  Different GUID is ignored.
- `Reader.GUID()` accessor and `joystick.GUIDForIndex()` helper.

## 0.11.0 — Pre-flight foundation, daemon side

- `joystickHolder` with atomic-pointer-style swappable wrapper.
- Per-instance event filtering: multiple readers can coexist.
- Global SDL event pump (`joystick.PumpEvents`) dispatches to all
  registered readers via `registerReader` / `unregisterReader`.
- Joystick disconnect → 500 ms hold of last-known values → silent.
  RP2040 + FC failsafe takes over.
- Endpoints:
  - `GET /api/v1/joysticks`
  - `POST /api/v1/joystick/select {index, emergency}`
  - `POST /api/v1/joystick/release`
  - `GET /api/v1/models?dir=...`
  - `POST /api/v1/flight/arm {armed}`
- "No swap during armed flight" guarantee, with `emergency=true` bypass.

## 0.10.0 — IDLE / READY state machine

- Daemon boots IDLE unless `-model PATH` is passed.
- `atomic.Pointer[Stack]` for lock-free model swap.
- IDLE = no CRSF emission; FC failsafe is in effect.
- Endpoints:
  - `GET /api/v1/preflight`
  - `POST /api/v1/model/load`
  - `POST /api/v1/model/unload`

## 0.9.0 — Logs tab

- `internal/logbuf` ring buffer (default 2000 entries), thread-safe.
- `log.SetOutput` redirects to `logBuf.TeeWriter(os.Stderr)`.
- `GET /api/v1/logs?since=<RFC3339Nano>` endpoint.
- GUI Logs tab: filter, pause/resume, clear, auto-scroll, colour for
  warn/error.

## 0.8.0 — Model tab

- `GET /api/v1/model/details` with full mixes, logic, custom functions,
  sensors.
- `GET /api/v1/model/image` serves model bitmap.
- `-model-image PATH` flag.
- Sensor unit lookup table.
- Live Active dot for logic switches.

## 0.7.x — Web GUI tabs

- Connection, Channels, Logic, Panel, Joystick tabs.
- Web GUI embedded via `go:embed`, fallback to `-web-dir PATH` for dev.

## 0.6.0 — HTTP / WebSocket API

- `internal/api` package with state, server, stream, tests.
- `/api/v1/health`, `/model`, `/state`, `/stream` (WS at 10 Hz).

## 0.5.x and earlier — Daemon foundation

- `internal/source` resolver, `internal/logic` engine,
  `internal/cf` custom function processor, `internal/mapper`.
- EdgeTX YAML parsing with ZeroTX overlay.
- USB-CDC IPC to RP2040 with COBS+CRC framing.
- SDL2 joystick input.
- Big Talon arm chain validated end to end.

## RP2040 firmware

Firmware milestones m0 (boot, USB-CDC, idle), m1 (CRSF on the wire,
heartbeat watchdog) are tracked separately in `rp2040/`.

---

Detailed transcripts of the design discussions for each milestone are
preserved outside this repository. The summaries above are the canonical
record of what was delivered.
