# Display protocol (HUB75 panel)

This document defines the wire protocol between the ZeroTX daemon (running
on the Pi 400) and the display device (an ESP32 driving two chained
HUB75 P2.5 64x32 panels arranged as a single 128x32 logical surface).

## Transport

USB CDC serial. The display device enumerates as `/dev/ttyACM*` or
`/dev/ttyUSB*` depending on the USB-serial chip. The daemon opens the
port at 115200 8N1.

## Format

Line-delimited UTF-8 text. One message per line, terminated by `\n`.
Each line is independent: parsers MUST drop malformed lines and
continue with the next.

Message structure:

```
<source> <command> [args...]
```

- `<source>` is always `DISP` for messages on this protocol. The token
  exists so a future multiplexer can route by source when multiple
  devices share the same daemon connection
- `<command>` is the message type
- `[args...]` are space-separated key=value pairs or positional values

String values containing spaces are quoted with double quotes. There is
no escape sequence support in round 1; messages MUST NOT contain
embedded newlines or unbalanced quotes.

## Direction and authority

The daemon is the authority. The display is a viewer. The daemon owns
all state; the display renders what it's told and reports back only
liveness and errors.

```
daemon --MODE/STATE/ALARM/...--> display
daemon <-READY/HEARTBEAT/ERROR-- display
```

No acknowledgments. Lost messages self-heal: the daemon sends state
snapshots periodically, so any glitch is corrected on the next snapshot.

## Modes

The display has six render modes. The daemon switches between them
with `DISP MODE <name>`. Mode transitions are immediate; the display
clears and re-renders.

| Mode        | Trigger                                  | Content                                           |
| ----------- | ---------------------------------------- | ------------------------------------------------- |
| `IDLE`      | Daemon startup, no model loaded          | Clock, GS battery if known, ambient               |
| `PREFLIGHT` | Model loaded, awaiting arm               | Checklist items as they pass                      |
| `FLIGHT`    | Armed                                    | Three-tile cluster: BAT, ALT, DIST                |
| `ALARM`     | An alarm fires                           | Full-width banner, alarm text, level coloring     |
| `RTH`       | Return-to-home active                    | Distance + directional arrow toward home          |
| `POSTFLIGHT`| Disarm                                   | Summary scroll, holds ~30s, then back to IDLE     |

The display tracks the current mode internally. If a mode-specific
state field arrives while in the wrong mode, it's stored but not
displayed until the mode changes.

## Bandwidth budget

| Message    | Frequency           | Approx size     |
| ---------- | ------------------- | --------------- |
| `MODE`     | On transition       | ~25 bytes       |
| `STATE`    | 5Hz active, 1Hz idle| ~120 bytes      |
| `ALARM`    | On alarm fire       | ~80 bytes       |
| `MSG`      | One-shot            | ~200 bytes      |
| `BRIGHTNESS`| On operator change | ~25 bytes       |
| `HEARTBEAT`| 0.2Hz               | ~40 bytes       |

Total peak: well under 2 KB/s. USB CDC at 115200 baud handles 11 KB/s
comfortably.

## Daemon -> Display messages

### `DISP MODE <name>`

Switch the render mode. `<name>` is one of: `IDLE`, `PREFLIGHT`,
`FLIGHT`, `ALARM`, `RTH`, `POSTFLIGHT`. Display clears and re-renders
immediately.

```
DISP MODE FLIGHT
DISP MODE IDLE
```

### `DISP STATE <key=value>...`

Periodic state snapshot. All fields optional; missing fields preserve
their previous value. Sent at 5Hz during active flight, 1Hz when idle.
Field reference:

| Key       | Type     | Meaning                                |
| --------- | -------- | -------------------------------------- |
| `armed`   | 0|1      | Arming state                           |
| `bat`     | float    | Battery voltage (V)                    |
| `batpct`  | int      | Battery percent (0-100)                |
| `alt`     | int      | Altitude (m)                           |
| `dist`    | int      | Distance from home (m)                 |
| `spd`     | int      | Speed (km/h)                           |
| `link`    | int      | Link quality (0-100)                   |
| `sats`    | int      | GPS satellite count                    |
| `mode`    | string   | Flight mode name (e.g. "ANGLE")        |
| `gps`     | string   | GPS fix state ("none", "2d", "3d")     |
| `time`    | int      | Mission elapsed time (seconds)         |

