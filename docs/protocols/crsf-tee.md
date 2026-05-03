# CRSF Tee — Daemon → mwp

Read-only telemetry tee. The daemon listens on a TCP port and
retransmits incoming CRSF telemetry to whatever connects (typically
mwp). One direction only: daemon emits, clients consume.

## Transport

TCP. Address configured via `-mwp-tee-addr`; default
`tcp://127.0.0.1:5761`. Empty string disables the tee entirely.

mwp connects with protocol set to "CRSF" and address pointing at
the tee. No special handshake; mwp just starts reading bytes.

## Framing

Standard CRSF, identical to what the radio link produces. Each
telemetry frame from the FC is reconstructed and forwarded in full:

```
+------+------+------+--------------+------+
| ADDR | LEN  | TYPE | PAYLOAD      | CRC8 |
+------+------+------+--------------+------+
```

The IPC link's `MsgTelemetry` carries a stripped form
(`[addr][type][payload]`). The tee re-adds LEN and computes CRC8
on each forward, producing wire-byte-identical frames to what mwp
would see from a USB radio.

## Direction

Daemon → clients only. The tee deliberately does NOT accept inbound
frames. Mission upload from mwp would need to coexist with the
daemon's channel intent loop's CRSF ownership; that's a different
design problem and is deferred.

## Multiple clients

The tee supports multiple connected clients simultaneously. Each
connection gets the same fan-out stream. Slow consumers don't block
fast ones; per-client write errors close that one connection
without affecting others.

## Lifecycle

- Daemon startup: tee listens immediately if `-mwp-tee-addr` is
  set.
- Per client: `Accept` → forward stream → close on read EOF or
  write error.
- Daemon shutdown: listener closes; in-flight writes are dropped.

## Failure modes

- Tee port already in use: daemon exits at startup with a clear
  error.
- Client connects but never reads: TCP write buffer fills, write
  errors trigger close. Daemon doesn't block.
- No upstream telemetry (FC silent): tee is just a listener with
  nothing to forward. Clients see an open connection with zero
  bytes.

## Why CRSF, not MSP

mwp supports both. CRSF is what the link actually carries; using
it means no protocol translation in the daemon, no impedance
mismatch on telemetry rates, and mwp's CRSF decoder handles the
same frame types the daemon already parses.

## Implementation reference

`pi/daemon/internal/crsftee/crsftee.go`. The `Tee` struct holds the
listener and a fan-out registry. Forward() rebuilds the full CRSF
frame from a stripped IPC payload (or SITL's already-stripped form)
and broadcasts to all connected clients.
