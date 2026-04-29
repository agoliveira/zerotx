# Safety architecture

This document captures decisions made for safety reasons that are not obvious
from reading the code. If you are about to change something in this list,
read this first and understand why it is the way it is.

## Failsafe chain

The end-to-end failsafe path on a normal flight is:

```
Pi daemon stops sending intents
    ↓ ~200ms
RP2040 watchdog notices, stops emitting fresh CRSF
    ↓ ~600ms
ELRS module sees no fresh data, declares link down
    ↓ ~150ms
FC (INAV) failsafe triggers, executes configured behavior (RTH, land, hold)
```

Total: roughly 950 ms from "Pi-side daemon goes quiet" to "FC takes over."

This chain is the foundation of every other safety behavior. The daemon
intentionally goes silent (rather than emitting last-known forever) when it
detects loss of joystick input, because going silent is what makes the chain
fire.

**Configuration responsibility:** the FC (INAV) must be configured for the
desired failsafe behavior. ZeroTX cannot guarantee what happens after the
ELRS link drops; that is the FC's responsibility. RTH should be tested on
the bench before every airframe's first flight.

## Override custom function chain (Big Talon and similar)

The Big Talon model uses a custom function that pins CH1 (throttle) to -100%
whenever the airplane is not armed:

```
CF: !L3 → OVERRIDE_CHANNEL 0,-100,1
```

L3 is the sticky arm latch. While not armed, throttle output is forced to
the minimum regardless of stick position. This means that if the operator
moves the throttle stick high before completing the arming sequence, nothing
happens; the override holds the channel at minimum.

**Do not remove or weaken this CF.** It is the only thing standing between
"throttle stick at max" and "props spinning at max" on the ground.

## Arming chain (Big Talon)

Arming requires three conditions in sequence:

```
L1: VNEG I0,-99 andsw=SF2     (throttle low + SF in down position)
L2: EDGE SH2,0,10 andsw=L1    (SH momentary press, ≤1s, while L1 true)
L3: STICKY L2,L4              (latches when L2 fires; clears when L4 fires)
L4: VNEG I0,-99 andsw=SF0     (throttle low + SF in up position; disarm)
```

Result: arming requires throttle low + arming switch in correct position +
deliberate momentary press of SH. Disarming requires throttle low + arming
switch flipped to opposite position. No accidental arms or disarms from
single-stick movements.

## IDLE / READY state machine

The daemon has two states:

- **IDLE:** alive, API works, no model loaded, **no CRSF emission.**
  RP2040 sees nothing; FC failsafe is in effect.
- **READY:** model loaded, joystick optionally selected, CRSF flowing.

Boot defaults to IDLE unless `-model PATH` is passed on the command line.
The pre-flight workflow requires explicit operator action (loading a model
via the API or GUI) to leave IDLE. This means a freshly booted daemon
cannot accidentally drive the airplane.

The transition is implemented via `atomic.Pointer[Stack]`. Loading a model
stores a non-nil pointer; unloading stores nil. The tick loop reads the
pointer each tick, lock-free.

## Joystick disconnect behavior

When SDL reports `JOYDEVICEREMOVED` for the active joystick, the daemon:

1. Marks the reader disconnected (`Connected() = false`)
2. Continues emitting last-known channel values for **500 ms**
3. After 500 ms, stops emission entirely (failsafe chain takes over)

The 500 ms hold window is intentional. Short USB glitches (cable jiggle,
brief contact loss) shouldn't immediately throw the airplane into failsafe;
that's worse than holding for half a second. But longer outages must
fail safely, and that means going silent so the FC's RTH activates.

## Joystick reattach (same device)

When SDL reports `JOYDEVICEADDED`, the daemon checks the new device's GUID
against the currently-installed-but-disconnected reader. If they match, it
reopens the device transparently and resumes emission. Different GUID is
ignored (the operator must select it explicitly via the API/GUI; this is a
new device, not a reconnection).

This applies even during armed flight. Reattaching the same device is not
a "swap" — it's the same controller returning. The "no swap during flight"
rule (below) does not block it.

## No mid-flight controller swap

Once flight is armed (signaled to the daemon via `POST /api/v1/flight/arm
{"armed":true}`), the joystick endpoints reject swap requests unless the
caller passes `emergency=true`. This guarantees that a routine API call
or GUI misclick cannot change which controller is flying the airplane.

The `emergency=true` bypass exists for the case where the primary
controller dies and the operator deliberately switches to a fallback. The
GUI should expose this only behind an explicit confirmation prompt.

This rule is advisory in the current implementation: the daemon trusts the
GUI/operator to set `flightArmed` correctly. Future work will tie this to
telemetry-confirmed arming state from the FC.

## Things deliberately NOT done

- **No automatic model swap mid-flight.** The model is loaded in pre-flight
  and is fixed for the duration of the flight. Stack reload while armed is
  technically allowed by the code but conceptually forbidden by this rule
- **No persistent state across daemon restarts.** Every power-on starts
  IDLE. There is no "remember last session and resume" behavior; the
  operator must explicitly select model and joystick after every boot.
  This is intentional: stale state pretending to be current is more
  dangerous than starting fresh
- **No keyboard or mouse fallback for flight control.** Both are bad ideas
  (digital input, jerky, easy to misclick mid-emergency). FC failsafe RTH
  is the correct fallback for "primary input has died."
