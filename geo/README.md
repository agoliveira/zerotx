# Offline reverse-geocoding for ZeroTX flight narration

When the daemon plays a post-flight summary, it can name the places
where significant events happened: "Peak altitude near Vila Industrial,"
"Reached peak distance over Salto." This package builds the offline
place-name database that supports that.

The resolution is sub-city: OSM `place=*` nodes (suburb, neighbourhood,
quarter, hamlet, village, town, city, locality, isolated_dwelling,
farm). Cities are too coarse for short flights, but pure cities-only
datasets like geonames don't have neighbourhood-level features.

## How it works

1. `tools/build-geo.sh` downloads a regional OSM extract from
   Geofabrik (Brazil by default).
2. `osmium-tool` filters the extract to `place=*` nodes only and
   writes them out in OPL format (one feature per line).
3. `pi/daemon/cmd/geobuild` parses the OPL and writes a sqlite
   database with two tables: `places` (id, name, place_type, lat,
   lon) and `places_rtree` (R-Tree spatial index for fast bbox
   queries).
4. At daemon startup, `internal/geo` opens the sqlite read-only.
   The post-flight narrator queries it for each significant event's
   coordinates.

## Building

Dependencies: `osmium-tool`, `curl`, `go` (>= 1.21).

```bash
sudo apt install osmium-tool
cd zerotx
tools/build-geo.sh                 # Brazil (default), ~3GB download, ~30-50MB output
tools/build-geo.sh argentina       # other Geofabrik regions work too
```

The PBF is cached at `~/.cache/zerotx/<region>-latest.osm.pbf`. Re-running
without a PBF refresh is fast (only the filter + sqlite build run).

For non-South-America regions, the script's URL builder won't match.
Workaround: download the PBF yourself and pass:

```bash
REGION_PBF=/path/to/europe-portugal-latest.osm.pbf tools/build-geo.sh -
```

## Output

`~/zerotx/geo/places-<region>.db`. Point the daemon at it:

```bash
zerotxd ... -geo-db ~/zerotx/geo/places-brazil.db
```

If the file is missing or unreadable, the daemon logs a warning at
boot and the post-flight narrator quietly omits location phrases.
The geo dependency is soft.

## Refresh cadence

OSM place data is stable. Refresh once a year unless you've moved
to a region with rapid OSM changes; a stale database produces the
same answers it did last year, which is fine for "near $town."

## Why not geonames?

geonames is cities-only (~500+ population). For 10-40km flights
that's too coarse: a peak altitude over the next neighbourhood gets
labelled "near $bigCityCenter" and you lose the granularity.

## Why not Nominatim?

A full Nominatim instance is 30-100GB and needs Postgres + RAM. We
fly in the field where internet may be unreliable; lugging a Postgres
service is overkill for a one-string answer per flight event.
