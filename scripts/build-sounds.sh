#!/usr/bin/env bash
#
# build-sounds.sh - generate ZeroTX audio bank.
#
# This script reads sounds/dictionary.yml (the public dictionary) and
# optionally sounds/personal.yml (the private overrides, gitignored),
# merges them, and synthesizes audio for every bank x track combination
# that has non-empty text.
#
# === Synthesizer: ElevenLabs (configured) ===
# The synthesize() function below calls the ElevenLabs REST API. This
# is the swappable section. To use a different engine (edge-tts, Piper,
# eSpeak-NG, etc.), replace the body of synthesize(). See sounds/README.md
# for alternatives and tradeoffs.
#
# Required env: ELEVENLABS_API_KEY
#
# The output file requirements (regardless of engine):
#   - Mono audio
#   - 22 kHz sample rate or higher
#   - .mp3, .wav, or .ogg container
#   - Reasonably small (under ~100 KB per clip)
#   - Saved as sounds/<bank>/<track>.mp3 (or .wav, .ogg)
#
# === Bank text inheritance ===
# A bank can declare `text_from: <other-bank>` in its YAML config. If
# a track has no explicit text in this bank, the script falls back to
# the named bank's text. Useful for cloned-voice banks that share the
# standard pt or en wording but use a different voice. Tracks with
# explicit text in the bank still win over the inherited text.
#
# === Usage ===
#   ./scripts/build-sounds.sh                              # all banks, incremental
#   ./scripts/build-sounds.sh --force                      # regenerate all
#   ./scripts/build-sounds.sh --bank en                    # single bank
#   ./scripts/build-sounds.sh --bank en --track armed      # single track
#   ./scripts/build-sounds.sh --dry-run                    # preview only
#
# === Dependencies ===
# Required:
#   sudo apt install yq jq curl

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DICT="$REPO_ROOT/sounds/dictionary.yml"
PERSONAL="$REPO_ROOT/sounds/personal.yml"
SOUNDS_DIR="$REPO_ROOT/sounds"
OVERRIDES_DIR="$SOUNDS_DIR/overrides"

FORCE=0
ONLY_BANK=""
ONLY_TRACK=""
DRY_RUN=0

# === Parse args ===
while [[ $# -gt 0 ]]; do
  case "$1" in
    --force)    FORCE=1; shift ;;
    --bank)     ONLY_BANK="$2"; shift 2 ;;
    --track)    ONLY_TRACK="$2"; shift 2 ;;
    --dry-run)  DRY_RUN=1; shift ;;
    -h|--help)  sed -n '3,/^$/p' "$0" | sed 's/^# \?//'; exit 0 ;;
    *)          echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# === Dependency checks ===
for cmd in yq jq curl; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "required command not found: $cmd" >&2
    echo "install with: sudo apt install yq jq curl" >&2
    exit 1
  fi
done

if [[ $DRY_RUN -eq 0 && -z "${ELEVENLABS_API_KEY:-}" ]]; then
  cat >&2 <<EOF
ELEVENLABS_API_KEY not set in environment.

This build script is configured to use the ElevenLabs API. Get your
key at https://elevenlabs.io/app/settings/api-keys then export it:

  export ELEVENLABS_API_KEY='sk_...'

Use --dry-run to preview without an API key.
EOF
  exit 1
fi

if [[ ! -f "$DICT" ]]; then
  echo "dictionary not found: $DICT" >&2
  exit 1
fi

# === Build merged dictionary ===
# Strategy: produce a single YAML on stdout that's the deep-merge of
# dictionary.yml and personal.yml (if it exists). Use yq for the merge.
# yq's `*+` deep-merge operator unions keys at every level; arrays
# concat. We don't have arrays in the schema so it's a clean key union.
build_merged() {
  if [[ -f "$PERSONAL" ]]; then
    yq -y -s '.[0] * .[1]' "$DICT" "$PERSONAL"
  else
    cat "$DICT"
  fi
}

MERGED_DICT="$(mktemp)"
trap 'rm -f "$MERGED_DICT"' EXIT
build_merged > "$MERGED_DICT"

# === Read bank list from merged dictionary ===
BANKS=()
while IFS= read -r line; do
  BANKS+=("$line")
