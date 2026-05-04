#!/usr/bin/env bash
# Vendor the OpenFreeMap "liberty" style + all referenced assets into
# ~/zerotx/maptiles/ for fully-offline use. Run once on a machine with
# internet; the result is a self-contained directory that the daemon
# serves at /styles/, /fonts/, /sprites/.
#
# Layout produced under MAPTILES_DIR:
#   styles/liberty.json
#   sprites/sprite{,@2x}.{json,png}
#   fonts/<fontstack>/<range>.pbf   (per fontstack referenced by the style)
#
# Usage:
#   tools/maps/fetch-style-liberty.sh [MAPTILES_DIR]
#
# Default MAPTILES_DIR: $HOME/zerotx/maptiles

set -euo pipefail

MAPTILES_DIR="${1:-$HOME/zerotx/maptiles}"

STYLES_DIR="$MAPTILES_DIR/styles"
FONTS_DIR="$MAPTILES_DIR/fonts"
SPRITES_DIR="$MAPTILES_DIR/sprites"
mkdir -p "$STYLES_DIR" "$FONTS_DIR" "$SPRITES_DIR"

UPSTREAM_STYLE="https://tiles.openfreemap.org/styles/liberty"
# OpenFreeMap hosts the exact fontstacks the liberty style references.
# Use that as the canonical source rather than the openmaptiles/fonts
# repo (which doesn't host all the same stacks).
FONTS_BASE="https://tiles.openfreemap.org/fonts"

echo "==> downloading upstream liberty style"
TMP_STYLE="$(mktemp)"
curl -sSL "$UPSTREAM_STYLE" -o "$TMP_STYLE"

# Validate JSON.
python3 -c "import json; json.load(open('$TMP_STYLE'))" \
  || { echo "ERROR: upstream returned non-JSON" >&2; exit 1; }

# Discover sprite + glyphs URLs from the upstream JSON so we don't
# hardcode a specific path that might change.
SPRITE_URL="$(python3 -c "import json; d=json.load(open('$TMP_STYLE')); print(d.get('sprite',''))")"
GLYPHS_URL="$(python3 -c "import json; d=json.load(open('$TMP_STYLE')); print(d.get('glyphs',''))")"
echo "    upstream sprite : $SPRITE_URL"
echo "    upstream glyphs : $GLYPHS_URL"

if [[ -z "$SPRITE_URL" || -z "$GLYPHS_URL" ]]; then
    echo "ERROR: upstream style missing sprite or glyphs key" >&2
    exit 1
fi

# ---- Sprites ----
# MapLibre fetches sprite.json + sprite.png (and optional @2x variants).
echo "==> downloading sprites"
for variant in "" "@2x"; do
    for ext in json png; do
        url="${SPRITE_URL}${variant}.${ext}"
        out="$SPRITES_DIR/sprite${variant}.${ext}"
        if curl -sSLfo "$out" "$url"; then
            echo "    sprite${variant}.${ext} ($(wc -c <"$out") bytes)"
        else
            echo "    sprite${variant}.${ext}: not found upstream, skipping"
            rm -f "$out"
        fi
    done
done

# ---- Fontstacks ----
# Walk the style JSON; collect every distinct text-font value.
echo "==> discovering fontstacks referenced by style"
mapfile -t FONTSTACKS < <(python3 - <<PY
import json
d = json.load(open("$TMP_STYLE"))
seen = set()
def walk(o):
    if isinstance(o, dict):
        for k, v in o.items():
            if k == "text-font":
                # text-font can be ["Stack A", "Stack B"] or
                # ["literal", ["Stack A", "Stack B"]] (expression).
                vals = v
                if isinstance(v, list) and v and v[0] == "literal":
                    vals = v[1] if len(v) > 1 else []
                for s in (vals or []):
                    if isinstance(s, str):
                        seen.add(s)
            else:
                walk(v)
    elif isinstance(o, list):
        for item in o: walk(item)
walk(d.get("layers", []))
# Also check root-level metadata for hints.
for s in sorted(seen):
    print(s)
PY
)

if [[ ${#FONTSTACKS[@]} -eq 0 ]]; then
    echo "WARN: no fontstacks found in style; labels may be invisible"
else
    echo "    fontstacks: ${FONTSTACKS[*]}"
fi

# Download each fontstack's full range PBFs (0-65535 in 256-glyph chunks).
# The openmaptiles/fonts repo has the standard chunks at master.
echo "==> downloading font PBFs from $FONTS_BASE"
for stack in "${FONTSTACKS[@]}"; do
    safe_stack="$(printf '%s' "$stack" | python3 -c "import sys,urllib.parse; print(urllib.parse.quote(sys.stdin.read()))")"
    out_dir="$FONTS_DIR/$stack"
    mkdir -p "$out_dir"
    n_ok=0
    n_404=0
    for start in $(seq 0 256 65280); do
        end=$((start + 255))
        url="${FONTS_BASE}/${safe_stack}/${start}-${end}.pbf"
        out="$out_dir/${start}-${end}.pbf"
        if [[ -s "$out" ]]; then n_ok=$((n_ok+1)); continue; fi
        # --silent --show-errors --fail with output to file. Suppress
        # 404s (expected for unicode ranges that don't exist) and only
        # surface true network errors.
        if curl --silent --fail -o "$out" "$url" 2>/dev/null; then
            n_ok=$((n_ok+1))
        else
            n_404=$((n_404+1))
            rm -f "$out"
        fi
    done
    echo "    $stack: $n_ok ranges fetched, $n_404 not present upstream"
done

# ---- Style JSON rewrite ----
# Rewrite vector source tiles, glyphs, sprite URLs to point at our daemon.
# Origin-relative paths (no host) so the same JSON works on any host.
echo "==> rewriting style JSON to local URLs"
python3 - <<PY
import json
d = json.load(open("$TMP_STYLE"))

# Rewrite sprite + glyphs.
d["sprite"] = "/sprites/sprite"
d["glyphs"] = "/fonts/{fontstack}/{range}.pbf"

# Remove sources that point at external hosts we haven't vendored
# locally. Liberty references openfreemap's natural-earth shaded relief
# at low zooms; we don't need it for FPV at zoom 12+, and leaving it in
# means the page tries to fetch from the internet.
keep_sources = {}
removed = []
for src_name, src in (d.get("sources") or {}).items():
    if src.get("type") == "vector":
        # Local vector tiles via daemon.
        src.pop("url", None)
        src["tiles"] = ["/tiles/osm/{z}/{x}/{y}.pbf"]
        src.setdefault("minzoom", 0)
        src.setdefault("maxzoom", 14)
        src["attribution"] = "© OpenStreetMap contributors"
        keep_sources[src_name] = src
    elif src.get("type") == "raster":
        # Drop external raster sources entirely.
        removed.append(src_name)
    else:
        keep_sources[src_name] = src
d["sources"] = keep_sources
print("    kept sources:", list(keep_sources.keys()))
print("    removed external raster sources:", removed)

# Drop layers that reference removed sources.
before = len(d.get("layers", []))
d["layers"] = [l for l in d.get("layers", []) if l.get("source") not in removed]
after = len(d["layers"])
print(f"    layers: {before} -> {after} (removed {before-after} referencing removed sources)")

with open("$STYLES_DIR/liberty.json", "w") as f:
    json.dump(d, f, indent=2)
print("    wrote $STYLES_DIR/liberty.json")
PY

rm -f "$TMP_STYLE"

echo
echo "Done. Files under $MAPTILES_DIR:"
du -sh "$STYLES_DIR" "$FONTS_DIR" "$SPRITES_DIR"