```
DISP STATE armed=1 bat=11.7 batpct=73 alt=124 dist=430 spd=22 link=87 sats=11 mode=ANGLE gps=3d time=145
```

### `DISP ALARM <level> "<text>"`

Fire an alarm overlay. Replaces the current mode's render until
cleared. `<level>` is one of: `info`, `notice`, `warning`, `critical`.
The display selects color and animation based on level.

```
DISP ALARM critical "BATTERY EMPTY"
DISP ALARM warning "GPS lost"
```

### `DISP CLEAR-ALARM`

Remove the alarm overlay. Display returns to whatever mode it was
in before the alarm.

```
DISP CLEAR-ALARM
```

### `DISP MSG "<text>"`

One-shot scrolling message. Display shows it once across the full
width, then returns to the prior mode's rendering. Used for boot
greeting, post-flight summary text, etc.

```
DISP MSG "ZeroTX online. Awaiting model."
```

### `DISP BRIGHTNESS <0-100>`

Set panel brightness. 0 = off, 100 = full. Affects all panels in
the chain.

```
DISP BRIGHTNESS 50
```

### `DISP PING`

Request an immediate `PONG` reply. Used for connection health checks.

```
DISP PING
```

## Display -> Daemon messages

### `DISP READY version=<v> panels=<n> w=<px> h=<px>`

Sent on boot. Announces firmware version, panel count, and total
logical surface dimensions. Daemon uses this to verify the panel is
configured as expected.

```
DISP READY version=0.1.0 panels=2 w=128 h=32
```

### `DISP HEARTBEAT uptime=<seconds>`

Periodic liveness report. Sent every 5 seconds. Daemon treats
absence-for-15-seconds as a disconnect.

```
DISP HEARTBEAT uptime=3247
```

### `DISP ERROR "<message>"`

Render fault, missing font, parse error, etc. Daemon logs these
verbatim. The display does not retry; the next state update will
overwrite whatever broke.

```
DISP ERROR "unknown mode: FLOOP"
```

### `DISP PONG`

Reply to `PING`.

```
DISP PONG
```

## Connection lifecycle

1. Daemon opens the serial port. If unavailable, retries every 5
   seconds. Failures don't block daemon startup
2. ESP32 boots, sends `DISP READY version=... panels=2 w=128 h=32`
3. Daemon receives `READY`, logs the version, and begins sending
   `DISP MODE` (current mode) + `DISP STATE` (current state) to
   synchronize the display
4. Daemon continues sending state snapshots and event messages as
   conditions change
5. ESP32 emits `HEARTBEAT` every 5s
6. If daemon stops receiving heartbeats for 15s, it logs a warning
   and continues; reconnection happens automatically when heartbeats
   resume
7. If the serial port returns an error (cable unplugged, ESP32
   reset), daemon closes the port and starts the reconnect retry
   loop

## Error handling and resilience

- Malformed messages are dropped silently. Parser MUST be permissive:
  unknown commands, extra args, missing args all just get ignored
- The display NEVER blocks on serial. If the daemon is silent, the
  display continues rendering the last known state
- The daemon NEVER blocks on serial. If the display port is full or
  closed, writes are dropped and logged
- After any reconnect (cable, reboot), the display sends a fresh
  `READY` and the daemon resyncs from scratch

## Future extensions

Reserved keys for future use (parsers should accept and ignore
unknown keys):

- `wind`, `windspd`, `windhdg`: wind data once the recorder tracks it
- `homedir`: bearing to home in degrees
- `vbat_gs`: ground station UPS voltage
- `audiothresh`: current audio threshold for display feedback

New message types will be added rather than overloading existing
ones. Each new type is documented in this file before being shipped.

## Versioning

The protocol version is implicit in the daemon and firmware
versions. Both sides are tested together; the daemon and firmware
of a given ZeroTX release work as a pair. There's no version
negotiation in the wire format yet.

If a breaking change becomes necessary, this document gets a
`Protocol version: 2` header at the top, both sides update, and
backward compatibility is not maintained (there's only one
deployment).
