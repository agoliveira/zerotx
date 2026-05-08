# ZeroTX Operations

## Purpose and scope

Day-to-day procedures: launch the system, fly with it, recover from common failures. Audience is me at the box, in the field or on the bench, wanting it to work.

For architecture see `docs/ARCHITECTURE.md`. For wiring see `docs/CONNECTIONS.md`. For first-time provisioning see `docs/BOOTSTRAP.md`.

## Cold start sequence

1. Confirm power source: lab IEC (front) or field 12V/AC input (rear). Both feed the same internal regulation; pick one.
2. Flip the case power switch. 13.8V rail comes up; downstream 5V rails follow.
3. Pi 400 boots from the USB SSD. HUD and Map LCDs show Pi boot output.
4. MCU satellites enumerate as the Pi reaches USB init.
   - Mega: VFD shows boot banner. (TODO: confirm final banner text)
   - ESP32: HUB75 panel briefly shows firmware version, then transitions to IDLE.
   - RP2040: hardware watchdog active; no visible indicator beyond USB enumeration.
5. Pi reaches autostart. Chromium kiosks launch on both LCDs.
6. Daemon autostarts (systemd unit, see `docs/BOOTSTRAP.md`), connects to all USB devices, transitions panel to IDLE.

Expected end state: VFD shows ready text, panel in IDLE mode, HUD shows "no telemetry" or last cached state, Map centered on home.

**TODO**: confirm exact boot indicators for each MCU once firmware boot banners are finalized.

## Daemon launch

Under systemd autostart the daemon comes up automatically. Manual launch (development or recovery):

```
cd ~/zerotx/pi/daemon
./bin/zerotxd \
  -api 127.0.0.1:8080 \
  -model configs/big_talon_zerotx.yml \
  -joystick-name Thrustmaster \
  -piper-binary $HOME/zerotx/bin/piper/piper \
  -web-dir web \
  -port /dev/serial/by-id/usb-Raspberry_Pi_Pico_E66138935F3C4824-if00 \
  -iohub-port /dev/serial/by-id/<MEGA> \
  -site-lat -22.91 -site-lon -47.06 \
  -tilewarm-rate 5 \
  -v
```

Flags:

| Flag | Purpose |
|---|---|
| `-api` | HTTP plus WebSocket API bind address |
| `-model` | aircraft profile (yaml in `configs/`) |
| `-joystick-name` | substring match on USB joystick device name |
| `-piper-binary` | path to Piper TTS binary |
| `-web-dir` | static web assets root |
| `-port` | RP2040 USB-CDC device path |
| `-iohub-port` | Mega IO board USB-CDC device (VFD, trackball LEDs, buttons, etc.) |
| `-site-lat`, `-site-lon` | home coordinates for Map and bearing calculations |
| `-tilewarm-rate` | tile prefetch rate (tiles/sec) |
| `-v` | verbose logging |

Logs:

- Under systemd: `journalctl -u zerotxd.service -f`
- Manual launch: stderr of the binary
- API log buffer: `GET http://127.0.0.1:8080/api/logs` (last N lines from `logbuf`)

## Pre-flight checklist

Before each flight:

1. ELRS TX module powered on and paired with aircraft.
2. Aircraft powered on, GPS lock acquired (check satellite count on HUD).
3. Telemetry stream visible: HUD shows live values, panel transitions to PREFLIGHT.
4. Joystick centered, no stuck axes: verify with `bin/zerotx-axes` if needed.
5. Audio output verified: any non-silent event or panel test.
6. Map shows current position (home or aircraft, depending on aircraft GPS state).
7. Arm subsystem pre-arm gate clear: check arm state in HUD or via `bin/zerotx-inspect`.

## In-flight workflow

Mostly hands-off after launch. The system narrates mode transitions, alarms, and weather alerts via audio. The HUB75 panel reflects FLIGHT, ALARM, or RTH state at a glance. Map updates with aircraft track. Recorder runs continuously.

Mode changes are made via radio, not via GCS.

## Antenna tracker (optional, extended configuration only)

