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
  -vfd-port /dev/serial/by-id/<MEGA> \
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
| `-vfd-port` | Mega serial device path (also carries iohub multiplex traffic) |
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

Symptom: daemon log shows `-vfd-port` device not found; VFD dark; trackball ring LEDs unresponsive.

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

### Telemetry stalled mid-flight

Symptom: HUD values frozen; panel does not reflect aircraft state.

Diagnose: ELRS TX backpack still enumerated? `source` subsystem in daemon log?

Fix: replug ELRS TX backpack at the hub. If recurrent, suspect cable to external pole, hub stability, or ELRS firmware on the module.

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
- `firmware/display/README.md`, `firmware/io/README.md`, `rp2040/README.md`: firmware-specific procedures
