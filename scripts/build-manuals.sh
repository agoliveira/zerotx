#!/usr/bin/env bash
# Build PDF versions of the canonical ZeroTX manuals via pandoc + xelatex.
# Outputs to docs/manuals/<NAME>.pdf alongside the source markdown.
#
# Source markdown is the canonical form (in-repo, version-controlled);
# generated PDFs are .gitignored and rebuilt on demand.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

# Tool dependencies. Pandoc is the converter, xelatex is the PDF
# backend, rsvg-convert turns the topology SVG into a PDF that
# xelatex can include via \includegraphics.
for cmd in pandoc xelatex rsvg-convert; do
  command -v "$cmd" >/dev/null 2>&1 || die "$cmd not installed. Install with: sudo apt install pandoc texlive-xetex texlive-latex-recommended texlive-fonts-recommended texlive-fonts-extra lmodern librsvg2-bin"
done

cd "$REPO_ROOT"

MANUALS_DIR="docs/manuals"
IMAGES_DIR="docs/images"

# Pre-convert each SVG referenced by the manuals to a side-by-side PDF.
# xelatex's graphicx ships with PDF/PNG/JPG support but not SVG; the
# converted PDFs are transient and cleaned up at the end of this run.
say "Converting SVG images for xelatex"
CONVERTED_SVGS=()
for svg in "$IMAGES_DIR"/*.svg; do
  [[ -f "$svg" ]] || continue
  pdf="${svg%.svg}.pdf"
  rsvg-convert -f pdf -o "$pdf" "$svg"
  CONVERTED_SVGS+=("$pdf")
  printf '    %s -> %s\n' "$svg" "$pdf"
done

# Pandoc passes `.svg` references through to xelatex's includegraphics
# without modification. To pick up the freshly-converted PDFs we rewrite
# the image references on a tmp copy of each manual before invoking
# pandoc. The originals on disk are untouched.
TMP_DIR=$(mktemp -d -t zerotx-manuals.XXXXXX)
trap 'rm -rf "$TMP_DIR"; for f in "${CONVERTED_SVGS[@]:-}"; do rm -f "$f"; done' EXIT

PANDOC_OPTS=(
  --from=gfm+yaml_metadata_block
  --pdf-engine=xelatex
  --toc
  --toc-depth=3
  --highlight-style=tango
  -V geometry:margin=1in
  -V documentclass=report
  -V mainfont="DejaVu Serif"
  -V monofont="DejaVu Sans Mono"
  -V linkcolor=blue
  -V urlcolor=blue
  -V colorlinks=true
  --metadata=date:"$(date +%Y-%m-%d)"
  --resource-path=.:docs:docs/images
)

build_one() {
  local name="$1"
  local title="$2"
  local src="$MANUALS_DIR/${name}.md"
  local out="$MANUALS_DIR/${name}.pdf"
  local tmpsrc="$TMP_DIR/${name}.md"

  [[ -f "$src" ]] || die "$src not found"

  # Rewrite .svg image references to .pdf in the tmp copy.
  sed 's|\.svg)|.pdf)|g' "$src" > "$tmpsrc"

  say "Building $out"
  pandoc "${PANDOC_OPTS[@]}" \
    --metadata=title:"$title" \
    -o "$out" "$tmpsrc"

  printf '    %s (%s)\n' "$out" "$(du -h "$out" | cut -f1)"
}

build_one BUILDER "ZeroTX Builder's Manual"
build_one USER    "ZeroTX User Manual"

say "Done. PDFs are in $MANUALS_DIR/ and not committed (per .gitignore)."
