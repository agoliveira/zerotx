# SITL Protocol — Daemon ↔ INAV SITL

Bench-test alternate FC endpoint. The daemon speaks raw CRSF over
TCP to an INAV SITL instance. Used in place of the RP2040 IPC link
when `-fc-tcp-addr` is set.

## Transport

TCP. The daemon dials a host:port; INAV SITL must already be
listening. Default port mapping in INAV SITL:

| INAV UART | TCP port |
|---|---|
| UART1 (default MSP) | 5760 |
| UART2 (default MSP) | 5761 |
| UART3 (typically Serial RX) | 5762 |
| ... | 5760 + (uart - 1) |

ZeroTX expects UART3 in SITL configured as Serial RX with CRSF
provider. Connect the daemon to the matching port (typically
`127.0.0.1:5762`).

## Framing

Standard CRSF on the wire. The TCP byte stream is treated as if it
were the UART byte stream. No additional framing or escaping.

CRSF frame layout:

```
+------+------+------+--------------+------+
| ADDR | LEN  | TYPE | PAYLOAD      | CRC8 |
+------+------+------+--------------+------+
   1B    1B    1B      LEN-2 bytes    1B
```

- `ADDR`: 0xC8 (Flight Controller)
- `LEN`: bytes after this field, including TYPE, PAYLOAD, and CRC.
  Total wire frame is `LEN + 2` bytes.
- `CRC8`: CRSF poly 0xD5, init 0x00, computed over `[TYPE..PAYLOAD]`.

## Direction

| Direction | Frames |
|---|---|
| Daemon → SITL | RC channels packed (`type=0x16`) at 50Hz |
| SITL → Daemon | All standard CRSF telemetry frames the FC produces (battery, attitude, GPS, link stats, flight mode, etc.) |

## Channel encoding (daemon → SITL)

`RC_CHANNELS_PACKED` payload: 16 channels × 11 bits each, little-
endian-style packing into 22 bytes.

```
bit  0 .. 10 -> channel 0
bit 11 .. 21 -> channel 1
bit 22 .. 32 -> channel 2
...
bit 165 .. 175 -> channel 15
```

Channel value range: 0 .. 2047 (11-bit). ZeroTX uses CRSF-standard
172 (min) / 992 (mid) / 1811 (max).

The daemon emits at 50Hz regardless of mixer tick rate. If the
mixer hasn't produced a snapshot yet (early boot), the sender
defensively emits center-stick across all channels; SITL stays in
RX failsafe until a real snapshot arrives.

## Telemetry decoding (SITL → daemon)

The daemon's reader strips each incoming frame to
`[ADDR][TYPE][PAYLOAD]` (drops LEN and CRC after validation) and
hands it to the same `telemHandler` used by the IPC link's
`MsgTelemetry` path. The downstream telemetry decoder doesn't
distinguish SITL from real radio.

Resync behavior:
- Lead byte not 0xC8 (or 0xEA, the radio-transmitter address used
  by some firmware): skip one byte, retry alignment.
- LEN out of plausible range (< 2 or > 62): same.
- Bad CRC: skip one byte, retry.

Incomplete frames are held in the read buffer until more bytes
arrive.

## SITL-specific considerations

- **No arm key**: the RP2040 panel arm key is absent. The arm
  state machine initializes "key UP" in SITL mode; operator triggers
  arming via the API/GUI.
- **No mushroom button**: hardware emergency cut is unavailable.
  Use the daemon's Disarm API (or kill the daemon).
- **INAV prearm checks**: SITL still enforces them. Sensors must
  be set to FAKE; throttle channel must be at low; AUX channel for
  ARM must be configured. See [`howto/bench-test-sitl.md`](../howto/bench-test-sitl.md).

## Implementation reference

`pi/daemon/internal/sitl/sitl.go`. Tests in `sitl_test.go` cover
frame shape, channel pack round-trip, CRC, and drainFrames
behavior.
