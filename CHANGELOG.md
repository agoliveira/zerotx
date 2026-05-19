# Changelog

This file tracks notable changes going forward. Earlier history lives
in the git log; the bulk of pre-public work was tracked there rather
than here. Append new entries at the top as they land.

## Unreleased

### Pre-flight status page: operator position configuration row

The /status page now surfaces a configuration check for the operator-position sources the recovery view depends on. New row in the Hardware section, "Operator position", driven from `/api/v1/preflight.groundStation.operatorPositionSources`. Three states: green when a Pi-side GPS reader is configured, yellow when only `-site-lat`/`-site-lon` flags are set (recovery bearing depends on the operator not moving from the configured point), red when neither is configured (recovery bearing/distance unavailable if the lost-aircraft view fires).

Not a flight gate: the row is informational. Operators who fly without an operator position source are doing so deliberately, but now they see it on every boot rather than discovering it mid-emergency.

API additions: `groundStation.operatorPositionSources` on the existing `/api/v1/preflight` response. Stable-ordered slice of strings drawn from a closed vocabulary (`"gps"`, `"site"`); empty when neither configured. Older daemons emit no field at all; clients should treat absence as "unknown" rather than "empty."

### Post-flight preserve toggle on the recordings list

The recordings tab on the legacy index UI grows a per-row preserve toggle. Click it to write a `<recording>.db.preserve` sidecar (reason `operator`) and pin the recording against auto-cleanup; click it again to remove the sidecar and let the recording age out normally. Idempotent in both directions.

Backed by two new endpoints, `POST /api/v1/recordings/preserve` and `POST /api/v1/recordings/unpreserve`, both taking `{"name": "<basename>"}`. 404 when the named recording does not exist, 400 on name validation failure, 503 if the daemon is running with `-no-recordings`. `GET /api/v1/recordings` now includes a `preserved` boolean per row so the UI never needs a follow-up query to know which recordings are pinned.

Reason-string vocabulary for the sidecar is closed and documented in DECISIONS.md: `failsafe`, `manual`, `operator`. Downstream tools may discriminate on it; new reasons require an explicit decision before shipping.

### Flight log GPX/KML exporter (`zerotx-export`)

New CLI tool under `tools/zerotx-export/`. Reads a `.db` recording produced by the daemon's recorder and emits GPX 1.1 or KML 2.2 for Google Earth, qgroundcontrol, or any other post-flight analyzer. Self-contained module, builds into `/bin/zerotx-export` via `make tools`.

Default altitude is relative to the takeoff point (first valid GPS sample's altitude is the ground reference; KML uses `altitudeMode=relativeToGround`). Pass `-altitude msl` for raw GPS altitude (KML uses `altitudeMode=absolute`). Timestamps are emitted in the local timezone of the export host.

Exports include the full GPS track plus waypoints for arm (labelled "Takeoff"), disarm ("Landing"), failsafe, RTH transitions, home-set ("Home"), peak-altitude, and peak-distance. Peak waypoints surface their value as a parenthetical (e.g. "Peak altitude (210 m)"). See USER.md §5.4 for usage.

### Lost-aircraft recovery view

The daemon now has a recovery state machine that auto-activates on FC failsafe (`FS`, `!FS`, `!ERR`) and can also be triggered manually from either kiosk. Both kiosks present a recovery-focused view while active.

- **Map kiosk**: top-right panel with pulsing red border, bearing and distance from operator to last-known, frozen-at-trigger snapshot (alt, speed, heading), warning when operator position is from `-site-lat`/`-site-lon` fallback. A big red marker with halo on the map at the last-known position, dashed red bearing line from operator. Dismiss button disabled for the first 5 seconds to prevent reflexive clearing.
- **HUD kiosk**: full-screen red-flashing takeover, "LOST AIRCRAFT / SEE MAP" headline, big amber bearing/distance readouts. Read-only (no dismiss); pointer-events pass through so HUD widgets behind it remain interactive.
- **Manual trigger**: "LOST AIRCRAFT" button on the map kiosk; Ctrl+Alt+R on the HUD; or `POST /api/v1/recovery/trigger`.
- **Dismiss**: map kiosk button after the 5s guard; or `POST /api/v1/recovery/dismiss`.

The state machine exposes `state.recovery` in the WebSocket stream and `/api/v1/recovery` for direct query. While active, fresh GPS samples flow into `state.recovery.lastKnown` so the operator's map view tracks the aircraft if it's drifting back into range.

Any recovery trigger (failsafe or manual) tells the recorder to preserve the in-progress session: a `<recording>.db.preserve` sidecar marker file is written on save-and-rotate, and the cleanup sweep skips any `.db` whose sidecar exists. The sidecar's content is the trigger reason (`failsafe` or `manual`) so the path that fired is recoverable after the fact. See USER.md §7.2 for the full operator procedure.

### HUD pre-flight banner: sunset countdown + wind summary

While disarmed, the HUD's pre-flight banner now surfaces two glanceable readouts from the daemon's cached weather data:

- **SUNSET**: clock time + daylight remaining, or "Xm past" once the sun has set (amber after sunset). Handles polar edge cases ("always up today" / "always down today").
- **WIND**: speed, 8-point compass direction (N/NE/E/SE/S/SW/W/NW), gusts when >= wind speed + 2 km/h, freshness age when cached data is more than 10 minutes old.

Both are decision aids, not gates: the arm sequence is unchanged. Silent when no weather data has been cached yet (typical first-boot-at-field with no internet). Reuses the existing `/api/v1/weather` endpoint; no daemon-side change.

### Repo layout: binaries consolidated

Compiled outputs and downloaded third-party tools now live in two
clearly-named top-level directories instead of being scattered across
four locations.

**Before:**

```
pi/daemon/bin/zerotxd                    Go daemon
tools/<name>/<name>                      Go tools (inline builds)
firmware/crsf/build/*.uf2                firmware artifacts
bin/piper/                               downloaded Piper TTS
voices/                                  downloaded ONNX models
```

**After:**

```
/bin/                ZeroTX's compiled outputs (daemon, tools,
                     firmware .uf2 + .elf)
/third_party/        downloaded tools and data (piper/, voices/)
```

Migration for existing deployments (one-time):

```sh
cd ~/zerotx
mv bin    third_party/piper
mv voices third_party/voices
make clean && make
sudo systemctl daemon-reload && sudo systemctl restart zerotxd
```

The systemd unit's `ExecStart` and `PIPER_BINARY` env paths changed;
`daemon-reload` picks up the new unit. The daemon's `-voices-dir`
flag default now points to `$HOME/zerotx/third_party/voices`, matching
the migration target. The `make tools` target is new; `make` (no args)
now builds daemon + tools + firmware.

