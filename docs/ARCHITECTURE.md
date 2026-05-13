# ZeroTX Architecture

## Purpose and scope

High-level architecture of ZeroTX: a workstation-grade ground control station for FPV and fixed-wing flight. Audience is future-me coming back to the project after a long break. Goal: rebuild the mental model fast.

For wire-level protocols see `docs/protocols/`. For per-firmware detail follow the links at the end. This doc deliberately avoids duplicating either.

![ZeroTX ground control station, opened](images/zerotx-render.png)

## System overview

The Raspberry Pi 400 is the brain. It runs the `zerotxd` Go daemon and two Chromium kiosk browsers (HUD and Map). The daemon ingests CRSF telemetry coming back from the radio link via the RP2040 over USB-CDC, drives twin LCDs via HDMI, orchestrates a HUB75 LED panel via an ESP32 satellite, talks to a Mega 2560 IO board for buttons, LEDs, relays, and the VFD, plays audio (pre-baked samples plus Piper TTS), and sends joystick-derived channel intents to the RP2040 which generates CRSF on a wired link to the externally-mounted ELRS TX module. The case interior is wired-only; ELRS modules and any other RF live on external poles.

The default case-to-pole link is a 5m single-wire CRSF cable carrying signal, signal ground, 12V, and power ground, terminating directly on the ELRS module's CRSF pin. No transceivers, no pole-end electronics. An optional extended configuration adds RS-422 transceivers and a pole-end project box; that configuration is documented separately at the end of this file and is what enables the inline antenna tracker.

```mermaid
flowchart LR
    subgraph EXT["External pole"]
        ELRS["ELRS TX module"]
    end

    subgraph PI["Raspberry Pi 400"]
        DAEMON["zerotxd (Go)"]
        HUDB["HUD browser"]
        MAPB["Map browser"]
    end

    subgraph MCU["MCU satellites"]
        MEGA["Mega 2560<br/>IO hub"]
        ESP32["ESP32<br/>panel driver"]
        RP["RP2040<br/>CRSF endpoint"]
    end

    subgraph FRONT["Front panel"]
        HUDL["HUD LCD"]
        MAPL["Map LCD"]
        PANEL["HUB75 128x32"]
        VFD["VFD 20x2 (x2)"]
        GLCD["GLCD 128x64<br/>(artificial horizon)"]
        TB["Trackball + buttons"]
        JS["USB joystick"]
    end

    DAEMON --- HUDB
    DAEMON --- MAPB
    HUDB -->|HDMI| HUDL
    MAPB -->|HDMI| MAPL
    DAEMON <-->|USB-CDC| MEGA
    DAEMON <-->|USB-CDC| ESP32
    DAEMON <-->|USB-CDC| RP
    RP <-->|"single-wire CRSF<br/>(manga 5m)"| ELRS
    MEGA --> VFD
    MEGA --> GLCD
    MEGA -->|"LED ring"| TB
    ESP32 --> PANEL
    JS -->|USB HID| DAEMON
    TB -->|USB HID| DAEMON
```

## Components

### Raspberry Pi 400 (brain)
Runs `zerotxd` and two Chromium kiosk browsers. Owns the joystick, trackball HID, LCDs, and the satellite USB-CDC links. See `pi/daemon/`.

### Mega 2560 (IO hub)
Drives VFD, trackball ring LEDs (bicolor green/red), 4 buttons, 4 LEDs, 4 relays, 16-pixel WS2813 strip, LDR, passive piezo buzzer, KY-040 rotary encoder. Active-HIGH default with HAL-flag opt-in for active-LOW per pin. Single shared serial link to daemon. See `firmware/io/README.md`.

### ESP32 (HUB75 panel driver)
Drives 2x Waveshare P2.5 64x32 panels chained, 128x32 logical resolution. USB-CDC link to Pi. RP2040 was attempted earlier and rejected (3.3V signaling insufficient at panel input shift registers); level shifters explicitly ruled out. See `firmware/display/README.md`.

### RP2040 (CRSF endpoint)
Bidirectional CRSF on the wire side, USB-CDC to the Pi on the host side. Outbound: receives joystick-derived channel intents from the daemon, generates CRSF frames. Inbound: receives CRSF telemetry coming back from the link, forwards frames to the daemon. The wire side connects directly to the case-to-pole cable; in default configuration this is a single-wire half-duplex link to the ELRS module (TX merged into RX through a series resistor at the case end). Hardware watchdog enabled (firmware m1.8-wdt). See `firmware/crsf/README.md`.

