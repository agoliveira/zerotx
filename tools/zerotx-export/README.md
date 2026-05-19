# zerotx-export

Convert SQLite flight recordings produced by `zerotxd` into GPX or
KML files for use in Google Earth, qgroundcontrol, or any other
post-flight analyzer.

## Usage

```
zerotx-export -in path/to/flight.db [-out path] [-format gpx|kml] [-altitude relative|msl]
```

Common cases:

```sh
# Default: emits flight.gpx next to the input, altitude relative to takeoff
zerotx-export -in ~/zerotx/recordings/2026-05-19_big-talon.db

# Explicit KML output
zerotx-export -in flight.db -out flight.kml

# True MSL altitudes (for terrain-clearance analysis)
zerotx-export -in flight.db -altitude msl -out flight.gpx

# To stdout (pipe to any GPX tool)
zerotx-export -in flight.db -format gpx > flight.gpx
```

## What's in the export

  * Track: every telemetry sample with a valid GPS fix, ordered by
    time. Each point has lat, lon, altitude, and timestamp.
  * Waypoints: arm (Takeoff), disarm (Landing), failsafe, RTH,
    home-set (Home), peak-altitude, peak-distance. Each is placed
    at the lat/lon of the nearest telemetry sample (or the event's
    own detail blob when it carries position data, like home-set).
  * Metadata: model name, session start time.

## Altitude

Two modes, controlled by `-altitude`:

  * `relative` (default): every emitted altitude is `gps_alt -
    ground_alt`, where `ground_alt` is the first valid GPS
    sample's altitude. Good for Google Earth (terrain mesh handles
    ground; trail floats above it correctly). Good for "how high
    did it fly above launch."
  * `msl`: emit raw GPS altitude (meters above mean sea level).
    Useful for terrain-clearance analysis or comparison to charted
    obstacle heights. KML uses `<altitudeMode>absolute</altitudeMode>`
    in this mode so Google Earth places the trail at true MSL.

## Timestamps

Local timezone. The tool reads `time.Local` at runtime, so flights
recorded in São Paulo and exported on a machine in São Paulo come
out with `-03:00` offsets in RFC3339 timestamps. Both GPX and KML
accept timezone offsets.

## Multi-session recordings

Current daemon design produces one `.db` per arm/disarm cycle, so
recordings are single-session. If a `.db` has multiple sessions
(future-proof), the tool exports the session with the most
telemetry samples and ignores the rest.

## Build

Built into `/bin/zerotx-export` by `scripts/build-tools.sh` or
`make tools` at the repo root.
