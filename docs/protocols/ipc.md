# IPC Protocol — Daemon ↔ RP2040

Wire format and message catalog for the daemon-to-MCU link. Owns
channel intent (Pi → MCU) and telemetry/log/input upstream events
(MCU → Pi).

## Transport

USB-CDC. The Mega IO board and the ESP32 panel also enumerate as
`/dev/ttyACM*` devices; identify the RP2040 by its USB VID:PID
`2e8a:000a` (Raspberry Pi Pico). Use `/dev/serial/by-id/` paths
in production:

```
/dev/serial/by-id/usb-Raspberry_Pi_Pico_<serial>-if00
```

Baud rate is irrelevant on USB-CDC but the daemon defaults to 115200
for tooling compatibility.

## Framing

COBS (Consistent Overhead Byte Stuffing) with `0x00` as the frame
delimiter. Each frame ends with a single `0x00` byte. Inside the
COBS-encoded data, no `0x00` bytes appear, so parser resync after
corruption is trivial: skip until the next `0x00`, decode what
follows.

Inner frame layout (after COBS decode):

```
+------+------+----------+----------+----- ... -----+----------+----------+
| TYPE | SEQ  | LEN_LO   | LEN_HI   | PAYLOAD       | CRC_LO   | CRC_HI   |
+------+------+----------+----------+----- ... -----+----------+----------+
   1B    1B    2B (LE)                  LEN bytes      2B (LE, CRC-16)
```

Header is 4 bytes; LEN is little-endian uint16 (max 256). CRC is
CRC-16/CCITT-FALSE computed over `TYPE..PAYLOAD` (everything before
the CRC bytes).

Sizing constants (must agree on both sides):

| Constant | Value |
|---|---|
| `MaxPayload` | 256 |
| `MaxFrameRaw` | 4 + payload + 2 = 262 |
| `MaxFrameCOBS` | raw + 2 + 1 (overhead + delimiter) |

Frames with payload length > `MaxPayload` are rejected at compose
time. Frames with mismatched LEN or bad CRC are dropped with a log
line; the parser stays in sync via the COBS delimiter.

## Versioning

The first frame of each session is `MsgHello`, exchanged in both
directions. Payload:

```
[proto:1][reserved:3][version_str:N]
```

Where `proto` is the protocol version (current: 3) and
`version_str` is a free-form ASCII identifier of the local build
(e.g. `"zerotxd 0.5.2"` or `"zerotx-fw m1.7-armkey"`). The daemon
logs both versions on connect.

| Version | Adds |
|---|---|
| 1 | Channel intent, input state, heartbeat |
| 2 | `MsgTelemetry` (CRSF passthrough) |
| 3 (current) | `MsgInputEvent` (panel button edges, used by arm state machine) |

Compat policy: new firmware against old daemon → unknown frames
silently dropped. Old firmware against new daemon → daemon misses
features that need higher-version frames but stays usable (falls
back to manual confirmations where automatic checks are missing).

## Message catalog

| Type | Direction | Rate | Name | Payload |
|---|---|---|---|---|
| `0x01` | Pi → MCU | 50Hz | `MsgChannelIntent` | 32 bytes (16 × uint16 LE, channel values) |
| `0x02` | MCU → Pi | 50Hz | `MsgInputState` | (M1 reserved, currently empty) |
| `0x03` | both | 5-10Hz | `MsgHeartbeat` | 1 byte (sequence number) |
| `0x05` | MCU → Pi | event | `MsgInputEvent` | 2 bytes: `[input_id][state]` |
| `0x10` | both | once | `MsgHello` | `[proto:1][reserved:3][version_str:N]` |
| `0x11` | both | once | `MsgHelloAck` | same as `MsgHello` |
| `0x12` | MCU → Pi | event | `MsgTelemetry` | full CRSF frame (addr + length + type + payload + CRC) |
| `0x14` | MCU → Pi | event | `MsgLog` | ASCII string (no terminator) |

Reserved input IDs for `MsgInputEvent`:

| ID | Meaning |
|---|---|
| `0x01` | Arm key |
| `0x02` | Confirm button |
| `0x03` | Mushroom button (also wired to ELRS power-cut) |

State byte: `0x00` = released/down, `0x01` = pressed/up. The MCU
debounces and only emits on stable edges.

## Channel encoding

`MsgChannelIntent` payload is 16 little-endian uint16 values, one
per CRSF channel slot. Range follows CRSF conventions:

| Constant | Value | Approx µs |
|---|---|---|
| `CrsfChMin` | 172 | 988 |
| `CrsfChMid` | 992 | 1500 |
| `CrsfChMax` | 1811 | 2012 |

The daemon enforces the range; out-of-range values are clamped
before send. The MCU re-packs these into the standard 11-bit packed
CRSF `RC_CHANNELS_PACKED` frame for the radio link.

## Heartbeat and failsafe

The daemon sends `MsgHeartbeat` every `HeartbeatTxPeriodMs` (100 ms).
The MCU watchdog declares HOLD if `HeartbeatRxTimeoutMs` (200 ms)
passes without a heartbeat or channel intent frame.

| State | Trigger | Effect |
|---|---|---|
| LINK_OK | recent heartbeat or intent | normal operation |
| HOLD | 200 ms gap | re-emit last channel snapshot; daemon may have hiccupped |
| FAILSAFE | 600 ms gap (HOLD + 400) | emit configured failsafe channels (typically arm channel low + sticks centered) |

The aircraft-side CRSF + INAV failsafe is independent of this; ELRS
declares link-loss based on its own UART timeout.

## Telemetry forwarding

`MsgTelemetry` carries a complete CRSF frame as received from the
ELRS module's UART back-channel. The MCU strips no bytes; the daemon
parses the frame the same way INAV would. Frame rate matches the
FC's telemetry rate (typically 4-30 Hz depending on the ELRS rate).

The daemon forwards these to:
- `internal/telemetry` for snapshot decoding
- `internal/crsftee` for the mwp tee (after stripping to
  `[addr][type][payload]` for symmetry with the SITL path)

## Log forwarding

`MsgLog` carries human-readable strings from the MCU. The daemon
logs them with a `[mcu]` prefix. Used for debugging, state-machine
transitions, error reports. Not used for any operational decisions.

## Sequence numbering

The `SEQ` byte in the frame header increments per message of the
same type. It's primarily a debugging aid; the daemon and MCU
don't enforce ordering or detect drops via SEQ.

## Implementation references

- Daemon: `pi/daemon/internal/ipc/`
  - `protocol.go` — constants, message types, version
  - `framing.go` — COBS, CRC, BuildFrame, ParseFrame
  - `link.go` — Link type, callbacks, Run loop
- Firmware: `rp2040/src/`
  - `proto.c` — frame composition + parsing
  - `state.c` — state machine driving HOLD/FAILSAFE