### ESP32-S3 (antenna tracker, optional)
Pole-end add-on. Sits inline on the wired CRSF path between the cable's pole-end MAX490 and the ELRS TX module's CRSF UART, byte-pumps frames transparently in both directions on Core 1 at top priority (the safety floor), parses CRSF GPS telemetry on Core 0, computes az/el to the aircraft, and drives a 2-DOF pan/tilt gimbal autonomously. Daemon-unaware: the case-side stack does not know the tracker exists, and removing it (or hardware-bypassing the cable past it) requires zero daemon changes. Failsafe is hold-last-position by construction. Requires the extended cable configuration described below. See `firmware/tracker/README.md`.

### ELRS TX module
HappyModel ES900TX or RadioMaster Nomad/Ranger, mounted externally on a pole. In default configuration the case-to-pole cable terminates directly on the module's CRSF pin and 12V input. In the optional extended configuration described below the module shares a project box with the antenna tracker; in either case the case-side stack treats the module as the same CRSF endpoint and is unchanged.

### LCDs
Two HDMI panels driven by the Pi's two HDMI ports. Each runs a Chromium kiosk pointed at a daemon-served web UI (HUD on one, Map on the other).

### HUB75 panel
At-a-glance state display: arm state, mode, alarms, big numerics. 2x Waveshare P2.5 64x32 chained. Wire protocol in `docs/protocols/display.md`.

### VFD (Noritake CU20025ECPB-W1J)
20x2 blue/white VFD. Driven by Mega via the vfd.0 and vfd.1 subsystems (HD44780 4-bit interface). Two instances coexist on the panel; originally specced for an RP2040 driver, moved to Mega to consolidate IO.

### 128x64 graphic LCD (ST7920)
Small monochrome graphic LCD next to the VFDs on the front panel. Driven by the Mega via 3-wire serial (hardware SPI), the `glcd` Mega subsystem renders a cool-factor artificial horizon (pitch ladder, roll scale, numeric readout) from attitude telemetry the daemon pushes at ~10 Hz. Falls back to a "NO LINK" screen when attitude is stale. Not on the safety path; loss of the GLCD doesn't block flight.

### Trackball + buttons
Arcade trackball plus 2 USB buttons. USB HID to Pi. Ring LEDs (green and red) driven by Mega via the led.trackball subsystem.

### Joystick
USB HID to Pi (Thrustmaster). Read by the `joystick` subsystem, forwarded to RP2040 over USB-CDC.

### Audio stack
ALSA out from the Pi. Two tiers: pre-baked WAV samples for safety-critical alarms, Piper TTS (en_US-amy-medium) for everything else. See `audio`, `narrator`, `phrasebook`.

### Web UIs
HUD and Map browsers, served by daemon out of `web/`. Shared CSS palette and self-hosted Orbitron variable + DSEG14 Classic fonts.

### Off-cluster
GL-MT6000 Flint 2 router (dual WAN). Home Ubuntu server "stan" running KVM/QEMU with OpenBeken/Home Assistant. Stan also hosts the satellite tile build pipeline and (pinned for later) replay datahub V2.

## Data flows

### Telemetry pipeline

The ELRS TX module emits CRSF telemetry on its UART. In default configuration the telemetry crosses the direct cable to the RP2040 over its CRSF UART; in the extended configuration the pole-end tracker passes it through transparently while sniffing GPS frames to drive its gimbal, then it crosses the RS-422 cable to the case. Either way it reaches the RP2040 and from there flows to the daemon over USB-CDC. The `source` subsystem reads frames from the RP2040 link; `telemetry` parses them into structured state. Downstream consumers: `api` (WebSocket push to web UIs), `devices/display` (HUB75 panel commands), `narrator` (audio events), `vfd` (status text), `recorder` (flight log).

```mermaid
sequenceDiagram
    participant ELRS as ELRS TX
    participant RP as RP2040
    participant SRC as source
    participant TEL as telemetry
    participant API as api
    participant DSP as devices/display
    participant NAR as narrator
    participant VFD as vfd
    participant WEB as Web UIs
    participant PNL as ESP32 panel
    participant SPK as ALSA

    ELRS->>RP: CRSF telemetry over case-to-pole cable
    RP->>SRC: frames over USB-CDC
    SRC->>TEL: raw frames
    TEL->>TEL: parse, update state
    TEL->>API: state updates
    TEL->>DSP: state updates
    TEL->>NAR: events (alarm, mode change)
    TEL->>VFD: status text updates
    API-->>WEB: WebSocket push
    DSP->>PNL: panel commands
    NAR->>SPK: sample or TTS audio
    VFD-->>VFD: render via iohub to Mega
```

