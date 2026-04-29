# IPC Protocol (skeleton, filled in M1)

## Pi <-> RP2040 over USB-CDC

Framing: COBS (Consistent Overhead Byte Stuffing). Each frame ends with `0x00`. COBS guarantees no `0x00` bytes inside the encoded payload, so resync is trivial after corruption.

Frame layout after COBS decode:

```
+------+------+------+------+----- ... -----+------+------+
| MSG  | SEQ  | LEN  | LEN  | PAYLOAD       | CRC  | CRC  |
| TYPE |      | LO   | HI   |               | LO   | HI   |
+------+------+------+------+----- ... -----+------+------+
   1B    1B    2B (LE)        LEN bytes        2B (CCITT)
```

CRC-16/CCITT-FALSE over MSG..PAYLOAD.

## Message types

Hot path (fixed-layout C struct payloads, packed):

| MSG  | Direction      | Rate    | Name              |
|------|---------------|---------|-------------------|
| 0x01 | Pi -> RP2040  | 50Hz    | CHANNEL_INTENT    |
| 0x02 | RP2040 -> Pi  | 50Hz    | INPUT_STATE       |
| 0x03 | both          | 5Hz     | HEARTBEAT         |
| 0x04 | RP2040 -> Pi  | event   | TELEMETRY_RAW     |

Slow path (MessagePack payloads):

| MSG  | Direction      | Name              |
|------|---------------|-------------------|
| 0x10 | Pi -> RP2040  | CONFIG_PUSH       |
| 0x11 | RP2040 -> Pi  | CONFIG_ACK        |
| 0x12 | Pi -> RP2040  | PARAM_SET         |
| 0x13 | RP2040 -> Pi  | PARAM_RESULT      |
| 0x14 | RP2040 -> Pi  | LOG               |
| 0x15 | Pi -> RP2040  | TX_POWER_TEST     |

## Heartbeat

- Pi -> RP2040 every 200ms. Missing 1 = warning. Missing 2 = degraded (RP2040 floors safe defaults).
- RP2040 -> Pi every 200ms. Missing 2 = GUI shows "RP2040 link lost" banner.

## Safe-default state (RP2040 emits when Pi heartbeat lost)

- All stick channels: midpoint (1500us equivalent in CRSF terms)
- Throttle channel: minimum (988us)
- Arm channel: low (988us)
- All other aux channels: last commanded value (pinned)

Rationale: Pi loss does not require an immediate flight-mode change; only inputs go to safe state. The aircraft keeps flying its last commanded mode but with neutral sticks and zero throttle. INAV will likely auto-disarm on idle throttle, or pilot can manually trigger module power off to force ELRS failsafe -> RTH.
