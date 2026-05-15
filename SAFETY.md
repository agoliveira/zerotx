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

## Telemetry

The RP2040 firmware reads CRSF telemetry frames from the ELRS module
(UART RX), validates the CRC, and forwards them as `MSG_TELEMETRY` IPC
messages to the daemon. The daemon's `internal/telemetry` package
parses GPS, Battery, Link, and Flight Mode frames into typed state.

The MCU does not parse telemetry payloads — only CRC validation and
framing. New sensor types are added entirely in Go without firmware
changes.

The telemetry path is **purely advisory** — never on the flight-critical
path. Telemetry parsing runs on the IPC dispatcher goroutine and can
fail or stall without affecting channel intent emission. The daemon
runs identically with no telemetry at all (operator chose to fly
without it, or the radio link doesn't carry it). Auto-verifiable
checklist items fall back to manual confirmations when no telemetry
is available.

Per-sensor stale windows: GPS 2s, Battery 5s, Link 1s, Flight Mode 30s.
Stale data is still served (last-known often beats nothing) with a
`stale: true` flag for the GUI to interpret.

Cell count is detected heuristically from initial battery voltage
(`ceil(volts / 4.2)`) on first telemetry frame and cached for the
session. Wrong for partially discharged packs at first connection —
documented limitation, not a bug.

## Audio

PLAY_TRACK and PLAY_SOUND custom functions in the model are evaluated
on rising-edge transitions and emit events on the CF processor's Audio
channel. A per-stack drain goroutine forwards events to a Player which
shells out to `paplay` (PulseAudio/PipeWire) or `aplay` (ALSA), playing
samples one at a time from a configurable directory.

The audio path is **purely advisory** — never on the flight-critical
path. Audio playback runs on its own goroutine, the queue is bounded
(default 16 events) with overflow drops logged but not retried, and
each playback has a 30s timeout in case the audio backend wedges. None
of this can affect channel intent emission to the RP2040.

Samples are looked up at `<sounds-dir>/<lang>/<name>.<ext>` with a
fallback to `<sounds-dir>/<name>.<ext>` for language-neutral sounds.
Default extensions tried are `.wav`, `.ogg`, `.mp3`. EdgeTX SD-card
layouts work directly without modification.

If no audio backend is available (no paplay or aplay on PATH), the
daemon logs a warning at startup and uses a NullPlayer that drops all
events. Daemon stays useful but silent.

### Priority levels

Each event has a Level (info / notice / warning / critical) derived by
pattern-matching the track name (e.g. `armed.*` → critical, `*low*` →
warning, `fm-*` → notice). The Player has a Threshold; events strictly
below threshold are dropped (logged at debug level). **Critical events
ignore the threshold and always play**, unless the whole audio system
is disabled (`-no-audio`).

The threshold is set via `-audio-threshold info|notice|warning|critical`
at startup (default: `notice` — info dropped, rest plays). Operators
can change it at runtime via `POST /api/v1/audio/threshold`. No file
persistence: restart resets to the flag default.

### Repeating alarms

Warning and critical events schedule periodic re-plays. Defaults:

- Critical: every 5s, indefinite, until acknowledged
- Warning: every 30s, max 3 cycles, dismissable
- Notice/info: never repeat

Same alarm name re-firing resets its timer (debounce). Operators
acknowledge via `POST /api/v1/audio/acknowledge` (per-name or `all:
true`). The post-arm GUI surfaces active alarms with dismiss buttons.
**Disarming the flight auto-acknowledges all active alarms** so the
post-flight environment isn't still beeping.

## Sound bank

The audio package looks up sample files at `<sounds-dir>/<bank>/<name>.<ext>`
with a fallback to `<sounds-dir>/<name>.<ext>`. Banks are named voice/style
collections; the daemon's `-sounds-lang` flag selects which bank plays.

The public dictionary ships with two banks (`en` and `pt`) and a generic FPV
vocabulary covering alarms, flight modes, building blocks (numbers, units,
words), and sentence fragments for narrative announcements. Operators add
custom banks (cloned voices, mood variants, etc.) via `sounds/personal.yml`,
which is gitignored. The build script merges the personal config over the
public dictionary at synthesis time.

The audio bank is generated from `sounds/dictionary.yml` by
`scripts/build-sounds.sh`, which uses **edge-tts** by default. The
synthesizer is swappable — operators replace one function in the build
script to use ElevenLabs, Piper, eSpeak-NG, or any other tool that produces
mono audio at 22 kHz or higher. Generated audio is **not committed to the
repo**; contributors regenerate on demand.

When whole-phrase lookup fails for a track name, the audio package falls
back to **stitching**: a curated decomposition map (in
`audio/decompositions.go`) splits compound names like `bat-low` into
fragments (`w-battery`, `low`) that are played in sequence with an 80ms
inter-fragment gap. Partial decompositions degrade gracefully — missing
fragments log a warning and the surrounding sequence still plays.

**Hand-recorded overrides** at `sounds/overrides/<bank>/<name>.{mp3,wav,ogg}`
are copied over the generated audio at build time and always win. This lets
the operator replace specific tracks with their own voice (for safety-
critical clips like `armed`, `failsafe`) without regenerating the whole bank.

The audio bank is **purely advisory**. Missing or corrupt audio files produce
a one-time log warning per file and silence for that event; nothing in the
flight-critical path depends on audio playback succeeding. The choice of
default synthesizer is documented in `sounds/README.md` along with
alternatives for contributors who need different licensing or quality
posture.

## Recording

Each arm-to-disarm flight is captured as a SQLite database for
post-flight analysis. The database lives in memory while the flight
is in progress; on disarm it is copied to disk via `VACUUM INTO`.

The recorder runs **off the flight-critical path** entirely:

- Audio events are forwarded to it via the same drain that feeds the
  player. A failed insert logs and continues; the player still plays.
- Telemetry samples are pulled by a 5Hz sampler goroutine that polls
  the telemetry state and forwards the snapshot. The sampler shares
  no locks with the channel emission path.
- All inserts happen on a single SQLite connection so concurrent
  writes serialise; there is no contention with the daemon's tick
  loop.

**Power loss between arm and disarm = lost recording.** This is by
design: writing to the SD card during flight risks FS corruption
under power-cut scenarios, and tmpfs writes are effectively free.
The trade-off is that a complete loss of power on the Pi mid-flight
loses the in-memory recording. An external 12V SLA UPS upstream of the
case input is the proper redundancy.

Cell count detected by the telemetry layer (heuristic from initial
voltage) is implicit in the recorded battery rows: the recording
captures `bat_volts` directly without an explicit cells field, so
post-flight analysis can derive per-cell voltage as
`bat_volts / cell_count` using the cell count from session metadata
or external knowledge of the airframe.

GPS coordinates are recorded at full 7-decimal precision. Sharing a
recording shares your flight path. There is no per-recording redaction
toolchain yet; an operator who needs to publish sanitised recordings
should script that separately.

Cleanup keeps the 10 most recent recordings; older files are deleted
on each save. Configurable via `-keep-recordings`.

## HUD (post-arm flight UI)

When the operator arms the flight, the GUI takes over the full window
with a dedicated flight UI. The pre-flight tab, sidebar, and header
chrome are hidden via a CSS class swap on the root container; the HUD
overlay becomes visible.

**The HUD is purely a display.** It reads from the same WebSocket
stream as every other tab. There is no separate flight data path. If
the WebSocket disconnects mid-flight, the daemon keeps emitting
channel intents to the RP2040 unaffected — only the GUI's view of the
state goes stale.

Three large tiles (battery, GPS, link) show critical state with
color-coded primary numbers:

- Green: nominal
- Amber: warning (per-cell V 3.5–3.7, GPS sats 4–5, link LQ 50–69%)
- Red: critical (per-cell V < 3.5, GPS sats < 4, link LQ < 50%)
- Stale: when the sensor's data hasn't been refreshed within the
  staleness window, the tile dims and a "STALE" badge appears

When at least one warning alarm is active, the entire HUD gets an
amber border. When at least one critical alarm is active, the border
goes red. This is a peripheral signal independent of the alarms
panel itself, so it's visible even when the operator isn't looking
directly at the screen.

The DISARM button is intentionally large and unmissable. It honors
the `confirmDisarm` setting: if confirmation is enabled, a modal
appears; if disabled, one tap disarms immediately. The big-button
design assumes operators may be using the radio in one hand while
tapping with the other; precision tap targets are unsafe at the field.

On disarm, the post-flight summary overlay appears showing aggregate
stats from the just-saved recording. The operator dismisses it with
the "Done" button to return to the pre-flight tab. The same summary
view is reachable later via the per-row "Summary" action in the
Recordings tab.

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
