#!/usr/bin/env bash
# Build offline OSM vector tiles for ZeroTX map view.
#
# Reads the cached Brazil PBF (downloaded by tools/build-geo.sh),
# extracts a regional bbox with osmium, renders to PMTiles with
# tilemaker. Output is consumed by the daemon's tile route in
# pi/daemon/internal/api/maptiles.go.
#
# Usage:
#     tools/maps/build-osm-tiles.sh                    # São Paulo state (default)
#     tools/maps/build-osm-tiles.sh sp-state           # explicit name (same)
#     tools/maps/build-osm-tiles.sh -                  # use $REGION_BBOX + $REGION_NAME
#
# Output: $HOME/zerotx/maptiles/<region>-osm.pmtiles
#
# Dependencies: osmium-tool, tilemaker (>= 3.0), Brazil PBF in cache.
#
# Adding a new region: append a bbox below in the case-statement.
# Format is "W,S,E,N" in decimal degrees (longitude, latitude order).

set -euo pipefail

REGION="${1:-sp-state}"
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/zerotx"
OUT_DIR="$HOME/zerotx/maptiles"
SOURCE_PBF="$CACHE/brazil-latest.osm.pbf"

mkdir -p "$OUT_DIR"

if [[ ! -f "$SOURCE_PBF" ]]; then
    echo "build-osm-tiles: source PBF not found at $SOURCE_PBF" >&2
    echo "build-osm-tiles: run tools/build-geo.sh first to populate the cache" >&2
    exit 2
fi

# Region selection. Add new entries here as needed.
case "$REGION" in
    sp-state)
        # São Paulo state bbox (W,S,E,N). Conservative bounds that
        # include all of SP plus a few km buffer.
        BBOX="-53.20,-25.40,-44.10,-19.70"
        REGION_NAME="sp-state"
        ;;
    -)
        if [[ -z "${REGION_BBOX:-}" || -z "${REGION_NAME:-}" ]]; then
            echo "build-osm-tiles: with region '-' you must set REGION_BBOX and REGION_NAME env vars" >&2
            exit 2
        fi
        BBOX="$REGION_BBOX"
        ;;
    *)
        echo "build-osm-tiles: unknown region '$REGION'" >&2
        echo "build-osm-tiles: known regions: sp-state" >&2
        echo "build-osm-tiles: or use '-' with REGION_BBOX and REGION_NAME env vars" >&2
        exit 2
        ;;
esac

OUT_PMTILES="$OUT_DIR/${REGION_NAME}-osm.pmtiles"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

EXTRACT_PBF="$WORK/${REGION_NAME}.osm.pbf"

# 1. Extract regional PBF from Brazil source.
echo "build-osm-tiles: extracting region $REGION_NAME ($BBOX) from $SOURCE_PBF"
osmium extract \
    --bbox="$BBOX" \
    --strategy=complete_ways \
    --overwrite \
    -o "$EXTRACT_PBF" \
    "$SOURCE_PBF"

EXTRACT_SIZE_MB=$(du -m "$EXTRACT_PBF" | cut -f1)
echo "build-osm-tiles: extracted PBF size: ${EXTRACT_SIZE_MB} MB"

# 2. Render to PMTiles with tilemaker.
#
# Use the bundled OpenMapTiles config + Lua. Different distros put
# them in different paths:
#   - Debian: /usr/share/tilemaker/
#   - Ubuntu: /usr/share/doc/tilemaker/examples/
#   - From source: $HOME/.local/share/tilemaker/
TM_CONFIG=""
TM_PROCESS=""
for d in \
    /usr/share/tilemaker \
    /usr/share/doc/tilemaker/examples \
    "$HOME/.local/share/tilemaker"
do
    if [[ -f "$d/config-openmaptiles.json" && -f "$d/process-openmaptiles.lua" ]]; then
        TM_CONFIG="$d/config-openmaptiles.json"
        TM_PROCESS="$d/process-openmaptiles.lua"
        break
    fi
done

if [[ -z "$TM_CONFIG" ]]; then
    echo "build-osm-tiles: tilemaker openmaptiles config not found in any of:" >&2
    echo "  /usr/share/tilemaker/" >&2
    echo "  /usr/share/doc/tilemaker/examples/" >&2
    echo "  $HOME/.local/share/tilemaker/" >&2
    echo "build-osm-tiles: ensure 'tilemaker' package provides config-openmaptiles.json and process-openmaptiles.lua" >&2
    exit 3
fi

echo "build-osm-tiles: using tilemaker config from $(dirname "$TM_CONFIG")"

echo "build-osm-tiles: rendering vector tiles (this takes 10-60 minutes for SP state)..."
tilemaker \
    --input "$EXTRACT_PBF" \
    --output "$OUT_PMTILES" \
    --config "$TM_CONFIG" \
    --process "$TM_PROCESS" \
    --bbox "$BBOX"

OUT_SIZE_MB=$(du -m "$OUT_PMTILES" | cut -f1)
echo "build-osm-tiles: done"
echo "build-osm-tiles: output: $OUT_PMTILES (${OUT_SIZE_MB} MB)"
echo ""
echo "Next steps:"
echo "  1. Verify with: pmtiles show $OUT_PMTILES"
echo "  2. When daemon supports PMTiles serving (stage C), point -maptiles-dir at $OUT_DIR"
