# ZeroTX User Manual

## Front matter

**Audience.** You at the box, in the field or on the bench, wanting it to work. You have built the case (or were handed one already built) and are now operating it day to day: cold-starting, pre-flighting, flying, post-flighting, packing it up. Recovery procedures cover the failures you can fix in the field; deeper build-time issues are referred to the Builder's Manual.

**Scope.** Daily operations: cold start through shutdown, with run-time troubleshooting. This manual assumes the system has been built and provisioned correctly per the Builder's Manual; it doesn't re-explain wiring or firmware flashing. Material that requires opening the case or reflashing firmware is build-time territory and lives in `docs/manuals/BUILDER.md`.

**Conventions.**
- `<user>` is the username chosen at Pi provisioning. Substitute your actual username.
- Commands prefixed with `$` run as the daemon's user; `sudo` is shown explicitly when needed.
- "The Pi" means the Raspberry Pi 400 inside the case. "The case" means the ZeroTX ground station as a whole.
- Pre-flight, in-flight, and post-flight are not strict modes the daemon enforces; they're phases of the operator's workflow. The daemon enforces a narrower set of gates (the syscheck gate to "Proceed to flight"; the arm state machine for ARM/DISARM); the rest of this manual's structure is for human navigation.

## Table of contents

1. System overview (operator-level)
2. Cold start
3. Pre-flight
4. In-flight
5. Post-flight
6. Field operations
7. Recovery procedures
8. Run-time troubleshooting
   - Appendix A: Daemon flag quick reference
   - Appendix B: Audio cues catalog
   - Appendix C: Glossary
   - Appendix D: Changelog

## 1. System overview (operator-level)

ZeroTX is a portable FPV ground station that replaces a handheld transmitter with a desktop setup: a Raspberry Pi 400 brain, four MCU satellites (RP2040 for the CRSF radio link, Mega 2560 for IO peripherals, ESP32 for the LED panel, optional ESP32-S3 for an antenna tracker), and a Thrustmaster-style joystick for control. Output goes to an ExpressLRS module via CRSF; telemetry comes back the same way.

This section is the operator's mental model. For the architectural depth (signal paths, failsafe timing, power tree), see the Builder's Manual Section 1.

### 1.1 What you'll see when it's running

Five visible surfaces convey state. From most-to-least-glanced:

| Surface | What it tells you |
|---|---|
| **HUD LCD** (left display by default) | Live telemetry: airspeed, attitude, altitude, battery, link quality, GPS, flight mode |
| **Map LCD** (right display by default) | Aircraft position, track line, home, range circles, weather overlay |
| **HUB75 panel** | Current high-level mode: IDLE, PREFLIGHT, FLIGHT, ALARM, RTH. Visible across the field. |
| **VFD** (above the panel) | Daemon status text, scrolling alerts, system uptime |
| **Audio** | Narration of mode changes, alarms, weather alerts (English or Portuguese) |

Plus two indicators on the case itself:
- **ELRS module LED** (visible through the case panel): RF transmit state
- **Heartbeat LED** (Pi GPIO breakout, if fitted): 1 Hz blink while the daemon's mapper loop is healthy

### 1.2 What you control

| Control | What it does |
|---|---|
| **Joystick** | Four axes (typically aileron, elevator, throttle, rudder) plus arm key (SF-style switch) and momentary push-button (SH-style; the "arm confirm" press) |
| **Panel buttons** (Mega-driven) | Mode toggles, brightness, mute, configurable per model |
| **Rotary encoder** (Mega-driven) | Menu navigation, value scrubbing |
| **Kiosks** (HUD and Map, via USB keyboard if connected, or via the case's front-panel USB-A) | Status page interactions, "Proceed to flight" gate, the keyboard arm confirm (Ctrl+Alt+A) |
| **E-stop mushroom** | Emergency cut to the ELRS module's 12V supply. Hardware-only; software cannot override. |
| **Keylock** | Master power. ON powers the case; OFF cuts everything. |

The joystick is the only primary flight control. Buttons and encoder are auxiliary (operator interface, not stick-replacement).

### 1.3 The failsafe chain

If anything in the chain from your joystick to the aircraft breaks, the aircraft enters its own failsafe within ~950 ms. The chain you should understand at the operator level:

1. **Joystick disconnected** → daemon detects within ~100 ms, switches to safe channel defaults.
2. **Daemon dies or hangs** → RP2040 stops getting heartbeats, holds last channels for ~600 ms, then stops emitting CRSF entirely.
3. **CRSF stops** → ELRS module's RX side sees no input for ~150 ms, the aircraft's receiver sees RX_LOSS, the FC engages its configured failsafe behavior (RTH, level, drop, etc., per FC config).
4. **Cable disconnected** → ELRS gets no input regardless of case state; aircraft fails over the same way.
5. **E-stop pressed** → ELRS module loses power in <10 ms; aircraft sees RX_LOSS immediately.

Total worst-case time from joystick disconnect to aircraft-side failsafe: ~950 ms.

You don't manage this chain manually. It just runs. What you do manage:
- **Don't trip e-stop unless you mean it** (it's a hard cut; recovering takes seconds).
- **Don't yank the pole cable mid-flight** (cable disconnect triggers failsafe; the aircraft's own behavior decides what happens next).
- **Watch the RP2040 status LED** (green = OK; amber pulses or red blinking = the chain is in motion).

### 1.4 Where things live (operator side)

You generally don't open the case in the field. The few things you might touch from outside:

