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
cd ~/zerotx
./bin/zerotxd \
  -api 127.0.0.1:8080 \
  -model configs/big_talon_zerotx.yml \
  -joystick-name Thrustmaster \
  -piper-binary $HOME/zerotx/third_party/piper/piper \
  -web-dir pi/daemon/web \
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
| `-fc-tcp-addr` | INAV SITL CRSF endpoint as `host:port` (e.g. `127.0.0.1:5762`). Bench-test mode: daemon talks raw CRSF over TCP instead of opening the RP2040 link. Mutually exclusive with `-port`. |
| `-iohub-port` | Mega IO board USB-CDC device (VFD, buttons, GLCD, etc.) |
| `-display-port` | ESP32 HUB75 panel driver USB-CDC device. Empty disables the panel; daemon runs fine without it. |
| `-maptiles-dir` | directory of PMTiles archives for offline map tile serving. Empty = online proxy mode. |
| `-no-online-tiles` | disable online tile proxy fallback (for field operation without uplink) |
| `-sounds-dir`, `-sounds-lang` | audio sample tree (EdgeTX-compatible layout) and language subdirectory |
| `-site-lat`, `-site-lon` | static fallback coordinates for the position resolver chain (`station GPS → telemetry home → site flag`). Used only when no station GPS lock and no in-flight home position are available |
| `-tilewarm-rate` | tile prefetch rate (tiles/sec) |
| `-heartbeat-gpio` | Pi GPIO line driving the daemon heartbeat LED (BCM numbering). -1 disables (default) |
| `-heartbeat-chip` | GPIO chip device for the heartbeat LED. Default `gpiochip0` |
| `-gps-port` | serial device for an optional Pi-attached GPS (e.g. `/dev/ttyAMA1`). Empty disables (default) |
| `-gps-baud` | baud rate for the GPS serial port. Default 9600 |
| `-v` | verbose logging |

Logs:

- Under systemd: `journalctl -u zerotxd.service -f`
- Manual launch: stderr of the binary
- API log buffer: `GET http://127.0.0.1:8080/api/logs` (last N lines from `logbuf`)

## Pre-flight checklist

Before each flight:

1. ELRS TX module powered on and paired with aircraft.
2. Aircraft powered on, GPS lock acquired (check satellite count on HUD).
3. **System check** (`/status` page on both kiosks at boot): all rows green, no blockers listed beneath the Proceed button. The two devices the daemon *enforces* are:
   - **RP2040 link**: heartbeats every ~200 ms over USB-CDC. Down means the CRSF generator is silent and the aircraft will failsafe.
   - **HDMI kiosk displays**: both micro-HDMI ports must report `connected` in `/sys/class/drm`. Down means one of the operator displays is unplugged.
   If either is down, "Proceed to flight" is disabled and the hint reads `Blocked by: device down: <name>`. Plug it back in, wait ~2 s for the page to re-poll, button enables.

   If a hardware baseline file is deployed (see "Hardware baseline" below), additional blockers may appear in the format `hardware baseline: <probe> expected pass, got <actual> (<reason>)`. These come from the daemon's self-check comparing the deployed baseline to current device state.
4. **Proceed**: click "Proceed to flight" on either kiosk. Both kiosks transition to `/hud` and `/map`.
5. Telemetry stream visible: HUD shows live values, panel transitions to PREFLIGHT.
6. Joystick centered, no stuck axes: verify with `bin/zerotx-axes` if needed.
7. Audio output verified: any non-silent event or panel test.
8. Map shows current position (home or aircraft, depending on aircraft GPS state).

After the syscheck gate, the daemon doesn't gate flight further -- arm/disarm is the operator's responsibility via the joystick.

### Hardware baseline (optional)

The bench-side `zerotx-bench` diagnostic tool (`tools/zerotx-bench/`) can export a YAML snapshot of every probed device's state. When that file is deployed to `/etc/zerotx/hardware-baseline.yaml`, the daemon runs a self-check at startup: ~3 seconds after launch (enough for devhealth heartbeats to settle), it compares the baseline's pass-expected probes against its current view of each device and lists mismatches in the Preflight blockers.

Workflow:

1. On the bench, wire up hardware as desired.
2. Stop the daemon: `sudo systemctl stop zerotxd`.
3. Run the bench tool: `tools/zerotx-bench/zerotx-bench`. Browse to `http://<pi-host>:8081`. Click through probes (or "Run all"), manually skip any probes that are intentionally absent.
4. Press "Export baseline". A modal pops up with the YAML and a Copy button. The file also gets written to `./hardware-baseline.yaml`.
5. Deploy: `sudo install -D -m 644 hardware-baseline.yaml /etc/zerotx/hardware-baseline.yaml`.
6. Restart the daemon. Self-check picks up the file automatically.

