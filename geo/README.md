# Offline reverse-geocoding for ZeroTX flight narration

When the daemon plays a post-flight summary, it can name the places
where significant events happened: "Peak altitude near Vila Industrial,"
"Reached peak distance over Parque do Ibirapuera." This package
builds the offline place-name database that supports that.

The resolution is sub-city. We pull two layers from OpenStreetMap:

**Layer A** — point landmarks:
- `place=*`: town, village, suburb, neighbourhood, quarter, hamlet,
  locality, isolated_dwelling, farm, city, island
- `natural=peak`, `natural=spring`

**Layer B** — area features (stored as bounding-box centers, capped
to 10km diagonal so a 100km park doesn't centroid into a useless
fictional point):
- `leisure=park`, `leisure=stadium`
- `amenity=university`, `amenity=hospital`
- `boundary=protected_area`
- Named `landuse=industrial / residential / commercial`

## How it works

1. `tools/build-geo.sh` downloads a regional OSM extract from
   Geofabrik (Brazil by default).
2. `osmium tags-filter` reduces to the feature types we want
   (referenced nodes are kept by default so way and relation
   geometries can be computed in the next step).
3. `osmium export -f geojsonseq` writes line-delimited GeoJSON,
   one feature per line. Nodes become `Point`, closed ways become
   `Polygon`, multipolygon relations become `MultiPolygon`.
4. `pi/daemon/cmd/geobuild` parses the GeoJSON stream, computes
   a representative point (Point coords for nodes, bbox center
   for polygons), maps OSM tags to our internal taxonomy, and
   writes a sqlite database with two tables: `places` (id, name,
   place_type, lat, lon) and `places_rtree` (R-Tree spatial index).
5. At daemon startup, `internal/geo` opens the sqlite read-only.
   The post-flight narrator queries it for each significant event's
   coordinates and decorates the spoken phrasing accordingly.

## Building

Dependencies: `osmium-tool`, `curl`, `go` (>= 1.21).

```bash
sudo apt install osmium-tool
cd zerotx
tools/build-geo.sh                 # Brazil (default), ~3GB download, ~30-100MB output
tools/build-geo.sh argentina       # other Geofabrik regions work too
```

The PBF is cached at `~/.cache/zerotx/<region>-latest.osm.pbf`. Re-running
without a PBF refresh is fast (only the filter, export, and sqlite build
steps run).

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

OSM data is stable for our purposes. Refresh once a year unless a
local feature you care about is missing or wrong; the build is
idempotent.

## Why not geonames?

geonames is cities-only (~500+ population). For 10-40km flights
that's too coarse: a peak altitude over the next neighbourhood gets
labelled "near $bigCityCenter" and you lose the granularity.

## Why not Nominatim?

A full Nominatim instance is 30-100GB and needs Postgres + RAM. We
fly in the field where internet may be unreliable; lugging a Postgres
service is overkill for a one-string answer per flight event.

## Why not polygons (point-in-polygon)?

For "you flew over $park" precision, we'd want polygon membership
tests rather than centroid+threshold. That requires a geometry
library or SpatiaLite, plus 100-300MB of polygon data. The bbox-
center approach gives us 80% of the perceptible improvement at
under a tenth of the disk footprint and zero new dependencies.
If narration ever feels thin, swapping to polygons is a localized
upgrade in `internal/geo` and `cmd/geobuild`.
