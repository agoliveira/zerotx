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

## Power

- 13.8V CCTV PSU is the primary internal rail: feeds all internal nodes via downstream regulation.
- 12V/AC dual-rail field power input: alternative path for field operation, switches over to internal regulation chain.