Probes the daemon enforces today: `rp2040`, `mega`, `esp32-display`, `hdmi`, `gps-ublox`, `rtc-ds3231`, `led-heartbeat`, `joystick`, `audio`, `elrs`. All ten bench-tool probes have matching daemon-side observers. Each observer is honest about its limits — for example, the LED check verifies the daemon believes it's driving the GPIO line, not that the LED is physically lit; the audio check verifies the daemon resolved a playback backend for each configured extension, not that the speakers actually emit sound. The bench tool's probes are stronger in some cases (direct hardware exercise); the daemon's are weaker but continuous.

To disable self-check entirely: pass `-hardware-baseline ""` to the daemon (or remove the file). Settling delay tunable via `-hardware-baseline-settle DURATION`.

## Arming the aircraft

The arm state machine requires three simultaneous inputs to transition to ARMED:

1. **Throttle low** (T-low): joystick throttle stick at minimum. The daemon reads the throttle channel from the active model file (TAER layout for Big Talon and friends, so channel 1; the model declares which).
2. **Arm key** (SF switch, joystick or panel-mounted): two-position switch held in the down ("arm requested") position.
3. **Confirm** (SH momentary): a press of the panel-mounted momentary button (RP2040 GPIO 15) OR Ctrl+Alt+A in either kiosk browser. Press-only; releasing doesn't matter.

All three must be present at the same time. Once armed, releasing any input doesn't disarm. To disarm: bring the arm key back UP combined with T-low -- the inverse handshake.

If arming fails, the audio narrator announces the specific failure ("throttle not low", "arm key not down", "not ready"). The HUD shows the current arm state.

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
3. Recordings flush to disk. Default location: `$XDG_DATA_HOME/zerotx/recordings/` if set, otherwise `~/.local/share/zerotx/recordings/`. Override with `--recordings-dir <path>` on the daemon command line; disable with `--no-recordings`. Retention is the 10 most-recent recordings by default (`--keep-recordings N`).
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

### "Proceed to flight" stays disabled

Symptom: both kiosks sit on `/status` after boot; the Proceed button is greyed out; the hint under it reads `Blocked by: device down: <name>`.

Diagnose: read the blocker name. Two devices block flight today:

- `device down: rp2040` — the RP2040 isn't sending heartbeats. Either the USB-CDC link to the Pi is wedged, or the RP2040 itself is in a reset loop. Check `ls /dev/serial/by-id/ | grep -i pico` and `journalctl -u zerotxd | grep ipc`. If the device path is present but heartbeats aren't arriving, suspect firmware (see "RP2040 CRSF watchdog reset loop" below).
- `device down: hdmi-displays` — one or both HDMI cables aren't reporting `connected` in `/sys/class/drm`. The daemon needs **both** kiosk displays attached. `for f in /sys/class/drm/card*-HDMI-*/status; do echo $f $(cat $f); done` shows the truth.

Fix: address the named blocker. The status page re-polls every 2 s, so a fresh `connected` state propagates within a couple of seconds. If everything looks right but the button stays disabled, `curl http://127.0.0.1:8080/api/v1/preflight | jq '.blockers, .devices'` shows what the daemon actually thinks.

The page enforces the gate visually; the daemon also enforces server-side: `POST /api/v1/syscheck/dismiss` returns HTTP 409 with the blocker list when not ready, so a stale page that somehow lets you click will still be refused.

### Mega didn't enumerate

Symptom: daemon log shows `-iohub-port` device not found; VFD dark; indicator LEDs unresponsive.

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

Fix: check 5V rail under load; replug USB at the dedicated Pi port. If persistent, reflash via BOOTSEL mode (see `firmware/crsf/README.md`).

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

### GPS not detected

Symptom: `-gps-port` is set, but the daemon log shows `GPS: ... (continuing without)` at startup, or no `GPS: <port> @<baud>` confirmation line.

Diagnose: `ls /dev/ttyAMA*` to confirm the device exists. `sudo cat /dev/ttyAMA1` (or whichever device) at the right baud should produce `$GP...` / `$GN...` lines once per second. Check `sudo dmesg | grep -i serial` for kernel-level UART issues. Confirm `dtoverlay=uart3` in `/boot/firmware/config.txt` and that the system was rebooted after the edit.

Fix: most often the wrong device path or baud rate. M6/M7/M10 ship at 9600. Check the breakout wiring: GPS TX must land on the Pi's RX (GPIO 5, header pin 29), GPS RX on the Pi's TX (GPIO 4, header pin 7). A swapped pair shows up as "I can read garbage but the GPS doesn't seem to react"; the daemon will log NMEA parse errors at most once per minute.

