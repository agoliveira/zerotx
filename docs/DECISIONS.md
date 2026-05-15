# ZeroTX Locked Decisions

Flat list of decisions that should not be re-litigated without explicit reason. One line each. New entries appended as they surface.

## Hardware

- ESP32 drives HUB75 panel: RP2040 3.3V signaling failed at panel input shift registers; level shifters ruled out.
- Mega 2560 is the IO hub, daemon is the brain: keeps Pi GPIO free, isolates real-time IO from Linux scheduling.
- VFD on Mega (vfd.0 subsystem, HD44780 4-bit): originally specced for an RP2040 driver, moved to Mega to consolidate IO.
- Joystick is USB HID to Pi: forwarded to RP2040 over USB-CDC for CRSF generation. Not connected to Mega.
- Trackball removed from the system: the arcade trackball used for GUI manipulation was found to have a busted encoder with no economical repair path. The legacy GUI is fully operable from the Pi 400 keyboard, and the kiosk pages (HUD, map, replay) never required pointer input — they were always WS-stream-driven or keyboard-driven. Removed rather than replaced. Mega pins 11/12 (formerly the ring LEDs) are now free. HAL_EEPROM_VERSION bumped accordingly.
- ELRS TX modules (HappyModel ES900TX, RadioMaster Ranger 2.4GHz) mount externally on poles, cable-connected to the case via bulkheads.

## Firmware and software

- Active-HIGH default project-wide: HAL flag opts individual pins into active-LOW.
- Audio split: pre-baked samples for safety-critical alarms (link loss, failsafe), Piper TTS (en_US-amy-medium) for everything else.
- Spectator SoftAP removed from display firmware: if revived, lives on Pi 400 or a dedicated ESP32 with no panel duties.
- RP2040 CRSF firmware m1.8-wdt: hardware watchdog enabled, no exceptions.
- TAER stick layout is the default and the model file is the source of truth: the daemon reads the throttle channel from the active EdgeTX model file (input names + mix data), it is never hardcoded. AETR is supported too but only via the model file. A hardcoded `ch[2] <= 200` check (legacy AETR assumption) was removed.
- Three-input arming workflow: ARMED requires throttle-low + SF arm key down + SH momentary press, all three concurrent. The momentary is press-only (release doesn't matter); to disarm, SF goes up combined with T-low.
- Pre-flight gate is two-part: operator acknowledgement on the `/status` page (syscheck) plus device-health blockers (devhealth). The only blocking devices are the RP2040 CRSF link and both HDMI kiosk displays; everything else (Mega + subsystems, ESP32 HUB75 panel) is informational and never gates flight. Server-side enforced via `POST /api/v1/syscheck/dismiss` returning 409 when not ready.
- Two VFDs (`vfd.0` and `vfd.1`) on the front panel: both Noritake CU20025ECPB-W1J, both driven by the Mega via HD44780 4-bit. Originally specced as one; doubled for status density (different categories on each).
- 128x64 ST7920 graphic LCD on the Mega panel (alongside the VFDs) renders an artificial horizon HUD. Cool-factor display only: telemetry is already complete on the kiosk HUDs. Loss of the GLCD never blocks flight.
- INAV autolaunch is not tracked by ZeroTX: CRSF telemetry's FlightMode frame doesn't expose autolaunch state distinctly from the regular flight mode (during autolaunch the FC still reports `ANGL` or whatever's active), and the launch-mode switch isn't a reliable signal either since autolaunch is commonly enabled full-time on the FC. Real-time detection would require adding MSP-over-CRSF, which is disproportionate effort for the operational value. Considered and dropped.

## Mechanical and case

- Case is wired-only inside: no internal antennas, no SMA bulkhead passthroughs, no RF shielding concerns for the case itself.
- Case mechanicals settled: sun hoods for LCDs, cable bulkhead connectors, single 12VDC input on a panel-mount jack, front-panel USB layout, ventilation and cooling, power switch, interior status indication via lid LED panels.

## Case-to-pole link

- Default cable configuration is single-wire CRSF over a 5m shielded multi-core cable, terminating directly on the ELRS module. No transceivers, no pole-end electronics. The 470Ω TX series resistor at the case end handles half-duplex contention.
- Extended cable configuration uses RS-422 (MAX490 pair on each end) instead of single-wire CRSF. Required for cable runs longer than ~5m and as the substrate the inline antenna tracker requires. Differential pairs handle long cable runs cleanly where a native UART would suffer noise and length limits.
- Switching between configurations requires no firmware change on the RP2040. GP0/GP1 either drive a single-wire merge through 470Ω or feed a MAX490; the firmware is unchanged.

## Antenna tracker (optional)

- Antenna tracker is pole-end and inline on the wired CRSF path (not daemon-side): keeps tracking autonomous across Pi reboots, requires no second comms link, and is invisible to the daemon. Adding or removing the tracker requires zero daemon-side code changes.
- Tracker is only deployable in the extended cable configuration. Inline byte-pump on a single-wire half-duplex line is not supported.
- Tracker `byte_pump_task` on Core 1 with top priority is the safety floor: it is the only task registered with the hardware watchdog. Tracker logic stalls on Core 0 (parser, math, servo loop, console) cannot panic the wire forwarder.
- Custom tracker over U360GTS or other OSS trackers: ZeroTX's inline-on-wired-CRSF deployment is unusual; existing tracker projects target different topologies (parallel WiFi telemetry, daemon-side commanded tracking, etc.).
- Tracker failsafe is hold-last-position by construction, not a programmed timeout: if no GPS frames arrive, the slew loop simply has no new target, so the gimbal sits where it was. No park-to-home pose.

## Power

- Single 12VDC input on a panel-mount jack: no internal PSU, no internal UPS. The case is power-agnostic from anything upstream of the jack.
- Battery backup is external and operator-supplied: a 12V SLA + charger unit (CCTV-style or equivalent) sits upstream of the case input. Rationale: keeps the case small and field-portable; lets the operator choose runtime by SLA capacity; matches the way the unit will be used (paired with whatever 12V source is on hand at the field, bench, or in transit).
- Two 12V to 5V buck converters inside the case: one feeds the Pi 400, the other feeds the powered USB hub. Separate bucks isolate Pi brownout from hub-side transient loads (joystick hotplug, USB DAC spin-up, MCU resets).
- ELRS module runs direct from the 12V rail. Modules accept up to ~16V, so no module-side regulation is needed. E-stop (NC contacts) sits in series with the module DC feed as the hardware kill path.
