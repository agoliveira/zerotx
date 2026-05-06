# ZeroTX antenna tracker firmware

Pole-mounted ESP32-S3 firmware. Sits inline between the daemon-side
MAX490 RS-422 transceiver and the ELRS TX module's CRSF interface.
Forwards CRSF bytes transparently in both directions and (eventually)
drives a pan/tilt servo gimbal to track the aircraft autonomously by
sniffing GPS frames in the telemetry stream.

## Position in the system

```
case end                              pole end (project box)
=========                             =========================

RP2040 CRSF generator                 ESP32-S3 (this firmware)
   |                                    ^         |
   v                                    |         v
MAX490 (RS-422) ------ cable --------->MAX490    pan/tilt servos
                                         |              ^
                                         v              |
                                       UART1 <==========+
                                         ^         (Phase 3+)
                                         |
                                       UART2 <-> ELRS TX module
```

The wire protocol on the cable is identical to "RP2040 directly to
ELRS module" wiring. This firmware is fully transparent: the daemon,
the RP2040 CRSF generator, and the ELRS module are unaware of the
tracker's existence. You can remove the tracker entirely and connect
RP2040 directly to the ELRS module without any daemon-side
configuration changes.

## Phased implementation

| Phase | Scope                                                    | Status |
|-------|----------------------------------------------------------|--------|
| 0     | Pure byte-pump. Inline CRSF passthrough + watchdog.      | done |
| 1     | CRSF telemetry sniffer on core 0 (parses GPS frames).    | done |
| 2     | Az/el math from aircraft GPS + station GPS.              | done |
| 3     | LEDC PWM driving 2 servos with slew-rate limiting.       | done |
| 4     | Glue az/el outputs to servo angles. Failsafe behaviors.  | done |
| 5     | USB-CDC calibration interface, NVS-stored station coords.| this firmware |

Phase 0 is the safety floor. The byte pump runs on core 1 at the
highest priority; nothing added in later phases is allowed to block
or starve it. Every Phase 1+ feature must live on core 0.

## Hardware

- **MCU**: ESP32-S3-WROOM-1, N16R8 variant (16MB flash, 8MB octal PSRAM)
- **Cable side**: MAX490 (or MAX3490 if available) RS-422 transceiver
- **ELRS side**: RadioMaster Nomad (or equivalent ELRS TX module) via
  CRSF UART
- **Servos** (Phase 3+): standard hobby PWM (1000-2000us, 50Hz),
  6V supply rail. Firmware assumes 180-degree mechanical travel for
  the front/rear flip technique; Phase 5 will make per-axis travel
  range configurable for 270-degree or 360-degree servo variants.
- **Power**: 12V from cable, with bucks at 6V (servos) and 5V (ESP32-S3)

## Pin map

```
UART1 (cable / MAX490): RX=GP17, TX=GP18
UART2 (ELRS module):    RX=GP4,  TX=GP5
Pan PWM   (Phase 3+):   GP6
Tilt PWM  (Phase 3+):   GP7
I2C SDA   (Phase 5):    GP8     (optional magnetometer)
I2C SCL   (Phase 5):    GP9
```

Pins avoided on the ESP32-S3-WROOM-1 N16R8 module: 19, 20 (native
USB-OTG, used by this firmware for USB-CDC); 26-32 (SPI flash and
octal PSRAM); 33-37 (not exposed on WROOM-1); 0, 3, 45, 46
(strapping pins).

## Build, flash, monitor

```
cd firmware/tracker
pio run
pio run -t upload
pio device monitor
```

The ESP32-S3 has native USB. Most S3 dev kits expose two USB-C ports:
one connected to the chip's native USB on GPIO 19/20 (used here for
USB-CDC log output and direct upload), and one connected to a CP2102
or CH343 UART-USB bridge (used for upload via the ROM bootloader's
UART path and as a backup serial console).

`ARDUINO_USB_CDC_ON_BOOT=1` in `platformio.ini` routes Arduino's
`Serial` through the native USB port. So `pio device monitor` over
the native-USB cable shows firmware logs without an external bridge.

## Bench validation (Phase 0)

1. **Build and flash** with the board's BOOT button held on the first
   upload. Subsequent uploads auto-reset via native USB.