This is a non-blocking subsystem: a misconfigured or absent GPS does not stop the rest of the daemon. Consumers fall back to `-site-lat` / `-site-lon` or other position sources.

### Heartbeat LED stuck or dark

Symptom: `-heartbeat-gpio` is set, but the LED is dark, solid on, or stuck.

Diagnose:

- Dark, daemon running: either the 50Hz mapper loop is hung past the 1.5s freshness window (genuine hang, check `journalctl -u zerotxd -f` for the last log line), or the GPIO chip / line is wrong (try `gpioinfo gpiochip0 | grep zerotx-heartbeat` to confirm the line was claimed).
- Solid on (rare): the toggle goroutine itself died after writing high. Restart the daemon. If reproducible, file as a bug — by construction this shouldn't happen.
- Solid on, daemon not running: GPIO line is open-collector floating high; harmless, the daemon will retake it and resume blinking on next start.

Fix: restart the daemon. If the failure mode is "dark while daemon running", the actual problem is upstream of the LED — investigate as a daemon hang.

### Tile coverage missing in current area

Symptom: Map shows blank tiles outside São Paulo state.

Diagnose: `tilewarm` only prefetches around current position; the bulk pmtiles file at `maptiles/sp-state-sat.pmtiles` only covers SP state.

Fix: for in-state gaps, let `tilewarm` catch up. For out-of-state coverage, build new pmtiles for the target area on `stan` (see `tools/maps/` and `docs/BOOTSTRAP.md`).

## Diagnostic tools

All compiled outputs live in `/bin/` at the repo root. Source for the daemon and its companion commands is under `pi/daemon/cmd/`; standalone tools are under `tools/`. Every binary accepts `-h` or `--help`.

- `bin/zerotxd`: the daemon itself
- `bin/disptest`: HUB75 panel test harness, sends test patterns to ESP32
- `bin/zerotx-inspect`: live state inspector, reads from running daemon API
- `bin/zerotx-axes`: joystick axis calibration and live monitor
- `bin/geobuild`: offline geographic data builder (used during BOOTSTRAP)
- `bin/zerotx-iohal-config`: HAL pin and flag configurator for Mega IO
- `bin/zerotx-bench`: hardware diagnostic web UI (bench-only)
- `bin/zerotx-replay`: replays recorded flight telemetry

## Updating components

### Daemon

```
cd ~/zerotx
git pull
scripts/build-daemon.sh
sudo systemctl restart zerotxd.service
```

The build script outputs to `bin/zerotxd`. To rebuild everything (daemon + tools + firmware), run `make` at the repo root.

### ESP32 panel firmware

See `firmware/display/README.md`. Typical flow:
```
cd ~/zerotx/firmware/display
pio run -t upload
```

### Mega IO firmware

See `firmware/io/README.md`. Same `pio run -t upload` pattern in `firmware/io/`.

### RP2040 CRSF firmware

See `firmware/crsf/README.md`. Build the .uf2 with Pico SDK and CMake, then put the RP2040 into BOOTSEL mode and copy the file.

### Tracker firmware (ESP32-S3, pole-end, optional)

Only applicable in the extended cable configuration with a tracker installed. See `firmware/tracker/README.md`. Built with PlatformIO. Connect the tracker via USB-CDC (typically `/dev/ttyACM0` through the CH343 bridge during dev), then:

```
cd ~/zerotx/firmware/tracker
pio run -t upload
pio device monitor -b 115200
```

## MCU recovery (last resort)

When the normal upload paths fail — Mega refuses USB programming, or the RP2040 won't enter BOOTSEL — these procedures restore the chip via the debug interface. Both assume physical access to the board, which may require opening the case.

Distinguish "needs recovery" from "needs reflash":

