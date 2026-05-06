# ZeroTX Locked Decisions

Flat list of decisions that should not be re-litigated without explicit reason. One line each. New entries appended as they surface.

## Hardware

- ESP32 drives HUB75 panel: RP2040 3.3V signaling failed at panel input shift registers; level shifters ruled out.
- Mega 2560 is the IO hub, daemon is the brain: keeps Pi GPIO free, isolates real-time IO from Linux scheduling.
- VFD on Mega (vfd.0 subsystem, HD44780 4-bit): originally specced for an RP2040 driver, moved to Mega to consolidate IO.
- Joystick is USB HID to Pi: forwarded to RP2040 over USB-CDC for CRSF generation. Not connected to Mega.
- Trackball is USB HID to Pi: arcade ball plus 2 USB buttons. LEDs (green/red) driven by Mega via led.trackball subsystem.
- ELRS TX modules (HappyModel ES900TX, RadioMaster Ranger 2.4GHz) mount externally on poles, cable-connected to the case via bulkheads.

## Firmware and software

- Active-HIGH default project-wide: HAL flag opts individual pins into active-LOW.
- Audio split: pre-baked samples for safety-critical alarms (auto-launch faults, link loss, failsafe), Piper TTS (en_US-amy-medium) for everything else.
- Spectator SoftAP removed from display firmware: if revived, lives on Pi 400 or a dedicated ESP32 with no panel duties.
- RP2040 CRSF firmware m1.8-wdt: hardware watchdog enabled, no exceptions.

## Mechanical and case

- Case is wired-only inside: no internal antennas, no SMA bulkhead passthroughs, no RF shielding concerns for the case itself.
- Case mechanicals settled: sun hoods for LCDs, cable bulkhead connectors, dual-rail (13.8V CCTV plus 12V/AC field) power input, front-panel USB layout, ventilation and cooling, power switch, interior status indication via lid LED panels.

## Antenna tracker

- Antenna tracker is pole-end and inline on the wired CRSF path (not daemon-side): keeps tracking autonomous across Pi reboots, requires no second comms link, and is invisible to the daemon. Adding or removing the tracker requires zero daemon-side code changes.
- Tracker byte_pump_task on Core 1 with top priority is the safety floor: it is the only task registered with the hardware watchdog. Tracker logic stalls on Core 0 (parser, math, servo loop, console) cannot panic the wire forwarder.
- Custom tracker over U360GTS or other OSS trackers: ZeroTX's inline-on-wired-CRSF deployment is unusual; existing tracker projects target different topologies (parallel WiFi telemetry, daemon-side commanded tracking, etc.).
- CRSF between case and pole runs over RS-422 (MAX490 pair on each end): differential pairs handle long cable runs cleanly where a native UART would suffer noise and length limits. Also gives the tracker a clean inline insertion point.
- Tracker failsafe is hold-last-position by construction, not a programmed timeout: if no GPS frames arrive, the slew loop simply has no new target, so the gimbal sits where it was. No park-to-home pose.

## Power

- 13.8V CCTV PSU is the primary internal rail: feeds all internal nodes via downstream regulation.
- 12V/AC dual-rail field power input: alternative path for field operation, switches over to internal regulation chain.