The pole-end ESP32-S3 antenna tracker is an optional add-on used in the extended cable configuration (see `docs/CONNECTIONS.md`). When present, it is autonomous: once configured for the site, it tracks the aircraft based on CRSF GPS frames it sees passing through on the wired link. The daemon is not involved; tracker behavior survives Pi reboots and daemon restarts. In default cable configuration there is no tracker on the line and this section can be skipped.

### First-time site setup

Connect the tracker to a host machine via USB-CDC. Open a serial console at 115200:

```
screen /dev/ttyACM0 115200
```

Configure station coordinates (lat, lon, alt in meters), pan reference azimuth (a known-direction landmark used to align pan-center), pan range (180, 270, or 360 degrees depending on servo), pan pulse calibration (min, center, max in microseconds), and the same for tilt. Persist to NVS:

```
cfg station <lat> <lon> <alt_m>
cfg pan_ref <azimuth_deg>
cfg pan_range <180|270|360>
cfg pan_pulse <min_us> <center_us> <max_us>
cfg pan_invert <on|off>
cfg pan_flip <on|off>
cfg tilt_range <deg>
cfg tilt_pulse <min_us> <center_us> <max_us>
cfg tilt_invert <on|off>
cfg save
```

`cfg show` displays the active config. `aim <az_deg> <el_deg>` manually drives the gimbal for alignment work. `pos` shows current servo positions and EMA state. `stats` shows parser counters and telemetry age.

See `firmware/tracker/README.md` for the full console reference.

### In-flight behavior

The tracker reports `tracking` when receiving fresh GPS frames, `hold` when it has lost telemetry but is holding the last commanded pose, and `no-telem` if it has never seen telemetry since boot. Failsafe is hold-last-position by construction; no GPS frames means no servo commands.

### Hardware bypass

**TODO**: a hardware bypass jumper that routes the cable's RS-422 pair directly to the ELRS module, skipping the tracker, is planned for the project box layout. Until that lands, a tracker firmware failure requires removing the tracker from the line and joining the cable to the ELRS UART manually.

## Post-flight

1. Land. Disarm.
2. Panel transitions to POSTFLIGHT, then IDLE after timeout.
3. Recordings flush to disk. Location: **TODO** (likely `~/zerotx/recordings/`; confirm against `recorder` subsystem).
4. Optionally fetch recordings off the Pi for analysis.

## Shutdown sequence

1. Stop the daemon cleanly: `sudo systemctl stop zerotxd.service` (or Ctrl-C if running manually).
2. Wait for clean exit. Recording flush completes; serial buffers drained.
3. `sudo poweroff` on the Pi.
4. Wait for Pi shutdown (LCDs go dark).
5. Flip the case power switch. 13.8V rail drops.
6. Disconnect power source.

Order matters: stop the daemon before pulling power so recordings finalize and USB serial flushes complete.

## Common failures and recovery

### Mega didn't enumerate

Symptom: daemon log shows `-iohub-port` device not found; VFD dark; trackball ring LEDs unresponsive.

Diagnose: `ls -l /dev/serial/by-id/ | grep -i mega` or `dmesg | tail`. If absent, check USB cable to hub, hub power, Mega power LED.

Fix: replug Mega USB at the hub. If still absent, power-cycle the hub. If still absent, suspect Mega bootloader corruption; reflash (see `firmware/io/README.md`).

### Daemon won't start

Symptom: `zerotxd` exits immediately or systemd shows failed state.

Diagnose: `journalctl -u zerotxd.service -n 50` or run manually with `-v`. Common causes: device path missing (Mega or RP2040 not enumerated), Piper binary missing, model yaml malformed, API port already bound.

Fix: address the specific error. Missing devices: see "Mega didn't enumerate" or "RP2040 watchdog reset loop" below.

### ESP32 panel dark or scrambled

Symptom: HUB75 panel shows no output, garbage, or wrong colors.

Diagnose: ESP32 USB enumeration via `ls /dev/serial/by-id/`. Daemon log for `devices/display` errors. Visual check: ESP32 power LED.

