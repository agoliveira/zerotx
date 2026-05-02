#!/usr/bin/env bash
# Build the offline place-name database for ZeroTX flight narration.
#
# Downloads a regional OSM extract from Geofabrik (cached on first
# run), filters with osmium-tool to the tagged features ZeroTX cares
# about, exports each feature as GeoJSON with computed geometry,
# and pipes the result to pi/daemon/cmd/geobuild for sqlite + R-Tree
# indexing.
#
# Feature set (Layer A + B):
#   - place=*: town, village, suburb, neighbourhood, quarter, hamlet,
#     locality, isolated_dwelling, farm, city, island
#   - natural=peak, spring
#   - leisure=park, stadium
#   - amenity=university, hospital
#   - boundary=protected_area
#   - landuse=* with name tag (industrial estates, named residential
#     developments)
#
# Usage:
#     tools/build-geo.sh                       # Brazil (default)
#     tools/build-geo.sh argentina             # different region
#     tools/build-geo.sh -                     # use $REGION_PBF env var
#
# Output: $HOME/zerotx/geo/places-<region>.db
#
# Dependencies: osmium-tool, curl, go (>= 1.21).
#   sudo apt install osmium-tool

set -euo pipefail

REGION="${1:-brazil}"
REPO="$(cd "$(dirname "$0")/.." && pwd)"
CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/zerotx"
OUT_DIR="$HOME/zerotx/geo"
OUT_DB="$OUT_DIR/places-$REGION.db"

mkdir -p "$CACHE" "$OUT_DIR"

# 1. Fetch region PBF.
PBF="$CACHE/$REGION-latest.osm.pbf"
if [[ "$REGION" == "-" ]]; then
    if [[ -z "${REGION_PBF:-}" ]]; then
        echo "build-geo: with region '-' you must set REGION_PBF env var" >&2
        exit 2
    fi
    PBF="$REGION_PBF"
    REGION="$(basename "$PBF" .osm.pbf)"
    OUT_DB="$OUT_DIR/places-$REGION.db"
fi

if [[ ! -f "$PBF" ]]; then
    URL="https://download.geofabrik.de/south-america/$REGION-latest.osm.pbf"
    echo "build-geo: downloading $URL"
    curl -L --fail -o "$PBF.tmp" "$URL"
    mv "$PBF.tmp" "$PBF"
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
FILTERED_PBF="$WORK/filtered.osm.pbf"
GEOJSON="$WORK/features.geojsonseq"

# 2. Tag-filter to the feature classes we care about. Referenced
#    nodes are kept (default) so the next step can compute way and
#    relation geometries.
echo "build-geo: tag-filtering..."
osmium tags-filter --overwrite -o "$FILTERED_PBF" "$PBF" \
    n/place=town,village,suburb,neighbourhood,quarter,hamlet,locality,isolated_dwelling,farm,city,island \
    n/natural=peak,spring \
    w/leisure=park,stadium \
    w/amenity=university,hospital \
    w/landuse=industrial,residential,commercial \
    w/boundary=protected_area \
    r/leisure=park \
    r/boundary=protected_area

# 3. Export with computed geometry as line-delimited GeoJSON. Each
#    feature is one line: a node becomes a Point, a closed way a
#    Polygon, a multipolygon relation a MultiPolygon. The geometry
#    type drives geobuild's centroid logic.
echo "build-geo: exporting GeoJSON..."
osmium export --overwrite -f geojsonseq -o "$GEOJSON" "$FILTERED_PBF"

# 4. Build sqlite.
echo "build-geo: building sqlite at $OUT_DB"
( cd "$REPO/pi/daemon" && go run ./cmd/geobuild -in "$GEOJSON" -out "$OUT_DB" )

echo "build-geo: done."
ls -lh "$OUT_DB"
