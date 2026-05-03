# ZeroTX Operation Manual

Field reference. Powering on, recognizing what's normal, recovering from
what isn't. Terse on purpose; if you want to know *why* a thing is the
way it is, see `architecture/`.

## Power-on sequence

1. Master case switch ON.
2. Pi 400 boots. Desktop session loads.
3. systemd starts `zerotxd.service` automatically.
4. Display panel briefly shows `ZEROTX 0.18.0`, then goes black until
   the daemon connects and the first telemetry arrives.
5. Right LCD: ZeroTX GUI auto-loads in browser.
6. Left LCD: mwp loads with the map view.
7. ELRS pole module powers up over its cable, antenna pointing skyward.
8. Aircraft powered last. CRSF link comes up; mwp shows GPS position.

## What "normal" looks like at boot

Logs (tail with `journalctl --user -u zerotxd -f`):

```
api: listening on http://127.0.0.1:8080
ipc: handshake OK (proto=3, firmware="zerotx-fw m1.7-armkey")
[mcu] state: 4 -> 2 (heartbeat received)
crsftee: listening on 127.0.0.1:5761
sounds: loaded ...
```

Audio: short boot greeting in selected language (`-sounds-lang`).

GUI right LCD: Pre-flight tab is the default. Status row shows
"Daemon up" / "MCU up" / "Joystick: Thrustmaster" / "Model: <name>".

LED panel: stays in IDLE (dim "ZEROTX") until the FC reports armed
state. Switches to PREFLIGHT when arming begins.

## Pre-flight checklist (manual)

The pre-flight tab in the GUI shows automated readiness:

- Daemon up
- MCU link up
- Joystick connected and bound
- Model loaded
- GPS lock (HDOP threshold from model file)
- Battery voltage above warn threshold
- FC ready (no `!ERR`/`!FS!`/`WAIT*` modes)

Operator-confirmed items (manual checks before the GUI's "Confirm"
button is pressed):

- Battery secured to airframe
- Props/control surfaces clean and free
- Control throws correct (cycle once each axis on the bench)
- GPS lock count above threshold
- Spotters in position

## Arming

1. Operator: pre-flight checklist complete.
2. Throttle stick at idle (channel 0 below ~1100us).
3. Lift the arm key cover; press the arm key.
4. GUI shows "ARMING REQUESTED" within ~1s.
5. Pre-flight TTS summary plays: model name, battery, mode, GPS sats.
6. Press "Confirm" in the GUI to proceed.
7. CRSF arm channel goes high. FC arms.
8. Audio: "Armed."
9. LED panel: switches to FLIGHT, shows BAT/ALT/DST tiles.

If any step fails:
- "ARMING BLOCKED" in GUI: pre-flight item missing. The blocking item
  is logged (`fc-ready: mode="!RX"...` etc.).
- "ARMING TIMED OUT": no Confirm within ~10s. Redo from step 3.
- "ARMING CANCELLED": arm key released or operator cancelled.

## In flight

Operator surfaces:
- **Right LCD HUD** (primary): horizon, altitude, distance, speed,
  battery, link. Color shifts on threshold breaches.
- **Left LCD mwp**: map, aircraft position, track, home, mission.
- **LED panel**: peripheral glance — BAT / ALT / DST in DSEG7 with
  color-zoned bars.
- **VFD** (when wired): scrolling diagnostic firehose, daemon log
  events, terminal-style. Pure aesthetic; not a primary surface.
- **Spectator phone**: connect to `ZeroTX-Spectator` / `pédogalo`,
  open `http://192.168.4.1/`.

Audio:
- Pre-baked alarms fire on threshold breaches (battery low/critical,
  link lost, failsafe). Repeating; instant.
- TTS narrates mode changes, arm/disarm transitions, periodic in-flight
  status if enabled (Settings cog → "Periodic in-flight narration").

## Aborting / emergency

- **Mushroom button**: hardware-cuts ELRS power. Aircraft enters its
  configured failsafe (typically RTH or land). Use when you need
  the link gone NOW. Recover by power-cycling the ELRS module.
- **GUI "Disarm" button**: software disarm via channel. Requires the
  FC to honor disarm-while-flying (most INAV builds reject; use
  mushroom button instead in flight).
- **Killing the daemon**: not a recovery path. Channels stop updating,
  RP2040 goes to HOLD then FAILSAFE on its own watchdog timer.

## Disarm + post-flight

1. Land. Throttle to idle.
2. Press arm key (or wait for FC's auto-disarm).
3. Audio: "Disarmed." LED panel: POSTFLIGHT.
4. ~1s later: post-flight TTS summary (duration, peaks, events).
   If `-geo-db` is loaded, peaks include nearby place names.
5. Recording auto-saves to `~/zerotx/recordings/<timestamp>.db`.
6. GUI Recordings tab shows the new entry.

## Common failures and what to do

| Symptom | Likely cause | Fix |
|---|---|---|
| GUI shows "Daemon down" | systemd unit crashed | `systemctl --user status zerotxd`; if crashed, `journalctl --user -u zerotxd -n 100` |
| GUI "MCU down" | RP2040 disconnected or crashed | Check USB cable; replug; the daemon will auto-reconnect on next handshake |
| GUI "Joystick: not detected" | HOTAS-X unplugged or after-boot insert | Replug; SDL hot-plug callback re-binds |
| No audio | mpg123/aplay not in PATH, or sound card busy | `aplay -l` to verify ALSA sees the device |
| TTS silent but alarms work | Piper binary missing or voices not fetched | Run `scripts/fetch-voices.sh`; verify `~/zerotx/bin/piper/piper` exists |
| Panel stuck on ZEROTX banner | Daemon not connected to ESP32 | Check `journalctl ... -f` for `display:` lines; verify `-display-port` |
| mwp shows no map data | CRSF tee not reaching mwp | mwp protocol must be set to CRSF; address `tcp://127.0.0.1:5761` |
| Spectator AP missing | ESP32 WiFi failed to start | Reboot ESP32 (replug USB to Pi) |
| VFD blank | Pro Micro firmware not loaded, or daemon `-vfd-port` wrong | `dmesg \| tail` for ttyACM enumeration |
| Failed arm, "FC not ready" | INAV reports `!ERR` / `!FS!` / `WAIT*` | Check INAV configurator; mode string is logged on every transition |

## Power-down

1. Disarm (if armed).
2. Aircraft battery off.
3. ELRS module off.
4. Pi: `systemctl --user stop zerotxd` (optional; the SIGTERM on
   shutdown does the same), then shutdown via desktop menu.
5. Master case switch OFF.

## Daily-use cheatsheet

```
# Start daemon manually (if not via systemd):
cd ~/zerotx/pi/daemon
./bin/zerotxd -api 127.0.0.1:8080 -model configs/big_talon_zerotx.yml ...

# Tail logs:
journalctl --user -u zerotxd -f

# Restart after env file edit:
systemctl --user restart zerotxd

# Inspect a model file:
./bin/zerotx-inspect -zerotx configs/big_talon_zerotx.yml

# Bench test against SITL:
./bin/zerotxd ... -fc-tcp-addr 127.0.0.1:5762
```