done < <(yq -r '.banks | keys[]' "$MERGED_DICT")

if [[ ${#BANKS[@]} -eq 0 ]]; then
  echo "no banks found after merge" >&2
  exit 1
fi

# === Read track list ===
TRACKS=()
while IFS= read -r line; do
  TRACKS+=("$line")
done < <(yq -r '.tracks | keys[]' "$MERGED_DICT")

if [[ ${#TRACKS[@]} -eq 0 ]]; then
  echo "no tracks found after merge" >&2
  exit 1
fi

echo "dictionary: ${#BANKS[@]} banks, ${#TRACKS[@]} tracks"
if [[ -f "$PERSONAL" ]]; then
  echo "  (merged with sounds/personal.yml)"
fi

# === Pre-extract all track text into a TSV cache ===
# yq is fast for one-off queries but slow when called hundreds of
# times in a loop. Dump everything once into a tab-separated file
# (track \t bank \t text), then look up via awk. Order of magnitude
# faster than per-track yq invocations.
TEXT_CACHE="$(mktemp)"
trap 'rm -f "$MERGED_DICT" "$TEXT_CACHE"' EXIT
yq -r '
  .tracks
  | to_entries[]
  | .key as $track
  | .value
  | to_entries[]
  | [$track, .key, .value] | @tsv
' "$MERGED_DICT" > "$TEXT_CACHE"

# === SYNTHESIS FUNCTION (swappable) ===
# Replace the body to use a different TTS engine. Inputs:
#   $1 - bank name (e.g. "en", "pt", custom names from personal.yml)
#   $2 - voice_id (from dictionary; may be empty for edge-tts default)
#   $3 - model (from dictionary; may be empty)
#   $4 - text to synthesize
#   $5 - output file path
# Returns 0 on success, non-zero on failure. Must produce a valid
# audio file at $5.
synthesize() {
  local bank="$1"
  local voice_id="$2"
  local model="$3"
  local text="$4"
  local outfile="$5"

  if [[ -z "$voice_id" || "$voice_id" == REPLACE_* ]]; then
    echo "no voice_id for bank $bank" >&2
    return 1
  fi

  local payload http_code
  payload="$(jq -nc --arg text "$text" --arg model "${model:-eleven_multilingual_v2}" \
    '{text: $text, model_id: $model}')"

  http_code="$(curl -sS -X POST \
    "https://api.elevenlabs.io/v1/text-to-speech/${voice_id}?output_format=mp3_44100_128" \
    -H "xi-api-key: $ELEVENLABS_API_KEY" \
    -H "Content-Type: application/json" \
    -d "$payload" \
    -o "$outfile" \
    -w "%{http_code}" 2>/dev/null || echo "000")"

  if [[ "$http_code" != "200" ]]; then
    if [[ -f "$outfile" ]]; then
      local err
      err="$(jq -r '.detail.message // .detail.status // .detail // .' "$outfile" 2>/dev/null || cat "$outfile")"
      echo
      echo "    HTTP $http_code: $err" >&2
    fi
    return 1
  fi

  if [[ ! -s "$outfile" ]] || head -c 4 "$outfile" 2>/dev/null | grep -q '^{'; then
    return 1
  fi
  return 0
}

# === Resolve text_from chain for a bank ===
# Returns the bank to use for text lookup: either the bank itself
# (if it has direct text or no text_from) or the terminal target of
# the text_from chain. Bails on cycles by returning the bank itself.
resolve_text_source() {
  local bank="$1"
  local seen="$bank"
  local current="$bank"

  while true; do
    local next
    next="$(yq -r --arg b "$current" '.banks[$b].text_from // ""' "$MERGED_DICT")"
    if [[ -z "$next" || "$next" == "null" ]]; then
      echo "$current"
      return 0
    fi
    # Cycle protection
    if [[ " $seen " == *" $next "* ]]; then
      echo "$current"
      return 0
    fi
    seen="$seen $next"
    current="$next"
  done
}

# === Pre-resolve text sources for all banks (cache) ===
# Compute once at startup so the per-track loop doesn't re-resolve.
declare -A TEXT_SOURCE
for bank in "${BANKS[@]}"; do
  TEXT_SOURCE[$bank]="$(resolve_text_source "$bank")"
done

# === Generate one bank ===
generate_bank() {
  local bank="$1"
  local out_dir="$SOUNDS_DIR/$bank"
  local override_dir="$OVERRIDES_DIR/$bank"

  local voice_id model description text_from
  voice_id="$(yq -r --arg b "$bank" '.banks[$b].voice_id // ""' "$MERGED_DICT")"
  model="$(yq -r --arg b "$bank" '.banks[$b].model // ""' "$MERGED_DICT")"
  description="$(yq -r --arg b "$bank" '.banks[$b].description // ""' "$MERGED_DICT")"
  text_from="$(yq -r --arg b "$bank" '.banks[$b].text_from // ""' "$MERGED_DICT")"

  echo
  echo "=== bank: $bank ($description) ==="
  if [[ -n "$voice_id" && "$voice_id" != "REPLACE_WITH_EN_VOICE_ID" && "$voice_id" != "REPLACE_WITH_PT_VOICE_ID" ]]; then
    echo "    voice_id=$voice_id model=$model"
  else
    echo "    NOTE: bank has no voice_id; will fail at synthesis."
  fi
  if [[ -n "$text_from" && "$text_from" != "null" ]]; then
    echo "    text inherited from: $text_from"
  fi

  mkdir -p "$out_dir"

  local generated=0 skipped=0 empty=0 failed=0
  for track in "${TRACKS[@]}"; do
    if [[ -n "$ONLY_TRACK" && "$track" != "$ONLY_TRACK" ]]; then
      continue
    fi

    local out_file="$out_dir/$track.mp3"
    local text_source="${TEXT_SOURCE[$bank]}"
    # Try this bank's own text first; if empty fall back to text_source
    # (which is bank itself if no text_from configured).
    local text
    text="$(awk -F'\t' -v t="$track" -v b="$bank" '$1==t && $2==b {print $3; exit}' "$TEXT_CACHE")"
    if [[ -z "$text" && "$text_source" != "$bank" ]]; then
      text="$(awk -F'\t' -v t="$track" -v b="$text_source" '$1==t && $2==b {print $3; exit}' "$TEXT_CACHE")"
    fi

    if [[ -z "$text" || "$text" == "null" ]]; then
      empty=$((empty + 1))
      continue
    fi

    if [[ $FORCE -eq 0 && -f "$out_file" ]]; then
      skipped=$((skipped + 1))
      continue
    fi

    if [[ $DRY_RUN -eq 1 ]]; then
      printf "  [dry-run] %-32s -> %q\n" "$track" "$text"
      generated=$((generated + 1))
      continue
    fi

    printf "  %-32s -> %q ... " "$track" "$text"
    if synthesize "$bank" "$voice_id" "$model" "$text" "$out_file"; then
      if [[ -s "$out_file" ]]; then
        echo "ok"
        generated=$((generated + 1))
      else
        echo "FAILED (empty output)"
        rm -f "$out_file"
        failed=$((failed + 1))
      fi
    else
      echo "FAILED"
      rm -f "$out_file"
      failed=$((failed + 1))
    fi
  done

  # === Apply overrides ===
  local override_count=0
  if [[ -d "$override_dir" ]]; then
    shopt -s nullglob
    for src in "$override_dir"/*.mp3 "$override_dir"/*.wav "$override_dir"/*.ogg; do
      [[ -f "$src" ]] || continue
      local base
      base="$(basename "$src")"
      cp "$src" "$out_dir/$base"
      override_count=$((override_count + 1))
    done
    shopt -u nullglob
  fi

  echo "[$bank] generated=$generated skipped=$skipped empty=$empty failed=$failed overrides=$override_count"
}

# === Main loop ===
for bank in "${BANKS[@]}"; do
  if [[ -n "$ONLY_BANK" && "$bank" != "$ONLY_BANK" ]]; then
    continue
  fi
  generate_bank "$bank"
done

echo
if [[ $DRY_RUN -eq 1 ]]; then
  echo "dry run complete."
else
  echo "done. point the daemon at $SOUNDS_DIR via -sounds-dir."
  echo "select a bank with -sounds-lang <bank>"
fi
