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
| 1     | CRSF telemetry sniffer on core 0 (parses GPS frames).    | this firmware |
| 2     | Az/el math from aircraft GPS + station GPS.              | pending |
| 3     | LEDC PWM driving 2 servos with slew-rate limiting.       | pending |
| 4     | Glue az/el outputs to servo angles. Failsafe behaviors.  | pending |
| 5     | USB-CDC calibration interface, NVS-stored station coords.| pending |

Phase 0 is the safety floor. The byte pump runs on core 1 at the
highest priority; nothing added in later phases is allowed to block
or starve it. Every Phase 1+ feature must live on core 0.

## Hardware

- **MCU**: ESP32-S3-WROOM-1, N16R8 variant (16MB flash, 8MB octal PSRAM)
- **Cable side**: MAX490 (or MAX3490 if available) RS-422 transceiver
- **ELRS side**: RadioMaster Nomad (or equivalent ELRS TX module) via
  CRSF UART
- **Servos** (Phase 3+): KS-3620 class digital servos, 6V supply rail
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
   === zerotx-tracker fw 0.2.0-parser ===
   Phase 1: byte pump + CRSF telemetry sniffer

   UART1 (cable): RX=GP17 TX=GP18 @ 420000 baud
   UART2 (ELRS):  RX=GP4 TX=GP5 @ 420000 baud
   watchdog: 1s, panic-on-timeout
   telem_buffer: 4096 bytes
   byte_pump task running on core 1
   crsf_parser task running on core 0
   ready

   heartbeat uptime=5s frames=0 gps=0 bad_crc=0 dropped=0
   heartbeat uptime=10s frames=0 gps=0 bad_crc=0 dropped=0
   ...
   ```

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
