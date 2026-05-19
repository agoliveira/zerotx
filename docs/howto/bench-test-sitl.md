# Bench Testing with INAV SITL

End-to-end software validation without an aircraft. INAV SITL acts
as the FC; X-Plane provides aerodynamics; ZeroTX talks raw CRSF to
SITL over TCP via the `-fc-tcp-addr` flag.

What this exercises:
- Daemon channel-intent loop, mixer, arm state machine
- CRSF telemetry decoder
- HUD, LED panel, VFD firehose
- Audio (alarms + TTS), recorder, post-flight narration, geo lookup
- mwp tee + map view

What this does NOT exercise:
- RP2040 firmware (idle in SITL mode)
- Real CRSF over RF, range, packet loss patterns
- Hardware arm key, mushroom button (use API/GUI to arm)
- Vibration / sensor noise

## One-time setup

### 1. Build INAV SITL

```bash
git clone https://github.com/iNavFlight/inav.git
cd inav
make TARGET=SITL
```

Output: `obj/main/inav_<version>_SITL` (Linux executable).

Dependencies: `cmake`, `gcc`, `ruby`. Apt:

```bash
sudo apt install cmake gcc ruby
```

### 2. Configure SITL eeprom for CRSF on UART3

SITL maps each configured INAV UART to TCP port 5760 + (uart-1).
UART3 lands on TCP 5762. We configure it as a Serial RX with CRSF
provider.

Launch SITL once in configurator-only mode to create `eeprom.bin`:

```bash
./obj/main/inav_<version>_SITL
```

In another terminal, launch the INAV Configurator. Connect to
`tcp://127.0.0.1:5760` (the UART1 default MSP port).

In the Configurator:
1. **Ports tab**: enable "Serial RX" on UART3. Save.
2. **Receiver tab**: receiver type = "Serial", serial provider = "CRSF". Save.
3. **Sensors tab**: set every sensor to "FAKE" (Gyro, Accel, GPS, Mag, Baro,
   Pitot if present).
4. **Modes tab**: ensure ARM is bound to a switch position you can drive
   from the model file's CH4 (Arm channel). Common: AUX1 high = ARM.
5. **Configuration tab**: motor + ESC sane defaults; airframe matches
   what X-Plane will load (start with "Aircraft with tail" preset).

Stop SITL. The eeprom.bin in the SITL working directory now has the
right config; it's reused on every subsequent launch.

### 3. Install X-Plane 11

X-Plane 11 (Steam or standalone). After install:

1. Launch X-Plane.
2. Settings → Network → enable "Accept incoming connections".
3. Note the "Port we receive on" under UDP PORTS (default 49000).

INAV's INAV-X-Plane-HITL plugin documentation has airframe files;
the simplest start is the bundled tail-aircraft.

## Per-session launch

**Order matters**: X-Plane first, then SITL, then ZeroTX daemon.

### 1. X-Plane

Launch X-Plane. Load airframe. Aircraft on runway, ready for takeoff.

### 2. SITL

```bash
cd ~/inav
./obj/main/inav_<version>_SITL --sim=xp --simip=127.0.0.1 --simport=49000
```

Console will show UART bindings:

```
[SOCKET] Bind TCP :: port 5760 to UART1
[SOCKET] Bind TCP :: port 5761 to UART2
[SOCKET] Bind TCP :: port 5762 to UART3
```

UART3 (port 5762) is the CRSF endpoint ZeroTX will dial.

### 3. ZeroTX daemon

```bash
cd ~/zerotx
./bin/zerotxd \
  -api 127.0.0.1:8080 \
  -model configs/big_talon_zerotx.yml \
  -joystick-name Thrustmaster \
  -piper-binary $HOME/zerotx/third_party/piper/piper \
  -web-dir pi/daemon/web \
  -fc-tcp-addr 127.0.0.1:5762
```

Look for in the daemon log:

```
fc endpoint: SITL TCP 127.0.0.1:5762 (RP2040 link disabled)
sitl: connected to 127.0.0.1:5762 (LocalVersion="zerotxd ...")
link: sitl tcp 127.0.0.1:5762
```

Telemetry should start flowing; HUD populates with attitude, GPS,
battery (FAKE values from SITL).

## Arming in SITL

Hardware arm key is absent in SITL mode. Use the API instead:

```bash
# Trigger arm-requested:
curl -X POST http://127.0.0.1:8080/api/v1/arm/request

# Confirm:
curl -X POST http://127.0.0.1:8080/api/v1/arm/confirm
```

Or use the GUI on the right LCD (or any browser):
`http://127.0.0.1:8080/` → Pre-flight tab → Confirm.

INAV's arming prerequisites apply:
- Throttle channel low (CH0 below ~1100us)
- All sensors marked FAKE
- No active failsafes
- AUX channels in valid ranges

If SITL refuses to arm, check its console log for the prearm error.

## Things that will likely go wrong on first bring-up

| Symptom | Cause | Fix |
|---|---|---|
| `sitl: dial: connection refused` | SITL not running, or UART3 not bound | Verify SITL log shows "Bind TCP :: port 5762 to UART3" |
| Daemon connects but no telemetry | UART3 not configured as Serial RX with CRSF | Re-do step 2 of one-time setup; check eeprom.bin location |
| `fc-ready: mode="!RX"` persistent | SITL not seeing channel data | Daemon is sending RC frames but SITL rejects them; check serial provider = CRSF in INAV |
| GPS shows zeros | X-Plane not running or wrong port | Verify X-Plane "incoming connections" enabled; default 49000 |
| SITL refuses to arm | INAV prearm check failed | SITL console shows the failed check; common: throttle not low, sensors not FAKE |
| Aircraft falls out of sky | INAV mixer mismatch with X-Plane airframe | Use `--chanmap=...` argument to remap; see INAV SITL docs |

## Verifying the test loop

A successful bench session should produce:
- Pre-flight TTS summary on arm request
- Mode change announcements as you trigger SE/SA/SG channel positions
- Periodic in-flight narration (if enabled in Settings)
- Recording auto-saved to `~/zerotx/recordings/<timestamp>.db` on disarm
- Post-flight TTS summary with peaks (and place names if `-geo-db` is set)

If those all behave, the daemon's flight pipeline is working
end-to-end. The only things you haven't tested are the parts that
require RF and a real airframe.

## Tearing down

Stop in reverse order: daemon (Ctrl-C), SITL (Ctrl-C), X-Plane (quit).
SITL's eeprom.bin persists; X-Plane saves its own state. Next session
just relaunches in the same order.
