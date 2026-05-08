# Protocols

Wire-level references for each link in the ZeroTX system. Each
document covers the transport, framing, message catalog, lifecycle,
and known constraints for one link.

| Link | Transport | Direction | Doc |
|---|---|---|---|
| Daemon ↔ RP2040 | USB-CDC, COBS-framed binary | bidirectional | [`ipc.md`](ipc.md) |
| Daemon ↔ ESP32 (LED panel) | USB-CDC, line-text | bidirectional | [`display.md`](display.md) |
| Daemon ↔ INAV SITL | TCP, raw CRSF | bidirectional | [`sitl.md`](sitl.md) |
| Daemon → mwp | TCP, CRSF telemetry frames | daemon → mwp only | [`crsf-tee.md`](crsf-tee.md) |
| ESP32 → spectators | WiFi SoftAP + WebSocket JSON | ESP32 → clients only | [`spectator.md`](spectator.md) |

The aircraft-side CRSF link (RP2040 ↔ ELRS module ↔ aircraft FC) uses
the standard CRSF protocol unchanged; it's not documented here. See
the upstream CRSF / ExpressLRS docs.

## Cross-cutting design notes

- **Backward compatibility is intentional**. Every protocol either has
  an explicit version field (IPC's `MsgHello`) or tolerates unknown
  commands gracefully (display). This lets MCU firmware and
  daemon ship asynchronously.
- **The daemon is the authority** on every link except CRSF (where
  the FC is). MCUs are reactive; they don't initiate state changes
  beyond reporting hardware events (button presses, telemetry frames
  received).
- **Failure isolation**: each MCU's link can drop without taking down
  the others. The daemon's failure model assumes any single link
  may go quiet at any moment.