- **Rear panel:** 12V input jack, keylock, optional Ethernet, optional rear USB-A for SSH-via-keyboard-dongle.
- **Front panel:** joystick USB-A, status row (LEDs/buzzer/encoder), control row (buttons), HDMI displays.
- **Top panel:** e-stop mushroom, key, ELRS module antenna mount, pole cable bulkhead.

If you need to know which physical pin maps to which subsystem, see Builder's Manual Appendix A. If you need to swap cables or change wiring, that's a build-time concern requiring case disassembly, not a field procedure.

## 2. Cold start

The system is portable: every cold start happens at a fresh site (or at home after a teardown). This section is the standard cold-start sequence from "powered off and disconnected" to "ready to fly."

### 2.1 Pre-power checklist

Before applying power, confirm:

1. **12V source connected to the rear panel jack.** SLA battery + charger pack, bench supply, or vehicle 12V. The case is source-agnostic; what matters is it's actually 12V (not 13.8, not 11.5) and capable of the peak load (~6 A worst-case with all kiosks + audio + panel + MCUs running).
2. **E-stop released** (twist to release if pressed).
3. **Keylock OFF.**
4. **Pole cable connected** at both ends (case bulkhead, pole-end project box or ELRS module).
5. **Joystick plugged into the front-panel USB-A.** If you'll need a USB keyboard for SSH or kiosk interaction, also plug it in now (the daemon's hot-plug works, but pre-flight is simpler with everything connected before boot).
6. **Both HDMI displays connected and on.** If the displays auto-enter standby on no signal, that's fine; they'll wake when Pi 400 starts outputting.

### 2.2 Power on

1. Turn the keylock to ON.
2. The 12V rail comes up. Downstream 5V rails follow within milliseconds.
3. Audio amp may emit a brief power-on pop through the speaker. Normal.

What you should see in the first 30 seconds:

| Time | Visible event |
|---|---|
| t=0s | Voltmeter (if fitted) shows ~12.0V. Heartbeat LED dark. |
| t=1-3s | Pi 400 begins booting; SSD activity LED flickers. Both LCDs show Pi boot output (kernel messages on black). |
| t=4-8s | Pi reaches userspace. Brief tty1 shell, then `startx` launches; screens go black. |
| t=8-12s | Chromium starts on both displays. White loading screens. |
| t=10-15s | Kiosks land on the daemon's `/status` page. |
| t=2-5s (parallel) | MCU satellites enumerate. VFD shows boot banner. HUB75 panel shows "ZEROTX <version>" briefly, then IDLE clock. RP2040 status LED transitions through BOOT → PENDING (amber) → OK (green). |

### 2.3 What "ready" looks like

After ~30 seconds, the expected end state:

- Both LCDs show the daemon's `/status` page with green rows and an enabled "Proceed to flight" button at the bottom.
- VFD shows a status message and a clock (or scrolling text).
- HUB75 panel shows dim "ZEROTX" with the clock tick.
- RP2040 status LED: **green solid** (OK).
- Heartbeat LED (if fitted): blinking at 1 Hz.
- Audio: silent except for the optional boot chime (if your model configures one).

This is the state you start pre-flight from (Section 3).

### 2.4 If something's wrong at cold start

The fastest triage:

