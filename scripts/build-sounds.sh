#!/usr/bin/env bash
#
# build-sounds.sh — generate ZeroTX audio bank from sounds/dictionary.yml.
#
# Reads track names + per-language spoken text from the dictionary,
# invokes Microsoft Edge TTS via the `edge-tts` CLI to synthesise each
# entry, writes MP3s to sounds/<lang>/<name>.mp3.
#
# Hand-recorded overrides at sounds/overrides/<lang>/<name>.{mp3,wav}
# are copied over the generated file at the end of each language pass,
# so a hand-recorded clip always wins over generated audio.
#
# Usage:
#   ./scripts/build-sounds.sh                  # incremental: skip existing
#   ./scripts/build-sounds.sh --force          # regenerate everything
#   ./scripts/build-sounds.sh --lang en        # one language only
#   ./scripts/build-sounds.sh --track armed    # one track only
#
# Multiple flags can be combined. --force overrides incremental skip.
#
# Dependencies (one-time install on Ubuntu):
#   pipx install edge-tts
#   sudo apt install yq          # for YAML parsing
#
# The `yq` here means kislyuk/yq (Python wrapper around jq) which is in
# Ubuntu's apt. The Go-based mikefarah/yq works too with slightly
# different syntax — this script uses the kislyuk variant since it's the
# Ubuntu default.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DICT="$REPO_ROOT/sounds/dictionary.yml"
SOUNDS_DIR="$REPO_ROOT/sounds"
OVERRIDES_DIR="$SOUNDS_DIR/overrides"

FORCE=0
ONLY_LANG=""
ONLY_TRACK=""

# === Parse args ===
while [[ $# -gt 0 ]]; do
  case "$1" in
    --force)
      FORCE=1
      shift
      ;;
    --lang)
      ONLY_LANG="$2"
      shift 2
      ;;
    --track)
      ONLY_TRACK="$2"
      shift 2
      ;;
    -h|--help)
      sed -n '3,/^$/p' "$0" | sed 's/^# \?//'
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

# === Dependency checks ===
if ! command -v edge-tts >/dev/null 2>&1; then
  cat >&2 <<EOF
edge-tts not found on PATH.

Install with:
  pipx install edge-tts

If you don't have pipx:
  sudo apt install pipx
  pipx ensurepath
EOF
  exit 1
fi

if ! command -v yq >/dev/null 2>&1; then
  cat >&2 <<EOF
yq not found on PATH.

Install with:
  sudo apt install yq

(This script uses the kislyuk/yq variant from Ubuntu apt. The Go-based
mikefarah/yq has different syntax and won't work without changes.)
EOF
  exit 1
fi

if [[ ! -f "$DICT" ]]; then
  echo "dictionary not found: $DICT" >&2
  exit 1
fi

# === Read voice config ===
VOICE_EN="$(yq -r '.voices.en' "$DICT")"
VOICE_PT="$(yq -r '.voices.pt' "$DICT")"

if [[ "$VOICE_EN" == "null" || "$VOICE_PT" == "null" ]]; then
  echo "dictionary missing voices.en or voices.pt" >&2
  exit 1
fi

# === Track list ===
# yq's `keys` operator returns track names; we strip the JSON quoting.
TRACKS=()
while IFS= read -r line; do
  TRACKS+=("$line")
done < <(yq -r '.tracks | keys[]' "$DICT")

if [[ ${#TRACKS[@]} -eq 0 ]]; then
  echo "no tracks found in dictionary" >&2
  exit 1
fi

echo "dictionary: ${#TRACKS[@]} tracks, voices: en=$VOICE_EN pt=$VOICE_PT"

# === Generate per language ===
generate_lang() {
  local lang="$1"
  local voice="$2"
  local out_dir="$SOUNDS_DIR/$lang"
  local override_dir="$OVERRIDES_DIR/$lang"
  mkdir -p "$out_dir"

  local generated=0 skipped=0 failed=0
  for track in "${TRACKS[@]}"; do
    if [[ -n "$ONLY_TRACK" && "$track" != "$ONLY_TRACK" ]]; then
      continue
    fi

    local out_file="$out_dir/$track.mp3"

    # Incremental skip (unless --force).
    if [[ $FORCE -eq 0 && -f "$out_file" ]]; then
      skipped=$((skipped + 1))
      continue
    fi

    # Read the spoken text for this track + language. yq's --arg
    # keeps the value safely separate from the path expression.
    local text
    text="$(yq -r --arg t "$track" --arg l "$lang" '.tracks[$t][$l] // ""' "$DICT")"
    if [[ -z "$text" || "$text" == "null" ]]; then
      echo "  $track [$lang]: no text in dictionary, skipping"
      continue
    fi

    printf "  %-28s [%s] -> %q ... " "$track" "$lang" "$text"
    if edge-tts --voice "$voice" --text "$text" --write-media "$out_file" 2>/dev/null; then
      echo "ok"
      generated=$((generated + 1))
    else
      echo "FAILED"
      failed=$((failed + 1))
      rm -f "$out_file" # clean up half-written file
    fi
  done

  # === Apply overrides (hand-recorded clips win) ===
  local override_count=0
  if [[ -d "$override_dir" ]]; then
    shopt -s nullglob
    for src in "$override_dir"/*.mp3 "$override_dir"/*.wav; do
      [[ -f "$src" ]] || continue
      local base
      base="$(basename "$src")"
      cp "$src" "$out_dir/$base"
      override_count=$((override_count + 1))
    done
    shopt -u nullglob
  fi

  echo "[$lang] generated=$generated skipped=$skipped failed=$failed overrides_applied=$override_count"
}

if [[ -z "$ONLY_LANG" || "$ONLY_LANG" == "en" ]]; then
  generate_lang en "$VOICE_EN"
fi
if [[ -z "$ONLY_LANG" || "$ONLY_LANG" == "pt" ]]; then
  generate_lang pt "$VOICE_PT"
fi

echo
echo "done. point the daemon at $SOUNDS_DIR via -sounds-dir to use."