Fix:
- Dark panel, ESP32 enumerated: daemon not sending commands. Restart daemon.
- Dark panel, ESP32 not enumerated: replug ESP32 USB at the hub.
- Wrong colors or scrambled: shift register signal integrity. Check HUB75 IDC ribbon for loose pins, reseat. If persistent, suspect ribbon damage or 5V rail sag under load.
- Isolation test: `bin/disptest --help`.

### RP2040 CRSF watchdog reset loop

Symptom: RP2040 device path keeps disappearing and reappearing every few seconds; daemon log shows port reconnects.

Diagnose: hardware watchdog (firmware m1.8-wdt) resets on internal stall. Common causes: USB-CDC starvation, firmware bug, brown-out on 5V.

Fix: check 5V rail under load; replug USB at the dedicated Pi port. If persistent, reflash via BOOTSEL mode (see `rp2040/README.md`).

### LCD shows no signal

Symptom: HUD or Map LCD black, "no signal" message.

Diagnose: HDMI cable seated, LCD powered, Pi config has both displays enabled.

Fix: reseat micro-HDMI on Pi side and HDMI on LCD side. Confirm via `xrandr` that Pi sees both outputs. If only one LCD shows, suspect that cable or that specific micro-HDMI port.

### VFD blank or garbled

Symptom: VFD shows no characters, random characters, or last state frozen.

Diagnose: Mega enumerated? VFD power present? `journalctl` shows `vfd` subsystem errors?

Fix:
- Blank: check VFD 5V power. Cycle Mega to reinit HD44780.
- Garbled: 4-bit interface timing or loose contrast pin. Reseat VFD ribbon.
- Frozen: daemon stopped pushing updates. Check daemon state and `vfd` subsystem log.

### Audio silent

Symptom: no alarm sounds, no TTS, panel events not narrated.

Diagnose: `aplay /usr/share/sounds/alsa/Front_Center.wav` produces sound? `journalctl` shows `narrator` errors?

Fix: confirm ALSA default sink with `pactl list short sinks`. Re-set with `pactl set-default-sink`. Confirm Piper binary path matches the `-piper-binary` flag and is executable.

### Tracker not tracking (gimbal not moving)

Applies only to the extended cable configuration with tracker installed. Symptom: aircraft is in the air with GPS lock, but the pole-end gimbal does not move.

Diagnose: connect to the tracker via USB-CDC and run `stats` to check parser counters and telemetry age. If telemetry age is high or no GPS frames have been parsed, the wired CRSF path is at fault, not the tracker. If parser counters are healthy but the gimbal is still, check `pos` for clamped servo positions and `cfg show` for misconfigured pan_ref or station coordinates.

Fix:
- High telemetry age: check the case-to-pole cable, MAX490 transceivers on both ends, and the RP2040 link.
- Healthy parser, no movement: re-run station and pan_ref calibration; verify servo wiring and 6V buck output.
- Aircraft stationary on the ground at short range: tracker may be holding center because angles are below tilt range; not a fault.

### Tracker firmware failure (need bypass)

Applies only to the extended cable configuration with tracker installed. Symptom: tracker is enumerated but byte-pumping is broken; channel intents do not reach ELRS, or telemetry does not return to the case.

Diagnose: this would manifest case-side as failsafe on the airframe and absent telemetry on the HUD. Confirm by power-cycling the pole-end project box.

Fix: with the planned hardware bypass jumper (TODO, not yet implemented), route the cable's RS-422 pair past the tracker directly to the ELRS UART. Without the jumper, manual bypass requires opening the project box and physically rewiring.

### Telemetry stalled mid-flight

Symptom: HUD values frozen; panel does not reflect aircraft state.

Diagnose: walk the telemetry path end to end. Default cable configuration: ELRS module to case-to-pole cable to RP2040 to daemon. Extended configuration: ELRS module to tracker (if present) to RS-422 cable to case-end MAX490 to RP2040 to daemon. In either case, RP2040 still enumerated on the Pi side? `source` subsystem in daemon log? In extended configuration, do tracker `stats` show parser still receiving frames?