- **Both LCDs dark or showing Pi boot output stuck for >60 s** → Pi boot issue. SSH in if you can; otherwise see Section 8.1 (Pi won't reach kiosks).
- **One LCD dark, one OK** → HDMI cable, power, or kernel display config issue. Section 8.2.
- **Kiosks loaded but "Proceed to flight" is greyed out with a "Blocked by" hint** → Section 3.3 covers what each blocker means and how to clear it.
- **HUB75 panel dark or scrambled** → ESP32-side problem. Section 8.5.
- **VFD dark or showing katakana** → Mega-side or wiring. Section 8.6.
- **Audio amp pop never happened (silent at boot)** → power to amp, or speaker wiring. Section 8.7.
- **RP2040 LED stuck on amber** → daemon never connected to it. Section 8.8.

Take the time to triage cold-start issues before flying. A subsystem that's wrong at boot won't fix itself in flight.

## 3. Pre-flight

You're at "ready" state from Section 2. Pre-flight prepares for flight: aircraft side ready, syscheck gate cleared, joystick centered, audio confirmed, arm sequence validated.

### 3.1 Aircraft side

Before touching the ground station's "Proceed":

1. **ELRS TX module on the case is powered and paired with your aircraft.** Check the module's own indicator (binding state, transmit state per its firmware).
2. **Aircraft powered on.**
3. **Aircraft GPS lock acquired.** Check via the HUD's satellite count once telemetry starts flowing — should be ≥6 sats for a usable home position, ideally ≥10.

If you can't get aircraft GPS lock at the field, that's an aircraft-side issue (sky view, antenna, FC config) and not a ZeroTX problem.

### 3.2 Daemon launch

Under normal cold-start, the daemon is already running (systemd autostart). Confirm:

```
$ systemctl status zerotxd.service
```

Expected: `Active: active (running)`. If anything else, see Section 8.3 (Daemon won't start).

**Manual launch** for development or recovery only (not normal field operation):

```
$ cd ~/zerotx
$ ./bin/zerotxd -model configs/big_talon_zerotx.yml -v
```

The full flag set lives in Appendix A. Under field operation you use the systemd unit, which is already configured per Builder's Manual Section 6.15.

**Logs:**

- Under systemd: `journalctl -u zerotxd.service -f`
- Manual launch: stderr of the binary
- In-daemon log buffer: `curl http://127.0.0.1:8080/api/logs` (last N lines from `logbuf`)

### 3.3 Syscheck gate ("Proceed to flight")

On the `/status` page, both kiosks show:

- A list of subsystem readiness rows (RP2040 link OK, HDMI displays OK, etc.)
- Possibly a list of blockers beneath the Proceed button
- The "Proceed to flight" button itself (enabled or greyed out)

The daemon enforces two devices as hard requirements:

- **RP2040 link**: heartbeats every ~200 ms over USB-CDC. Down means the CRSF generator is silent and the aircraft will failsafe immediately on any pre-existing link. Without this, you cannot fly.
- **HDMI kiosk displays**: both micro-HDMI ports must report `connected` in `/sys/class/drm`. Down means one of your operator displays is unplugged. Without both, you don't have the operator interface you need to fly safely.

If either is down, "Proceed to flight" is disabled and the hint reads `Blocked by: device down: <name>`. Plug it back in, wait ~2 s for the page to re-poll, button enables.

If a **hardware baseline file** is deployed (see 3.4), additional blockers may appear with the format `hardware baseline: <probe> expected pass, got <actual> (<reason>)`. These come from the daemon's self-check comparing the deployed baseline to current device state.

**Proceed when ready:** click "Proceed to flight" on either kiosk. Both kiosks transition out of `/status`: HUD lands on `/hud`, Map lands on `/map`. Telemetry stream visible.

After the syscheck gate, the daemon doesn't gate flight further. Arm/disarm is your responsibility via the joystick (Section 3.6).

### 3.4 Hardware baseline (optional)

The bench-side `zerotx-bench` diagnostic tool (in `tools/zerotx-bench/`) can export a YAML snapshot of every probed device's state. When that file is deployed to `/etc/zerotx/hardware-baseline.yaml`, the daemon runs a self-check at startup: ~3 seconds after launch (enough for devhealth heartbeats to settle), it compares the baseline's pass-expected probes against its current view of each device and lists mismatches in the Preflight blockers.

You'd set this up once on the bench during build verification. Recapture only if you change hardware (replace a module, add a peripheral, swap a cable).

Capture workflow (bench, not field):

1. Stop the daemon: `sudo systemctl stop zerotxd`.
2. Run the bench tool: `tools/zerotx-bench/zerotx-bench`. Browse to `http://<pi-host>:8081`. Click through probes (or "Run all"), manually skip any probes that are intentionally absent.
3. Press "Export baseline". A modal pops up with the YAML and a Copy button; the file is also written to `./hardware-baseline.yaml`.
4. Deploy: `sudo install -D -m 644 hardware-baseline.yaml /etc/zerotx/hardware-baseline.yaml`.
5. Restart the daemon. Self-check picks up the file automatically.

Probes the daemon enforces today: `rp2040`, `mega`, `esp32-display`, `hdmi`, `gps-ublox`, `rtc-ds3231`, `led-heartbeat`, `joystick`, `audio`, `elrs`. Each daemon-side observer is honest about its limits — for example, the LED check verifies the daemon believes it's driving the GPIO line, not that the LED is physically lit; the audio check verifies the daemon resolved a playback backend for each configured extension, not that the speakers actually emit sound. The bench tool's probes are stronger in some cases (direct hardware exercise); the daemon's are weaker but continuous.

**To disable self-check entirely:** pass `-hardware-baseline ""` to the daemon (or remove the file). Settling delay tunable via `-hardware-baseline-settle DURATION`.

### 3.5 Joystick check

After Proceed, with kiosks on `/hud` and `/map`:

1. **Center the joystick.** All axes at their detents.
2. **Wiggle each stick** through full range. The HUD's input panel (or the channel monitor on `/api/v1/channels`) should reflect the movement.
3. **Verify no stuck axes.** If an axis appears moved when the stick is centered, there's a hardware calibration issue. Quick fix: replug the joystick (daemon re-binds and re-zeroes). If persistent, see Section 8.9.
4. **Audio output check.** Trigger any non-silent panel test, or just verify the cold-start chime played. Silent audio means the operator can't hear narration in flight, which removes a key feedback channel.

### 3.6 Pre-arm conditions and arming

The arm state machine requires **three simultaneous inputs** to transition to ARMED:

1. **Throttle low (T-low):** joystick throttle stick at minimum. The daemon reads the throttle channel from the active model file (TAER layout = channel 1 for Big Talon and most fixed-wing models).
2. **Arm key:** SF-style two-position switch held in the down ("arm requested") position. May be panel-mounted or on the joystick depending on your model config.
3. **Confirm press:** the panel-mounted SH momentary button (RP2040 GPIO 15) OR Ctrl+Alt+A in either kiosk browser. Press-only; releasing doesn't matter.

All three must be present at the same instant. Releasing any input after arming doesn't disarm — once you're armed, you're armed until the disarm handshake.

**Disarm:** arm key UP **combined with** T-low. The inverse handshake. The momentary confirm button is not required to disarm.

If arming fails, the audio narrator announces the specific failure ("throttle not low", "arm key not down", "not ready"). The HUD shows the current arm state and which precondition is missing. Fix the missing input and try the press again.

## 4. In-flight

After arming, the system is mostly hands-off. The daemon narrates mode transitions, alarms, and weather alerts via audio. The HUB75 panel reflects FLIGHT, ALARM, or RTH state at a glance. Map updates with the aircraft track. Recorder runs continuously.

**Flight mode changes are made via radio**, not via GCS. The FC owns mode state; the ground station reports it.

This section walks the surfaces in priority order.

### 4.1 Reading the HUD

The HUD is the primary in-flight display. Layout (varies slightly per model):

- **Center:** artificial horizon, with the aircraft attitude
- **Top-left:** airspeed
- **Top-right:** altitude (barometric and/or GPS)
- **Bottom-left:** battery (voltage, %, mAh consumed if FC reports it)
- **Bottom-right:** GPS state (satellites, fix type), home distance and bearing
- **Top-center:** flight mode (per FC: ANGLE, HORIZON, MANUAL, RTH, etc.)
- **Side bars:** link quality (RSSI, LQ, telemetry age), throttle position, mixer state

What to watch for:

- **Battery dropping faster than expected.** Triggers low-battery audio narration at configurable thresholds (default 30% then 15%). Adjust the thresholds in the model config if needed.
- **GPS satellite count dropping.** Below 6 sats means home position degrades; below fix-loss means the FC's own GPS-dependent modes (RTH, position hold) won't work.
- **Link quality dropping.** Telemetry age >2 s means the link is broken or marginal; the FC will failsafe on its own RX timeout.

### 4.2 Reading the Map

The Map shows aircraft position over offline (PMTiles) or online (proxy) tile imagery. Layout:

- **Aircraft icon** rendering current GPS position and heading
- **Track line** showing recent flight path
- **Home marker** at the configured site or aircraft home (set on first arm or by FC config)
- **Range circles** at configurable radii (1 km, 5 km, etc.)
- **Weather overlay** (if enabled): wind direction/speed, precipitation chance for the next interval

What to watch for:

- **Aircraft moving outside the planned range.** Range circles give visual cues.
- **Tile gaps** (white squares) if you're flying beyond the prefetched coverage. The `tilewarm` subsystem fetches in the background when online; gaps mean the area wasn't prefetched and you're offline. See Section 7.6.

### 4.3 Reading the HUB75 panel

The panel is a low-resolution, high-visibility status indicator visible across the field. Modes:

| Mode | Visual |
|---|---|
| IDLE | Dim "ZEROTX" centered, clock tick |
| PREFLIGHT | Status text + readiness indicators |
| FLIGHT | Bold flight-state display (mode, airspeed, time aloft) |
| ALARM | High-contrast warning text + color-coded by severity |
| RTH | Distinct "RTH" indicator visible from anywhere on the field |

The panel reflects the daemon's view of high-level state. It's not a primary display (don't fly off it); it's a glance-at-a-distance indicator for when you're past arm's-length from the HUD.

### 4.4 Reading the VFD

The VFD shows daemon status text: current alert, recent narration, system uptime, etc. Scrolls when text doesn't fit the 2x20 layout. Lower-priority than the panel and HUD; useful when you want a small numeric or text readout the panel doesn't render.

### 4.5 Audio cues and narration

The daemon narrates events via Piper TTS (default English; Portuguese alternative bank). What gets narrated:

| Event class | Examples |
|---|---|
| Mode transitions | "Armed", "Disarmed", "Manual", "Return to home" |
| Alarms (audio thresholds: info / notice / warning / critical) | Low battery, link loss, GPS lost, RTH triggered, weather alert |
| Periodic | Time aloft, battery percentage, current altitude (configurable via `-narrate-interval` and `-narrate-content`) |
| Weather (if enabled) | High gusts, near-sunset warning, wind shear, golden hour |

Default behavior: narration follows the `-audio-threshold` flag (`notice` by default). Critical events ignore the threshold and always play.

Full catalog of audio cues lives in Appendix B. The model YAML can also customize which events narrate; see Builder's Manual Appendix D.

To mute mid-flight (rare; usually you want narration): the panel-mounted mute button (if configured) toggles `-no-audio` behavior at runtime. Loses all narration including critical alarms until untuned; **don't fly muted by default**.

### 4.6 Operator inputs

| Input | Effect |
|---|---|
| Joystick axes | Channels (throttle, roll, pitch, yaw, etc., per model) |
| Joystick arm key + momentary | ARM state machine |
| Panel buttons | Model-defined; typically mode switches, brightness, mute |
| Rotary encoder | Menu navigation, value scrubbing |
| Ctrl+Alt+A in either kiosk (USB keyboard plugged into front-panel USB-A) | Software arm confirm equivalent to the SH press |
| E-stop mushroom | Hardware cut to ELRS module. Press to immediately drop the RF link. Twist-release to recover. |

The e-stop is the only input you should never press accidentally. Treat it like an emergency-only control.

### 4.7 What the recorder is doing

The daemon records every flight to `~/zerotx/recordings/flight-<timestamp>.db` (SQLite). What gets recorded:

- All received telemetry frames with timestamps
- All daemon-issued events (mode transitions, narration triggers, threshold crossings)
- Configuration snapshot (model, daemon flags, hardware baseline status)

You don't manage the recorder in flight. It just runs. Retention is the 10 most-recent recordings (configurable via `-keep-recordings`); older recordings auto-delete on save.

**To disable recording for a specific session** (testing, demos, throwaway flights): pass `-no-recordings` to the daemon launch — but under systemd autostart this means editing the unit and reloading, which you'd do on the bench, not in the field.

### 4.8 Antenna tracker behavior (optional, extended configuration only)

If your build includes the pole-end ESP32-S3 antenna tracker, it operates autonomously and the daemon is not involved. From your perspective in flight:

- **Tracker reports `tracking`** when receiving fresh GPS frames from the aircraft and computing pan/tilt. Servos move smoothly to follow the aircraft.
- **`hold`** when telemetry stops or stalls. Servos hold last commanded pose. This is the safe state; nothing damaging happens during hold.
- **`no-telem`** if the tracker has never seen telemetry since boot. Servos don't move from their power-on position. Indicates a failure earlier in the chain (aircraft GPS not locked, telemetry not bound, cable broken). See Section 7.5.

Tracker state is reported in the HUD's link panel. The tracker itself has no on-the-pole indicator visible from the operator position.

The tracker's behavior is autonomous and survives Pi reboots / daemon restarts. You don't intervene in flight; if it's misbehaving (pointing the wrong way, jittering), that's a calibration issue handled on the bench (see Builder's Manual Section 5.4.4 for NVS configuration).

## 5. Post-flight

You've landed. This section covers the procedures from "stick disarm" to "recordings safe on disk."

### 5.1 Disarm and cooldown

1. **Land the aircraft.** Standard landing per your model's mission profile.
2. **Disarm** via the inverse handshake: arm key UP combined with throttle low. The audio narrator announces "Disarmed." The HUD shows DISARMED state. The HUB75 panel transitions to POSTFLIGHT.
3. **Wait ~30 seconds** for POSTFLIGHT to time out and the panel to return to IDLE. During POSTFLIGHT the daemon finalizes the recording file and runs any post-flight processing (geographic enrichment, recording summary, etc.).

Don't immediately power down. The next subsection explains why.

### 5.2 Recording flush behavior

Recordings flush to disk as the daemon runs, but the final finalization (closing the SQLite file properly, writing metadata) happens at POSTFLIGHT and at daemon stop.

Default location: `~/zerotx/recordings/flight-<timestamp>.db`. Override with `-recordings-dir` flag (see Builder's Manual Appendix C). Disable entirely with `-no-recordings`.

Retention: the 10 most-recent recordings by default (`-keep-recordings N`). Older recordings auto-delete on save. If you want to preserve a specific flight, copy it off the Pi before the next flight.

**Important:** pulling power before the daemon shuts down cleanly can leave the recording file in an inconsistent state. SQLite is resilient but not bulletproof. Use the shutdown sequence in Section 6.6.

### 5.3 Recording review with zerotx-replay

The `zerotx-replay` tool reads a recording file and prints a flight summary: session metadata, telemetry statistics, the chronological event log, and audio/alarm events grouped together. It does **not** re-evaluate analysis (no re-running narrator, no re-running alert rules); the recordings already capture what the daemon decided at the time.

```
$ zerotx-replay ~/zerotx/recordings/flight-<timestamp>.db
```

The tool is a separate Go module that builds standalone on any desktop or server without the daemon's hardware-specific dependencies. You can copy the `.db` files off the Pi to a workstation for analysis:

```
$ scp <pi_host>:~/zerotx/recordings/flight-*.db /tmp/
$ /path/to/zerotx-replay /tmp/flight-<timestamp>.db
```

What the summary tells you:

- Total flight duration (arm to disarm)
- Telemetry frame statistics (received, dropped, gaps)
- Mode transitions with timestamps
- Threshold crossings (battery, altitude, distance)
- Alarm/audio events
- Any errors the daemon logged in flight

This is your post-flight debrief data. Useful for: confirming the planned flight matched the actual flight, spotting issues you didn't notice in flight, retrospective analysis of failures.

### 5.4 Geographic enrichment

If the daemon was launched with `-geo-db <path>` (an offline place-name database built by `tools/build-geo.sh`), post-flight narration enriches the flight summary with place names: "Flew over Itu, peak altitude 850m AMSL at 14:23." This happens during POSTFLIGHT phase, before the recording is finalized.

The geo database is built once on a workstation with internet access and copied to the Pi. Field operation doesn't require an internet connection for enrichment to work.

To disable enrichment: omit the `-geo-db` flag from the daemon launch. The narrator skips the geo-enrichment step and the post-flight summary uses raw coordinates.

## 6. Field operations

Operating in the field — at a site away from your build bench. This section covers everything that's specific to deploying, operating, and packing up the case at a location.

### 6.1 Site setup

A typical site setup:

1. **Place the case** on a stable surface (table, vehicle tailgate, dedicated stand). The case is heavy enough that wind won't tip it; orient it so:
   - You face the kiosks comfortably.
   - The keylock and e-stop are reachable without crossing your body.
   - The pole bulkhead is on the appropriate side for your antenna pole placement.
2. **Run the pole cable** from the case bulkhead to where the antenna pole will stand. Avoid kinks and tight bends; the cable carries CRSF (single-wire or RS-422 differential) and any cable damage degrades the link.
3. **Set up the antenna pole** at the appropriate location. The ELRS module's antenna should have a clear path to the aircraft's flight area, ideally with no obstructions in the line-of-sight angle the aircraft will operate within.
4. **Connect the pole cable** at the pole end (project box if extended config; ELRS module directly if default config).
5. **Connect the 12V source** to the rear panel. Verify the source can sustain the case's runtime needs (see 6.2).
6. **Run the cold start sequence** (Section 2).

If you're at a familiar site, you may have notes on where the pole goes relative to the case, what angle gives the best link, where the sun is at the planned flight time. Keep a per-site notebook entry; ZeroTX itself doesn't store this.

### 6.2 Power management

The case runs on 12VDC at the rear panel jack. No internal battery; no internal UPS. **Power loss = case off.** Plan accordingly.

**Power source options:**

| Source | Pros | Cons | Notes |
|---|---|---|---|
| SLA battery + charger pack | Independent, predictable runtime | Heavy (7-12 kg for useful capacity), needs maintenance | Most common field option. Pick a 12V 12-18 Ah battery for ~3-5 hours of runtime. |
| LiFePO4 battery | Lighter, longer cycle life | More expensive; needs a 12V-compatible LiFePO4 to avoid voltage swing | Modern alternative. 12V 20Ah is comfortable. |
| Vehicle 12V (cigarette lighter / dedicated wiring) | Convenient if at the vehicle | Vehicle electrical noise may affect audio; vehicle off = case off | Use a dedicated outlet, not the cigarette lighter for high-current draws. |
| Bench supply | Steady, configurable | Tethered to AC | For bench testing and home-field flying near AC power. |

**Runtime budget:** the case draws roughly 2-4 A at 12V depending on what's active (peak ~6 A with all kiosks rendering, audio playing, panel at max brightness, MCUs running). For a 12V 12Ah SLA, expect ~3 hours of usable runtime before the SLA dips below cutoff voltage.

**Voltage thresholds:**

- ZeroTX's internal regulators handle 11-14V comfortably. Below 11V the 5V bucks start brownout-resetting MCUs; below 10.5V the Pi 400 itself drops out.
- If using an SLA, **stop flying before the battery dips below 11.5V** — your aircraft going into failsafe due to ground-station brownout is a worst-case failure.

**Hot-swapping power:** the case does not support hot-swap. Powering down between source changes is mandatory (run the shutdown sequence in 6.6).

### 6.3 Cable handling and stress points

The pole cable is the single most failure-prone item on a field deployment. It gets stepped on, tripped over, sun-baked, repeatedly bent at the same points. Habits that prolong it:

- **Cable strain relief.** At both ends (case bulkhead, pole-end project box or ELRS), the cable should not hang under its own weight from the connector. Route it so the weight goes through a sleeve or zip-tie attached to the case/pole, not through the connector contacts.
- **Avoid running cable across walking paths.** When unavoidable, tape it down and flag the location.
- **Coil for transport.** Loose-coil (not figure-8) when packing; tight coils cause kinks that degrade signal integrity over time.
- **Inspect connectors before each session.** A bent pin is a coin flip on link reliability.
- **5m default; longer means extended configuration.** If you need >5m, you should already be running RS-422 with MAX490 transceivers per the extended config (Builder's Manual Section 4.7.2). Long runs of single-wire CRSF at 400kbaud are unreliable past ~5m.

If a cable fails mid-flight (you see RX_LOSS at the aircraft), see Section 7.4.

### 6.4 Wind and weather thresholds

ZeroTX itself doesn't care about weather (it's a static ground station). But the daemon surfaces weather-aware alerts (if `-no-weather` isn't set) that inform your operational decisions:

| Alert | Default threshold | Configurable flag | Meaning |
|---|---|---|---|
| `wind_gust_high` | gusts > 30 km/h | `-wx-max-gust-kmh` | Strong gusts; small aircraft will struggle |
| `wind_speed_high` | sustained > 20 km/h | `-wx-max-wind-kmh` | Sustained wind exceeds typical safe operating |
| `wind_shear_high` | 80m wind direction differs from surface by >45° AND speed ratio >2 | `-wx-shear-dir-deg`, `-wx-shear-speed-ratio` | Significant shear; aircraft may behave unexpectedly at altitude |
| `precip_imminent` | precipitation probability > 60% in next 3 hours | `-wx-precip-pct` | Rain or wet conditions coming |
| `near_sunset` | within 30 minutes of sunset | `-wx-near-sunset-min` | Light degrading; consider RTH timing |
| `golden_hour_active` | sun elevation < 6° | `-wx-golden-elev-deg` | Glare and visibility concerns |

These alerts narrate via audio when they trigger. Don't fly when they do without explicit awareness. The daemon doesn't *block* flight on weather (that's the operator's call), but it does shout about it.

You can adjust thresholds per site if needed (e.g., a coastal site with more wind tolerance). Edit the daemon flags in the systemd unit and reload.

### 6.5 Updating components

Field updates (between flights, at the site) work for the daemon and Pi-side software. **Firmware updates** (MCU reflashing) generally require a workstation; defer firmware updates to bench time.

**Daemon update (if you've pushed a new build to git):**

```
$ cd ~/zerotx
$ git pull
$ scripts/build-daemon.sh
$ sudo systemctl restart zerotxd.service
```

Build script outputs to `bin/zerotxd`. To rebuild everything (daemon + tools + firmware), run `make` at the repo root. In the field, only the daemon usually needs touching.

**Firmware update at the bench** (covered for reference; not a field procedure):

- ESP32 panel: `cd ~/zerotx/firmware/display && pio run -t upload`
- Mega IO: `cd ~/zerotx/firmware/io && pio run -t upload`
- RP2040 CRSF: build the `.uf2` (see `firmware/crsf/README.md`), BOOTSEL the RP2040, copy the file
- ESP32-S3 tracker: `cd ~/zerotx/firmware/tracker && pio run -t upload`

All firmware update flows are covered in Builder's Manual Section 5. In the field with no laptop, you can't do them; carry a known-good SSD image (see Builder's Manual Section 6.17) as the field-recovery option instead.

### 6.6 End-of-day shutdown

**Order matters.** Don't pull power before the daemon shuts down — recordings need to flush, USB-CDC buffers need to drain.

1. **Stop the daemon cleanly:** `sudo systemctl stop zerotxd.service`. Wait for the journal to confirm clean exit (`Stopped ZeroTX daemon`).
2. **Wait for clean exit.** Recording flush completes; serial buffers drain. Typically ~2 seconds.
3. **Shut down the Pi:** `sudo poweroff`. Wait for the Pi to shut down (LCDs go dark, SSD activity LED stops).
4. **Flip the keylock to OFF.** 12V rail drops to zero.
5. **Disconnect the 12V source** (unplug battery, disconnect bench supply, etc.).
6. **Disconnect the pole cable** at both ends. Coil for transport.
7. **Pack the pole** (collapse if telescoping; bag the ELRS module separately).
8. **Stow the case.** Protect the front panel from impacts and the rear ports from debris.

Doing this in reverse order (power off, then daemon stop) loses the last few seconds of recording data and risks USB-CDC buffer corruption. Don't do it.

## 7. Recovery procedures

Things sometimes go wrong in flight or right at the field. This section covers the fast-action scenarios where you need to act now, not return to the bench.

For pre-flight or bench-side issues, see Section 8 (Run-time troubleshooting). For deep build-time recovery (MCU bootloader corruption, etc.), see Builder's Manual Section 9.

### 7.1 Failsafe triggered at the field

Your aircraft just engaged its failsafe behavior (RTH, descend, level, whatever the FC is configured for). What do you do?

**First: don't panic.** Failsafe behaviors are designed to bring the aircraft home or down safely. Let the FC do its job. Your job is to figure out **why** it triggered.

**Possible triggers (in rough probability order):**

1. **Range / link degradation:** the aircraft flew far enough that link quality dropped below the FC's RX_LOSS threshold. Check the HUD's last-known link quality and distance. Most common cause.
2. **Pole cable broken:** the cable between case and pole became disconnected or damaged. ELRS module went dark from the case's perspective; aircraft saw RX_LOSS.
3. **Ground station fault:** daemon crashed, Pi rebooted, RP2040 stopped emitting. Check the kiosks: are they showing live state, or are they frozen? Are they showing `/status` again (which means daemon was reset)?
4. **E-stop pressed:** accidentally tripped. The e-stop's mushroom is usually obvious if pressed (latched position).
5. **Aircraft-side issue:** receiver fault, ELRS module on aircraft glitch, antenna issue. ZeroTX can't tell you this from the ground.

**Action while the aircraft is failsafing:**

- Track its course visually. Failsafe is usually RTH; the aircraft will return.
- If the kiosks are frozen, **don't reboot anything** — even with daemon dead, the aircraft is autonomously RTH'ing. Adding chaos doesn't help.
- Once the aircraft is back overhead, the ground station MUST be alive again for you to take manual control. So *if* the daemon is dead, restart it before the aircraft is back in range:
  ```
  sudo systemctl restart zerotxd.service
  ```
- Wait for the kiosks to reload, RP2040 LED to go green, ELRS module to indicate link restored.
- Disarm via the joystick when you have control again.

If the trigger was the ground station (#3 or #4 above), don't fly again until you understand why and have fixed it. Re-arming and re-launching with a flaky ground station risks a worse failsafe next time.

### 7.2 Lost aircraft

No telemetry, no visual. The aircraft is somewhere out there and you don't know where.

**Last known position:** the Map kiosk's track line ends at the last received GPS frame. That's your search starting point.

**Heading and altitude trend:** the HUD's last-known values give you a vector. If the aircraft was descending when telemetry died, the search radius is smaller than if it was cruising.

**Time since last contact:** the longer it's been, the larger the search area. Aircraft tend to keep moving in a roughly straight line until something interrupts them (battery, terrain, structural failure).

**Recovery steps:**

1. Note the time. Glance at your watch or phone.
2. Note last-known position from the Map kiosk before anything else changes.
3. Note last-known altitude, heading, and battery from the HUD.
4. If your model has GPS-based RTH and there's still battery, the aircraft *may* still be RTH'ing without telemetry coming back. Wait 30-60 seconds at the home position before assuming lost.
5. If clearly lost, mark the last-known position on a paper map or phone GPS. Plan a search pattern based on heading, time, terrain.
6. **Don't power down the ground station yet** — if the aircraft comes back into range, telemetry resumes and you'll see it instantly.

This is an operational concern, not a ZeroTX failure mode. The system can't recover a lost aircraft for you; it can only give you the last data it had.

### 7.3 Daemon dies mid-flight (Pi up, kiosks dark)

You see: the Pi 400 is on (HDMIs receiving signal, but content is frozen or black), the RP2040 LED has transitioned from green → amber-blink → red-blink (FAILSAFE), audio narration stopped, no new telemetry on either kiosk.

**The failsafe chain has already triggered.** The aircraft saw RX_LOSS within ~950ms of daemon death and is now engaged in its own failsafe behavior.

**Action:**

1. **Don't panic.** The chain works. The aircraft is autonomously RTH'ing or whatever its FC configures.
2. **Restart the daemon:**
   ```
   sudo systemctl restart zerotxd.service
   ```
   If you can SSH in, do that. If not, you may need to plug a USB keyboard into the front-panel USB-A and switch to a tty via Ctrl+Alt+F2 then `sudo systemctl restart zerotxd.service`.
3. **Watch for the RP2040 LED to return to green.** That confirms daemon is back and CRSF is emitting.
4. **Wait for kiosks to refresh.** They may need a Ctrl+R if they cached an error state.
5. **Confirm telemetry returns.** When the aircraft is back in range (RTH'ing), telemetry comes back through the link; HUD repopulates.
6. **Take manual control via the joystick** to land normally.

If the daemon won't restart, see Section 8.4.

### 7.4 Lost telemetry mid-flight (link alive, no data)

You see: kiosks are alive, RP2040 LED green, ELRS module appears to be transmitting. But the HUD shows frozen telemetry values; the Map's aircraft icon hasn't updated in seconds.

The link is up but no FC data is arriving. Possibilities:

- **Aircraft-side telemetry stalled:** FC isn't sending telemetry frames (could be FC config change, FC fault, receiver fault). Aircraft is still receiving channels from you; it's only the return path that's broken.
- **ELRS module on the aircraft glitched:** uncommon; usually transient.
- **Tracker (extended config) stopped pumping bytes:** ground-station-side telemetry path is broken.

**Action:**

1. **Keep flying.** If channels are reaching the aircraft (i.e., it still responds to your stick inputs), you can fly it home by visual line-of-sight even without telemetry.
2. **If you can't see the aircraft, fly home via memory/skill.** You've lost the GPS readout on the Map but the FC is still receiving inputs.
3. **Land as soon as practical.**
4. **Diagnose post-flight.** Don't fly more until you know why telemetry stalled.

If telemetry is degrading slowly (link quality dropping rather than abrupt freeze), that's a range issue; fly closer and the link will recover.

### 7.5 Tracker stopped tracking (extended config)

Tracker reports `hold` or `no-telem` in the HUD's link panel. The pole-end gimbal is frozen.

**The link is unaffected.** The tracker is transparent in the byte-pump path; even if it's not parsing GPS frames, CRSF still flows through. You can keep flying. The only loss is the autonomous antenna pointing.

**Action:**

- **Continue the current flight** if visible/in-range. The tracker's failure isn't aircraft-side.
- **Mark the issue mentally** for post-flight investigation.
- **Bench-side diagnosis:** see Section 7.6 if it's a complete firmware failure (byte-pump broken), or just calibration/parser issue (handled at the bench per Builder's Manual Section 5.4.4).

### 7.6 Tracker firmware failure (extended config; need bypass)

The tracker is the problem: byte pump broken, no CRSF reaches the ELRS module from the case, telemetry doesn't return. Aircraft has failsafed because nothing is coming back from the link.

You're at the field. You need to bypass the tracker physically.

**Planned bypass jumper (TODO, not yet implemented):** a hardware bypass jumper in the project box that routes the cable's RS-422 pair directly to the ELRS module's CRSF input, skipping the tracker. When this lands, you set the jumper and continue.

**Until the jumper exists:** manual bypass requires opening the project box and physically rewiring the cable directly to the ELRS module's CRSF connector. This is a field operation only if you have a screwdriver and 2 minutes. Otherwise, **the flight is over** until you can do the bypass at the bench.

This is a known limitation of the current build. The jumper item is on the roadmap.

### 7.7 Map tile gaps mid-flight

You see: the aircraft has flown into an area where the Map kiosk shows blank/white tiles. No coverage.

**Cause:** the offline PMTiles archive (`maptiles/sp-state-sat.pmtiles` for the SP-state default; per `-maptiles-dir` flag) only covers a specific region. If you're at a site outside that region, you have no tile coverage there.

**Action in flight:**

- The aircraft icon and track line still render — they don't need tiles. You can fly by them and the HUD; the Map's geographic context is just gone.
- **`tilewarm` may catch up if you're online.** If the Pi has internet (Wi-Fi or LTE tether), the daemon's tile-warm subsystem opportunistically fetches tiles around the aircraft's position and caches them. Watch for tiles to fill in as you fly.

**Action between flights:**

- **For in-state gaps:** let `tilewarm` catch up (online, given time).
- **For out-of-state coverage:** build new PMTiles for the target area at home (workstation, not field), per `tools/maps/` documentation. Copy the file to the Pi's `~/zerotx/maptiles/` and update `-tileset-osm-file` / `-tileset-sat-file` flags if needed.

**Prevention:** check map coverage at the site before takeoff. If the Map kiosk is blank around your planned flight area, you're flying without map context. Acceptable for some pilots, not for others — your call.

### 7.8 Mid-flight peripheral failure

A peripheral (VFD, panel, audio, kiosk) dies mid-flight but everything else is fine. The flight can continue because the failed item isn't on the safety path.

**Triage by what failed:**

| Failed | Critical? | Action |
|---|---|---|
| VFD blank or stuck | No | Continue. Auxiliary indicator only. |
| HUB75 panel dark | No | Continue. Glance-at-a-distance indicator only. |
| Audio silent | Marginal | Continue but with extra HUD attention; you're flying blind to narration cues. Land as soon as practical. |
| One kiosk dark | Possible | Continue if you can pilot from the remaining kiosk. Land if both kiosks are needed. |
| Both kiosks dark | Yes | If channels are still reaching the aircraft, fly it home visually. Otherwise treat as 7.3 (daemon-equivalent failure). |
| Heartbeat LED dark | No | Continue. Indicator only. |
| ELRS module indicator dark | Yes | Aircraft is in failsafe; treat as 7.1. |

The rule: peripherals that affect operator awareness (kiosks, audio) are higher-priority to recover. Peripherals that just report state (VFD, panel, heartbeat LED) are not flight-critical.

## 8. Run-time troubleshooting

> **Placeholder.** Section to be filled in next patch in the USER.md series.

## Appendices

### A. Daemon flag quick reference

> **Placeholder.** Will mirror the Builder's Manual Appendix C, but framed for operational adjustments (which flags to add/remove for different sites, modes, scenarios). To be filled in next patch.

### B. Audio cues catalog

> **Placeholder.** Catalog of every audio narration line the daemon produces, mapped to its trigger condition. To be filled in next patch.

### C. Glossary

> **Placeholder.** Operator-facing subset of the Builder's Manual Appendix G. To be filled in next patch.

### D. Changelog

> **Placeholder.** Version history of this manual. v0.1 entry on creation.
