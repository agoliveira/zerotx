# VFD Protocol — Daemon → Pro Micro

Wire format for the daemon-to-VFD link. Drives the Noritake
CU20025ECPB-W1J 2x20 character VFD via the Pro Micro firmware in
`firmware/vfd/`. Single direction: daemon sends, MCU renders.

## Transport

USB-CDC. The Pro Micro enumerates as `/dev/ttyACM*` with USB
VID:PID `1b4f:9206`. Use the by-id path:

```
/dev/serial/by-id/usb-SparkFun_SparkFun_Pro_Micro-if00
```

Baud is irrelevant on USB-CDC; daemon defaults to 115200.

## Framing

ASCII, line-delimited. One command per `\n`. No CRC, no
acknowledgments. Robustness comes from:

- Idempotent commands (re-sending `L0 ...` is safe)
- Tolerance for unknown commands (firmware silently ignores them)
- Line-buffering with overflow recovery (firmware drops a too-long
  line and resyncs on the next `\n`)

`\r` is tolerated and ignored. Lines longer than the firmware's
internal buffer (currently 64 bytes) are truncated and resynced.

## Command catalog

| Command | Effect |
|---|---|
| `L<row> <content>` | Write `<content>` to row 0 (top) or 1 (bottom). Pad/truncate to 20 cols. Pauses animation for 2s, then resumes. |
| `C` | Clear display, return to AMBIENT mode. |
| `B <level>` | Brightness 0..3 (0 = max, 3 = 25%). |
| `V` | Show firmware version banner. |
| `E <kind> [args]` | Animation event. See below. |

Unknown first-byte commands are silently ignored (forward
compatibility).

### Animation events

The firmware owns animation state. The daemon emits semantic
events and the firmware decides how to render them. Events drive
mode transitions and visual elements (activity bar, sweeps, alarm
flashes).

| Event | Effect |
|---|---|
| `E tick [n]` | n CRSF frames in the last sample window (default 1). Feeds the activity bar. Daemon batches at 10Hz. |
| `E arm 0\|1` | Arm state edge. Triggers a sweep across both rows (~800 ms). |
| `E mode <text>` | Flight mode change. Brief pulse with the mode name. Cached for display in AMBIENT/ARMED rows. |
| `E lq <pct>` | Link quality 0..100. Cached; displayed alongside mode. Daemon emits on edge change at ≤1Hz. |
| `E batt <text>` | Battery voltage as text (e.g. `14.2V`). Cached. Daemon emits on >0.1V change at ≤1Hz. |
| `E warn` | Warning alarm flash (slow blink, ~200ms period). |
| `E critical` | Critical alarm flash (~120ms period). |
| `E failsafe` | Failsafe alarm flash (~80ms period). |
| `E disarmed` | Edge: returning to ambient mode. |

Unknown event kinds are silently ignored (forward compatibility).

## Animation state machine (firmware-side)

| Mode | Trigger | Visual |
|---|---|---|
| BANNER | boot | "ZEROTX VFD / fw X.Y.Z awaiting" |
| IDLE | 6s without events | Single dot orbits the perimeter |
| AMBIENT | events flowing, not armed | Top: mode + LQ; Bottom: activity bar |
| ARMED | `E arm 1` | Top: heartbeat dot + mode; Bottom: bar (more vivid) |
| TEXT | L command received | Operator-pushed line held for 2s |
| EVENT | arm/disarm/alarm transitions | Sweeps or flashes for ~800ms |

Render loop: 30 fps. CGRAM holds 8 user glyphs (5 partial-fill bar
widths plus dot/blob/tick); set once at boot, not changed at
runtime.

## Daemon emitter

Two parallel feeds drive the VFD from the daemon side:

- **Firehose** (`internal/vfd/firehose.go`): subscribes to the
  daemon's log buffer, scrolls each new line as `L0`/`L1` text
  overlays at 5Hz.
- **Event emitter** (`cmd/zerotxd/vfd_events.go`): translates
  state changes into `E ...` commands. Emits `tick` at 10Hz
  (batched count of CRSF frames received), and `mode`/`lq`/`batt`
  on edge change at 1Hz. Emits `arm` on the arm state callback.

Both feeds share the same `vfd.Driver` and serialize their writes
internally.

## Implementation references

- Daemon: `pi/daemon/internal/vfd/vfd.go` (driver interface +
  Null/Log/Serial backends), `pi/daemon/cmd/zerotxd/vfd_events.go`
  (event emitter)
- Firmware: `firmware/vfd/src/main.cpp`