- Normal failures (firmware crash loop, watchdog reset, daemon can't talk to it) usually still respond to USB programming. Try the standard `pio run -t upload` first.
- True recovery is for when the bootloader itself is corrupted (Mega) or the BOOTSEL button can't physically reach the operator (sealed case, broken switch). Until that's the case, use the normal path.

### Mega 2560: ICSP recovery

Symptoms that warrant ICSP: the Mega doesn't enumerate as a USB serial device at all when connected, OR enumerates briefly then drops, OR `avrdude` reports "stk500v2_ReceiveMessage(): timeout" with the bootloader. The USB-to-serial chip (16U2/8U2) is fine — the AVR has lost its bootloader.

What you need:

- An ISP programmer: USBasp (~$5, most common), Arduino-as-ISP (any Uno/Nano with the ArduinoISP sketch), or an Atmel-ICE. The ZeroTX repo doesn't bundle a specific one — use whatever's at hand.
- A 6-pin (2×3) ICSP cable matching the programmer to the Mega's ICSP header.
- `avrdude` installed (`sudo apt install avrdude`).
- The Mega2560 bootloader hex: `/usr/share/arduino/hardware/arduino/avr/bootloaders/stk500v2/stk500boot_v2_mega2560.hex` (Arduino IDE distribution) or the matching path under `~/.arduino15` for a PlatformIO/arduino-cli install.

Mega ICSP header location: the 2×3 pin header labeled "ICSP" adjacent to the ATmega2560 chip (not the 16U2's ICSP, which is a separate 2×3 header near the USB jack). On the standard Mega R3 board layout it sits between the digital pins 50-53 block and the chip itself. **TODO**: confirm whether this header is accessible without unmounting the Mega from the case.

Procedure (USBasp shown; substitute `-c` value for other programmers):

```
# Verify the programmer sees the chip
avrdude -p atmega2560 -c usbasp -P usb -v

# Restore the bootloader (path may vary per Arduino install)
avrdude -p atmega2560 -c usbasp -P usb \
  -U flash:w:/usr/share/arduino/hardware/arduino/avr/bootloaders/stk500v2/stk500boot_v2_mega2560.hex:i \
  -U lock:w:0x0F:m

# Restore fuses to Arduino defaults (only if fuses were touched;
# do NOT run blindly — wrong fuses can semi-brick the chip)
avrdude -p atmega2560 -c usbasp -P usb \
  -U lfuse:w:0xFF:m -U hfuse:w:0xD8:m -U efuse:w:0xFD:m
```

After the bootloader is back, the Mega should enumerate normally over USB. Reflash the IO firmware via the standard path (`pio run -t upload` in `firmware/io/`).

Caveats:

- USBasp clones sometimes need `-B 4` (slow SPI) for the first read on a chip with unknown fuses.
- The Mega's ICSP header is 5V — don't connect a 3.3V-only programmer without a level shifter.
- If `avrdude` reports the chip signature as `0x000000` or `0xffffff`, the SPI lines aren't connecting cleanly. Check the cable orientation; the pin-1 indicator on the cable must match the pin-1 indicator on the Mega's ICSP header.

### RP2040: SWD recovery

Symptoms that warrant SWD: the BOOTSEL button is physically inaccessible (sealed case, broken switch, soldered out), OR the chip is so hung that holding BOOTSEL during a power cycle doesn't expose it as a USB mass-storage device. Most "firmware crash" cases don't need SWD — the watchdog will reset into a normal state and the daemon can recover.

What you need:

- A SWD probe: another Pico flashed with [`picoprobe`](https://github.com/raspberrypi/picoprobe) firmware is the cheapest option (~$4). CMSIS-DAP, J-Link, or a Raspberry Pi's GPIO with openocd also work.
- A 3-wire connection (SWCLK, SWDIO, GND) from the probe to the RP2040.
- `openocd` with RP2040 support: `sudo apt install openocd` on Ubuntu 22.04+ ships a recent-enough version, otherwise build from the official rp2040 branch.
- The firmware .uf2 from `firmware/crsf/build/zerotx_crsf.uf2` (or equivalent location after a successful Pico SDK build).

RP2040 SWD pad location: on the Pico the SWD pads (SWCLK, GND, SWDIO) are exposed on the underside of the board near the USB connector, marked as pads rather than a header. **TODO**: confirm whether these pads are wired out to anywhere accessible inside the ZeroTX case — if not, recovery requires unmounting the Pico.

Procedure (picoprobe as the SWD source):

```
# One-shot flash from a built .uf2
openocd -f interface/cmsis-dap.cfg -c "adapter speed 5000" \
  -f target/rp2040.cfg \
  -c "program firmware/crsf/build/zerotx_crsf.uf2 verify reset exit"
```

Or, if working with a .elf instead:

```
openocd -f interface/cmsis-dap.cfg -c "adapter speed 5000" \
  -f target/rp2040.cfg \
  -c "program firmware/crsf/build/zerotx_crsf.elf verify reset exit"
```

After flash succeeds, the RP2040 reboots into the new firmware. If the BOOTSEL access issue was the only problem, USB programming works again afterward via the normal `cp firmware.uf2 /media/$USER/RPI-RP2/` path on the next firmware update.

Caveats:

- Hold the RP2040's RESET line low while connecting the SWD probe if the chip is in an active crash loop — otherwise openocd may not get a clean halt.
- `picoprobe` firmware on the source Pico has to match the probe-target protocol the openocd config expects (`cmsis-dap.cfg` for newer picoprobe builds, `raspberrypi-swd.cfg` for the older serial-protocol version).
- Don't share the SWD probe's USB connection with another openocd instance; serialize attempts.



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
- `firmware/display/README.md`, `firmware/io/README.md`, `firmware/tracker/README.md`, `firmware/crsf/README.md`: firmware-specific procedures