Fix: replug RP2040 USB if its enumeration dropped. If RP2040 is fine but no telemetry is parsing, the issue is upstream of the case (cable, transceivers if present, tracker if present, ELRS module). Power-cycle the pole-end electronics. If recurrent, suspect cable connection at the bulkhead or ELRS module firmware.

### Web UI won't load or WebSocket dropping

Symptom: HUD or Map browser blank; console shows fetch errors or WebSocket disconnects.

Diagnose: `curl http://127.0.0.1:8080/api/logs` works? Daemon running? Network classification (`netclass`) in daemon log?

Fix: restart daemon. Reload browser kiosk (Ctrl+R if keyboard accessible, or restart Chromium kiosk service).

### Joystick not detected

Symptom: daemon log shows `joystick` no device matching `-joystick-name`; control inputs don't reach radio.

Diagnose: `ls /dev/input/by-id/` shows the joystick? `lsusb` shows it?

Fix: replug joystick at the hub. Confirm `-joystick-name Thrustmaster` substring still matches the device name.

### Tile coverage missing in current area

Symptom: Map shows blank tiles outside São Paulo state.

Diagnose: `tilewarm` only prefetches around current position; the bulk pmtiles file at `maptiles/sp-state-sat.pmtiles` only covers SP state.

Fix: for in-state gaps, let `tilewarm` catch up. For out-of-state coverage, build new pmtiles for the target area on `stan` (see `tools/maps/` and `docs/BOOTSTRAP.md`).

## Diagnostic tools

Binaries in `pi/daemon/cmd/`. All accept `-h` or `--help`.

- `bin/zerotxd`: the daemon itself
- `bin/disptest`: HUB75 panel test harness, sends test patterns to ESP32
- `bin/zerotx-inspect`: live state inspector, reads from running daemon API
- `bin/zerotx-axes`: joystick axis calibration and live monitor
- `bin/geobuild`: offline geographic data builder (used during BOOTSTRAP)
- `tools/zerotx-iohal-config/`: HAL pin and flag configurator for Mega IO

## Updating components

### Daemon

```
cd ~/zerotx/pi/daemon
git pull
go build -o bin/zerotxd ./cmd/zerotxd
sudo systemctl restart zerotxd.service
```

### ESP32 panel firmware

See `firmware/display/README.md`. Typical flow:
```
cd ~/zerotx/firmware/display
pio run -t upload
```

### Mega IO firmware

See `firmware/io/README.md`. Same `pio run -t upload` pattern in `firmware/io/`.

### RP2040 CRSF firmware

See `rp2040/README.md`. Build the .uf2 with Pico SDK and CMake, then put the RP2040 into BOOTSEL mode and copy the file.

### Tracker firmware (ESP32-S3, pole-end, optional)

Only applicable in the extended cable configuration with a tracker installed. See `firmware/tracker/README.md`. Built with PlatformIO. Connect the tracker via USB-CDC (typically `/dev/ttyACM0` through the CH343 bridge during dev), then:

```
cd ~/zerotx/firmware/tracker
pio run -t upload
pio device monitor -b 115200
```

## Tile data refresh

When `stan` finishes a tile build:

```
scp stan:~/zerotx/maptiles/sp-state-sat.pmtiles ~/zerotx/maptiles/
sudo systemctl restart zerotxd.service
```

Verify in Map browser: zoom to a known coordinate, confirm imagery loads.

## See also

- `docs/ARCHITECTURE.md`: system overview
- `docs/CONNECTIONS.md`: physical wiring and topology
- `docs/BOOTSTRAP.md`: first-time Pi provisioning, systemd unit, udev rules
- `docs/protocols/display.md`: HUB75 panel command grammar
- `docs/DECISIONS.md`: locked decisions
- `docs/ROADMAP.md`: pinned and backlog items
- `firmware/display/README.md`, `firmware/io/README.md`, `firmware/tracker/README.md`, `rp2040/README.md`: firmware-specific procedures