### Joystick to radio

USB HID joystick events flow through `joystick` into `logic`, mixed against the active aircraft profile from `model`, then sent to the RP2040 over USB-CDC. The RP2040 builds CRSF frames and emits them onto the wire. In default configuration they travel as single-wire half-duplex CRSF directly to the ELRS module's CRSF pin; in extended configuration they cross the RS-422 cable to the pole and pass transparently through the tracker's byte-pump before arriving at the same module pin.

```mermaid
sequenceDiagram
    participant STK as USB joystick
    participant JS as joystick
    participant LOG as logic
    participant MDL as model
    participant RP as RP2040
    participant ELRS as ELRS TX

    STK->>JS: HID events
    JS->>LOG: axis/button state
    MDL-->>LOG: profile (mixes, expo, limits)
    LOG->>RP: channel values (USB-CDC)
    RP->>RP: build CRSF frame
    RP->>ELRS: CRSF over case-to-pole cable
```

### IO events

Mega events (button presses, encoder ticks, LDR readings, etc.) flow over a single shared serial link. The `iohub` subsystem multiplexes the link; downstream subsystems (`vfd`, `trackballled`, future semantic consumers) subscribe to their slice. Outbound effects (LED states, VFD writes, relay commands) go back through `iohub`.

### Panel orchestration

`devices/display` owns the HUB75 panel state model: IDLE, PREFLIGHT, FLIGHT, ALARM, RTH, POSTFLIGHT. It writes commands over USB-CDC to the ESP32 using the `panel` subsystem's protocol writer. Wire grammar and alarm levels in `docs/protocols/display.md`.

## Daemon subsystem map

`pi/daemon/internal/`:

- `api`: HTTP plus WebSocket API for web UIs and external clients
- `arm`: arm state machine, gates flight-critical actions
- `astro`: sun/moon position helpers (golden hour, civil twilight)
- `audio`: ALSA playback engine for samples and Piper output
- `cf`: control-flow helpers (debouncing, edge detection)
- `crsftee`: CRSF passthrough (ground-side splitter)
- `devhealth`: per-device liveness registry; gates preflight Ready on RP2040 + HDMI displays
- `devices/display`: HUB75 panel mode/alarm orchestration
- `geo`: geographic helpers (great circle, bearing, distance)
- `glcd`: 128x64 ST7920 graphic LCD driver (artificial horizon HUD on Mega)
- `gps`: optional Pi-attached NMEA receiver for station position
- `hdmihealth`: scans `/sys/class/drm` for connected HDMI displays
- `heartbeat`: drives a Pi GPIO LED as a daemon-alive indicator
- `iohub`: shared serial client multiplexing access to the Mega IO board
- `ipc`: COBS+CRC framed link to the RP2040 over USB-CDC
- `joystick`: USB HID joystick reader
- `lcd`: 20x4 character LCD on Mega via I2C PCF8574 backpack
- `logbuf`: ring buffer for log lines, exposed via API
- `logic`: cross-cutting orchestration glue
- `mapper`: channel intent computation from joystick + model + arm state
- `model`: aircraft profile loader (yaml in `configs/`)
- `narrator`: Piper TTS scheduler and playback queue
- `netclass`: network classification (link health, etc.)
- `panel`: HUB75 panel wire protocol writer
- `phrasebook`: catalog of pre-baked samples and TTS templates
- `recorder`: flight recording (telemetry plus events)
- `servo`: servo subsystem (driven via iohub, Mega-hosted)
- `sitl`: Software In The Loop integration for bench testing
- `source`: telemetry source abstraction (real ELRS, SITL, replay)
- `syscheck`: operator-acknowledgement pre-flight gate (kiosks land here on boot)
- `telemetry`: telemetry frame parser and state model
- `tilewarm`: map tile prefetcher around current position
- `trackballled`: bicolor ring LED driver (consumes `iohub`)
- `vfd`: VFD driver supporting two instances vfd.0, vfd.1 (consumes `iohub`)
- `weather`: weather data fetcher
- `wxalert`: weather-derived alerts

Auxiliary binaries in `pi/daemon/cmd/`:

- `zerotxd`: the daemon
- `disptest`: HUB75 panel test harness
- `geobuild`: offline geographic data builder
- `zerotx-axes`: joystick axis calibration
- `zerotx-inspect`: live state inspector

## Arm subsystem

The `arm` subsystem in `pi/daemon/internal/arm/` is the gatekeeper for flight-critical actions. The state machine itself is the source of truth and changes more often than this doc — see the source for the canonical state list. What's worth pinning here is the **three-input arming workflow**, which is settled and won't change without a re-design:

- **Throttle low** (T-low): the throttle stick must be at minimum. Read from the joystick by the `logic` package, derived against the model's TAER channel layout (the throttle channel index is read from the EdgeTX model file, not hardcoded).
- **Arm key** (SF switch on the joystick or panel): the operator-held two-position switch that gates the arm sequence. **Up** position is the "arm requested" signal (RP2040 firmware emits `state=1` for UP, `state=0` for DOWN, regardless of GPIO wiring polarity — see `firmware/crsf/src/input_arm.h`).
- **Confirm** (SH momentary, panel-mounted): a momentary press emitted by the RP2040 over IPC `MsgInputEvent` (input id `0x02`), wired to GPIO 15 on the RP2040 (internal pull-up, switch to GND).

All three must be present to transition to ARMED. The momentary is a **press-only** signal — releasing doesn't matter; once consumed, the arm key must be cycled to re-arm. Disarm is the inverse handshake: SF-down combined with T-low brings the state back to DISARMED.

### State diagram

Three states, captured directly from `pi/daemon/internal/arm/arm.go`:

```mermaid
stateDiagram-v2
    [*] --> DISARMED

    DISARMED --> ARMING_REQUESTED : SF up<br/>EventArmingRequested
    ARMING_REQUESTED --> DISARMED : SF down<br/>EventArmingCancelled
    ARMING_REQUESTED --> DISARMED : 60 s timeout<br/>EventArmingTimeout
    ARMING_REQUESTED --> ARMED : SH press<br/>+ T-low + FC ready + checklist OK<br/>EventArmed
    ARMING_REQUESTED --> ARMING_REQUESTED : SH press, gate fails<br/>EventArmDenied{Throttle, NotReady, Checklist}
    ARMED --> DISARMED : SF down + T-low<br/>EventDisarmed
    ARMED --> ARMED : SF down + T-non-zero<br/>EventDisarmDeniedInFlight
```

A few invariants worth pinning, none of which are reflected in the diagram alone:

- **Denial precedence on Confirm** (when in ARMING_REQUESTED): throttle is checked first, then FC-readiness, then checklist. The most visceral safety signal wins the operator's audio cue. The package has explicit tests pinning this ordering — don't reorder casually.
- **No state change is possible without an explicit transition trigger.** `ThrottleChanged`, `FCReadyChanged`, and `ChecklistOkChanged` are cache updates; they are only consulted at decision points. Telemetry flapping during `ARMING_REQUESTED` does NOT auto-cancel — the operator decides via key flip or by pressing confirm during a flap-low (which yields `EventArmDeniedNotReady` and lets them retry).
- **Boot-time `Init` with key already UP** does not transition the state to `ARMING_REQUESTED`. The machine stays `DISARMED` and emits `EventBootKeyUp` once as a warning to the operator (and via narrator audio). The arm key must be cycled to actually arm — preventing an "armed at power-on" footgun.
- **In-flight disarm is intentionally blocked.** From `ARMED` with throttle non-zero, flipping the key down emits `EventDisarmDeniedInFlight` and stays armed. Operators use the FC's failsafe or the mushroom emergency stop for in-flight aborts; the daemon-side gate is for ground handling, not flight termination.

## Pre-flight readiness

The daemon gates flight on two pre-flight signals before the operator can leave the boot-time `/status` page:

- **Operator acknowledgement** (`syscheck` subsystem): both kiosks land on `/status?dest=hud` and `/status?dest=map` at boot. The operator reviews the checklist and clicks "Proceed to flight"; both kiosks then navigate to `/hud` and `/map`. Process-local: the gate resets on every daemon restart so a Pi reboot brings the operator back to the check.
- **Device health** (`devhealth` subsystem): tracks per-device liveness via `LastSeen` timestamps. Two device classes block flight:
  - **RP2040 CRSF link**: refreshed on every `MsgHeartbeat` (~200 ms). 500 ms timeout.
  - **HDMI kiosk displays**: polled every 5 s against `/sys/class/drm/card*-HDMI-*/status`. Both must report `connected`. The `hdmihealth` package owns the scan; `devhealth` owns the registry.

