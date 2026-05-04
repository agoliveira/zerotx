#!/usr/bin/env bash
# Download self-hosted fonts for the HUD page. Run once on a machine
# with internet; result lives at pi/daemon/web/hud/fonts/.
#
# Sources:
#   Orbitron variable font: github.com/google/fonts (raw, single TTF)
#   DSEG14 Classic Regular + Bold: github.com/keshikan/DSEG release zip
#
# Both are SIL OFL. Total payload ~250KB, loaded once at HUD page boot.

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
FONTS_DIR="$REPO/pi/daemon/web/hud/fonts"

mkdir -p "$FONTS_DIR"
cd "$FONTS_DIR"

echo "==> downloading fonts into $FONTS_DIR"

# ---- Orbitron (variable font from google/fonts) ----
if [[ ! -s "Orbitron-VariableFont_wght.ttf" ]]; then
    echo "  Orbitron variable font"
    curl -fsSL -o "Orbitron-VariableFont_wght.ttf" \
        "https://raw.githubusercontent.com/google/fonts/main/ofl/orbitron/Orbitron%5Bwght%5D.ttf"
    curl -fsSL -o "Orbitron-OFL.txt" \
        "https://raw.githubusercontent.com/google/fonts/main/ofl/orbitron/OFL.txt"
fi

# ---- DSEG14 Classic (from keshikan/DSEG release zip) ----
# The TTFs are not committed to master; they ship in the release zip.
# Latest release at time of writing: v0.46.
if [[ ! -s "DSEG14Classic-Regular.ttf" || ! -s "DSEG14Classic-Bold.ttf" ]]; then
    echo "  DSEG14 Classic (extracting from release zip)"
    DSEG_VER="0.46"
    DSEG_ZIP_NAME="fonts-DSEG_v$(echo "$DSEG_VER" | tr -d .).zip"
    DSEG_URL="https://github.com/keshikan/DSEG/releases/download/v${DSEG_VER}/${DSEG_ZIP_NAME}"
    TMPZIP="$(mktemp --suffix=.zip)"
    trap 'rm -f "$TMPZIP"' EXIT
    curl -fsSL -o "$TMPZIP" "$DSEG_URL"
    SIZE_KB=$(($(stat -c%s "$TMPZIP") / 1024))
    if [[ "$SIZE_KB" -lt 100 ]]; then
        echo "ERROR: DSEG zip download too small (${SIZE_KB}KB), upstream URL may have changed" >&2
        echo "  URL was: $DSEG_URL" >&2
        exit 1
    fi
    echo "    fetched ${SIZE_KB}KB, extracting"
    # The zip's internal layout is a top-level dir like "DSEG_v46/" containing
    # "DSEG14-Classic/DSEG14Classic-Regular.ttf" etc. We extract just the two
    # files we need, stripping the path so they end up in our fonts dir.
    if ! command -v unzip >/dev/null 2>&1; then
        echo "ERROR: 'unzip' not installed. apt install -y unzip" >&2
        exit 1
    fi
    unzip -j -q "$TMPZIP" \
        "*/DSEG14-Classic/DSEG14Classic-Regular.ttf" \
        "*/DSEG14-Classic/DSEG14Classic-Bold.ttf" \
        "*/LICENSE.txt" -d "$FONTS_DIR" 2>/dev/null
    # Rename license to disambiguate from Orbitron's
    if [[ -f "$FONTS_DIR/LICENSE.txt" ]]; then
        mv "$FONTS_DIR/LICENSE.txt" "$FONTS_DIR/DSEG-OFL.txt"
    fi
fi

echo
echo "==> result:"
ls -lh "$FONTS_DIR"

# Sanity-check sizes. Real font files should be 30KB+.
for f in Orbitron-VariableFont_wght.ttf DSEG14Classic-Regular.ttf DSEG14Classic-Bold.ttf; do
    if [[ ! -s "$FONTS_DIR/$f" ]]; then
        echo "ERROR: $f is missing or empty" >&2
        exit 1
    fi
    SIZE=$(stat -c%s "$FONTS_DIR/$f")
    if [[ "$SIZE" -lt 20000 ]]; then
        echo "WARN: $f is only $SIZE bytes, may be incomplete" >&2
    fi
done

echo "==> all fonts present"
