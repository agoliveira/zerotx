# Spectator Protocol — ESP32 → Phones

Read-only telemetry dashboard for onlookers at the field. Self-
contained: WiFi SoftAP on the ESP32, no internet, no router.

## Transport

The ESP32 panel firmware runs a SoftAP plus an HTTP server (port 80)
and a WebSocket server (port 81).

| Setting | Value |
|---|---|
| SSID | `ZeroTX-Spectator` |
| Auth | WPA2-PSK |
| Password | `pédogalo` |
| Channel | 1 (fixed) |
| Max clients | 4 |
| Gateway | `192.168.4.1` |

Spectators connect, then open `http://192.168.4.1/` in any browser.
The SPA loads, opens a WebSocket to `ws://192.168.4.1:81/`, and
auto-reconnects on disconnect.

## HTTP routes

| Method | Path | Returns |
|---|---|---|
| GET | `/` | the dashboard SPA (single static HTML page, served from PROGMEM) |
| GET | `/anything-else` | 404 |

There are no API routes. State arrives only via the WebSocket.

## WebSocket protocol

Server pushes JSON state messages at 5Hz (200ms tick interval).
Clients send nothing; any received frame is ignored.

Message shape (all fields optional except `status` and `mode`):

```json
{
  "status": "DISARMED" | "ARMED" | "RTH" | "FAILSAFE",
  "mode": "ANGL",
  "alt_m": 142,
  "dist_m": 387,
  "spd_kmh": 64,
  "link_pct": 87,
  "bat_v": 14.20,
  "time_s": 187
}
```

Numeric fields are present only when the corresponding telemetry is
known. Missing fields render as `—` in the dashboard.

`status` is derived by the firmware from arm state, flight mode,
and active alarms (failsafe and RTH override the base armed/disarmed
state). Clients render it as a hero block; tile grid below shows the
remaining fields.

## Data flow

The ESP32 panel firmware already receives full telemetry over its
USB-CDC link from the daemon (see [`display.md`](display.md)). The
spectator subsystem reads from the same in-memory state and emits
the JSON above on each tick. No separate daemon-side wiring; the
daemon doesn't know spectators exist.

## Constraints and known limits

- WiFi shares the radio core with the ESP32 panel I2S DMA. Under
  spectator load, panel jitter may be visible. Bench-test under
  realistic client counts before relying in flight.
- No latency guarantees: 5Hz tick + WiFi + browser render = up to
  ~300ms behind the operator HUD.
- No authentication beyond WPA2. Password is shared, baked into
  firmware. Anyone who knows the password and is in range can
  watch.
- The dashboard is read-only by design. There is no "spectator API
  surface" and no plan to add one. Anyone wanting to interact with
  the system uses the operator GUI on the right LCD.

## Implementation reference

`firmware/display/src/spectator.h` and `spectator.cpp`. SPA HTML is
inline as `PROGMEM`.