Everything else (Mega + its subsystems including GLCD, VFDs, buttons, encoder, LEDs, trackball ring, WS strip, LDR, relays; ESP32 HUB75 display) is tracked but **informational only** — surfaced on the status page so the operator sees what's connected, never gating flight. A dead VFD is annoying but flyable.

Server-side enforcement: even if the UI's button-disable misses a race, the `POST /api/v1/syscheck/dismiss` endpoint returns HTTP 409 when preflight is not ready, with the blockers list in the response body.

## Audio architecture

Two tiers, picked at the call site:

1. Pre-baked samples: WAV files for safety-critical alarms (link loss, failsafe). Played immediately, no synthesis latency. Catalog managed by `phrasebook`.
2. Piper TTS: `en_US-amy-medium` voice, used for non-critical narration (mode changes, weather alerts, status announcements). Synthesized on demand by `narrator` and queued through `audio`.

Both tiers share the same ALSA output. Sample-tier requests preempt the TTS queue when needed.

## Optional: extended cable configuration with pole-end project box

The default single-wire cable handles a 5m run from the case directly to a pole-mounted ELRS module. Two situations require the extended configuration: cable runs longer than 5m where single-ended TTL stops being viable, and use of the inline antenna tracker.

In the extended configuration the case-to-pole cable carries an RS-422 differential pair instead of single-wire CRSF. A MAX490 transceiver at each end converts between the RP2040's TTL UART and the differential cable. The pole end terminates inside a project box that holds the pole-end MAX490, the ELRS TX module, an optional ESP32-S3 antenna tracker, downstream bucks for servos and logic, and the pan/tilt servos themselves. When the tracker is present it sits inline on the wire between the pole-end MAX490 and the ELRS module, byte-pumping frames transparently and sniffing GPS telemetry to drive the gimbal.

```mermaid
flowchart LR
    subgraph CASE["Case end"]
        RP["RP2040<br/>CRSF endpoint"]
        TXC["MAX490<br/>(case)"]
    end

    subgraph BOX["Pole-end project box"]
        TXP["MAX490<br/>(pole)"]
        TRK["ESP32-S3 Tracker<br/>(optional, inline byte-pump)"]
        ELRS["ELRS TX module"]
        SERVOS["Pan/tilt servos"]
    end

    RP <-->|"CRSF UART<br/>(TTL)"| TXC
    TXC <-->|"RS-422<br/>(A/B pair, cable)"| TXP
    TXP <-->|"CRSF UART"| TRK
    TRK <-->|"CRSF UART"| ELRS
    TRK --> SERVOS
```

The tracker is daemon-unaware and the case-side stack is identical to the default configuration. Removing the tracker (or hardware-bypassing the cable past it) requires zero daemon changes; the pole-end MAX490 simply talks to the ELRS module directly.

Wiring detail for both configurations is in `docs/CONNECTIONS.md`.

## See also

- `docs/protocols/display.md`: HUB75 panel wire protocol
- `firmware/display/README.md`: ESP32 panel firmware
- `firmware/io/README.md`: Mega IO board firmware and HAL
- `firmware/tracker/README.md`: ESP32-S3 antenna tracker firmware
- `firmware/crsf/README.md`: CRSF endpoint firmware
- `docs/CONNECTIONS.md`: physical wiring and topology
- `docs/OPERATIONS.md`: launch and recovery procedures
- `docs/BOOTSTRAP.md`: bare-metal Pi 400 provisioning
- `docs/DECISIONS.md`: locked architectural decisions
- `docs/ROADMAP.md`: pinned and backlog items

## Glossary

- **CRSF**: Crossfire serial protocol, used by ELRS for radio link control and telemetry
- **CPPM**: Combined PPM, multiplexed RC signal on a single wire
- **MAVLink**: telemetry and command protocol used by ArduPilot and INAV
- **ELRS**: ExpressLRS, open-source long-range radio link
- **HUB75**: shift-register based RGB LED panel interface
- **VFD**: Vacuum Fluorescent Display
- **GCS**: Ground Control Station
- **RTH**: Return To Home (autopilot mode)
- **SITL**: Software In The Loop (simulated flight for bench testing)
- **HAL**: Hardware Abstraction Layer (firmware-side pin and flag layer in `firmware/io/`)