2. **Power up** with the tracker NOT yet inline (just powered, USB
   cable for monitor). USB-CDC should print roughly:

   ```
   === zerotx-tracker fw 0.6.0-cfg ===
   Phase 5: tracking + USB-CDC config + NVS persistence

   UART1 (cable): RX=GP17 TX=GP18 @ 420000 baud
   UART2 (ELRS):  RX=GP4 TX=GP5 @ 420000 baud
   watchdog: 1s, panic-on-timeout
   --- config ---
   station   lat=-22.9123000 lon=-47.0610000 alt=685.0m
   pan       ref_az=0.00deg range=180.0deg pulse=1000/1500/2000 invert=off flip=on
   tilt      range=180.0deg pulse=1000/1500/2000 invert=off
   --------------
   telem_buffer: 4096 bytes
   byte_pump task running on core 1
   crsf_parser task running on core 0
   servos: pan GP6 ch0 (1000/1500/2000 us), tilt GP7 ch1 (1000/1500/2000 us), 50Hz, 12-bit
   servo_slew task running on core 0
   cmd_parser task running on core 0
   servo self-test: starting sweep
     pan: low / pan: high / pan: center
     tilt: low / tilt: high / tilt: center
   servo self-test: complete
   ready (type 'help' for commands)

   >
   heartbeat uptime=5s frames=0 gps=0 bad_crc=0 dropped=0 no-telem
   ```

   The `>` prompt means the command parser is ready. Type `help`
   for the full list of commands.

3. **Initial calibration** (one-time, per installation):

   ```
   > cfg station -22.4591 -45.4502 1234
   station = -22.4591000 -45.4502000 1234.0m
   > cfg pan_ref 270
   pan_ref = 270.00 deg
   > cfg save
   config saved to NVS
   ```

   Values persist across reboots. Use `cfg show` to inspect at any
   time. `defaults` resets RAM config to compile-time values without
   touching NVS; follow with `cfg save` to actually persist.

4. **Manual aim** for installation alignment (without telemetry):

   ```
   > aim 0 0
   aiming az=0.00 el=0.00 -> pan=1500us tilt=1000us flip=off
   > aim 90 30
   aiming az=90.00 el=30.00 -> pan=2000us tilt=1167us flip=off
   ```

   With telemetry flowing, each successful GPS decode produces a
   GPS log line, a TRK log line, and drives the servos:

   ```
   GPS lat=-22.9101234 lon=-47.0612345 alt=712m spd=14.2km/h hdg=183.45 sats=15
   TRK az=178.32 el=12.45 dist=842m
   ```

   The heartbeat status field becomes `tracking` when telemetry is
   fresh (< 1500ms old) and `hold` when stale.

3. **Connect inline** between the case-side MAX490 and the ELRS TX
   module. Power up the tracker, then power up the daemon and the ELRS
   module. CRSF traffic should flow through transparently:
   joystick movements drive the radio link, telemetry comes back to
   the daemon. No behavior change visible to the daemon.

4. **Bypass test** (validates fall-back): wire the tracker out of the
   path with a hardware bypass jumper (or just unplug the tracker and
   connect the cable-side MAX490 directly to the ELRS UART). The link
   should still work without the tracker. This is the recovery path
   for any tracker firmware fault in the field.

5. **Watchdog test**: the watchdog resets the chip if the byte pump
   ever hangs for more than 1 second. There's nothing in Phase 0 that
   would hang the pump under normal conditions, but the watchdog is
   insurance for Phase 1+ when more code runs in parallel.

## Why pole-end and not case-end?

The daemon could compute az/el from telemetry it already receives and
send servo commands to a separate tracker MCU. We chose pole-end
inline tracking instead because:

- **Autonomy**: the tracker keeps tracking even if the Pi 400 reboots
  mid-flight.
- **No second comms link**: the cable already carries CRSF; the
  tracker reads telemetry it's already forwarding. No need for a
  parallel WiFi or ESP-NOW link to a separate tracker box.
- **No daemon-side code changes**: see "Position in the system" above.

The trade-off is that the tracker MCU sits in the safety-critical RC
control path. Mitigations: byte pump on its own core at top priority,
hardware watchdog, hardware bypass jumper for field recovery.
