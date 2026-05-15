# ZeroTX Roadmap

Pinned items and tracked future work. Living document; append as new items surface, remove as items land.

For locked decisions that should not be re-litigated, see `docs/DECISIONS.md`.

## Pinned

Items deferred to a specific later moment, not abandoned.

- **Pi 400 CPU optimization**: currently 70-80% load with two browsers. Profile per-component, evaluate the optimization flags listed in `docs/BOOTSTRAP.md`, measure each in isolation. Goal: comfortable headroom for telemetry and TTS bursts.
- **Stan-as-datahub V2**: replay tool architecture pinned for later. Stan would host replay sessions for offline analysis and bench testing without involving the live daemon.
- **Daemon-side semantic consumers for IO board events**: LDR-driven auto-brightness, buzzer alarm pattern engine, encoder UI binding, button event semantics. Plumbing exists in `iohub`; consumers don't yet.
- **`PRAGMA busy_timeout=5000`** in `tools/maps/sat-download/main.go`. SQLite write contention during long downloads.

## B-tier backlog

Smaller items, off the critical path. Append as they surface.

- Power regulator topology fully documented in the builder's manual Section 4.2: replace TODO sections with measured values once the build is final.
- ESP32 udev `idVendor` and `idProduct` confirmed for the specific board in use, plugged into `docs/BOOTSTRAP.md`.
- VFD brightness control: if CU20025ECPB-W1J exposes a software-readable contrast or brightness line, wire it to ambient light (LDR) alongside the planned panel auto-brightness.

## Open questions

Decisions that need to be made but haven't been. Resolve and migrate to `docs/DECISIONS.md` once locked.

- Field-vs-lab power switchover mechanism: passive ORing diodes, manual switch, or auto-changeover relay?
- EEPROM `BOOT_ORDER`: keep default `0xf41` (SD then USB) or change to `0xf14` (USB first)?
- USB hub power source: tap the internal 5V rail, or use the hub's own external brick?
- Audio routing: ALSA direct (lower CPU) or PulseAudio (more flexible)?
- Kiosk autostart: systemd user units (clean lifecycle) or LXDE autostart (simpler, default)?
- Single ELRS pole vs dual: design supports two pole-mounted modules. Always run both, or one with the other slot empty?

## See also

- `docs/DECISIONS.md`: locked decisions
- `docs/ARCHITECTURE.md`, `docs/OPERATIONS.md`, `docs/BOOTSTRAP.md`, `docs/manuals/BUILDER.md`: docs containing TODO markers that may graduate to roadmap items
