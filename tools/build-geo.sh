#!/usr/bin/env bash
# Build the offline place-name database for ZeroTX flight narration.
#
# Downloads a regional OSM extract from Geofabrik (cached on first
# run), filters with osmium-tool to place=* nodes only, and pipes
# the result to pi/daemon/cmd/geobuild for sqlite + R-Tree indexing.
#
# Usage:
#     tools/build-geo.sh                       # Brazil (default)
#     tools/build-geo.sh argentina             # different region
#     tools/build-geo.sh -                     # use $REGION_PBF env var
#
# The output db lands at $HOME/zerotx/geo/places-<region>.db. Daemon
# picks it up via the -geo-db flag.
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
    # Geofabrik's continent/country layout. Brazil is under
    # south-america/, others vary; if the URL 404s, the user can
    # set REGION_PBF directly to a downloaded file.
    URL="https://download.geofabrik.de/south-america/$REGION-latest.osm.pbf"
    echo "build-geo: downloading $URL"
    curl -L --fail -o "$PBF.tmp" "$URL"
    mv "$PBF.tmp" "$PBF"
fi

# 2. Filter to place=* nodes. The list mirrors internal/geo's
#    typeMaxDistanceM keys; cmd/geobuild also filters by this set
#    before insertion so the two stay independent.
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
OPL="$WORK/places.opl"

echo "build-geo: filtering place nodes..."
osmium tags-filter -o "$OPL" -f opl --overwrite "$PBF" \
    n/place=town,village,suburb,neighbourhood,quarter,hamlet,locality,isolated_dwelling,farm,city

# 3. Build sqlite.
echo "build-geo: building sqlite at $OUT_DB"
( cd "$REPO/pi/daemon" && go run ./cmd/geobuild -in "$OPL" -out "$OUT_DB" )

echo "build-geo: done."
ls -lh "$OUT_DB"
