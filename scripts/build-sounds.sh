#!/usr/bin/env bash
#
# build-sounds.sh — generate ZeroTX audio bank from sounds/dictionary.yml
# via the ElevenLabs REST API.
#
# Reads bank definitions (voice ID, model) and per-bank track text from
# the dictionary, calls the ElevenLabs TTS endpoint for each non-empty
# entry, writes MP3s to sounds/<bank>/<name>.mp3.
#
# Hand-recorded overrides at sounds/overrides/<bank>/<name>.{mp3,wav}
# are merged into the bank dir at the end of each pass and always win
# over generated audio.
#
# Usage:
#   ./scripts/build-sounds.sh                              # all banks, incremental
#   ./scripts/build-sounds.sh --force                      # regenerate everything
#   ./scripts/build-sounds.sh --bank pt-pirate             # one bank only
#   ./scripts/build-sounds.sh --bank pt-pirate --track armed   # one track only
#   ./scripts/build-sounds.sh --dry-run                    # show what would generate
#
# Multiple flags can be combined. --force overrides incremental skip.
#
# Empty track text (e.g. unfilled pt-pirate placeholders) is skipped
# silently — no API call, no file. This lets the pirate bank ship with
# placeholders that the operator fills in iteratively.
#
# Dependencies (one-time install on Ubuntu):
#   sudo apt install yq jq curl
#
# Environment:
#   ELEVENLABS_API_KEY    required. Your ElevenLabs API key from
#                         https://elevenlabs.io/app/settings/api-keys

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DICT="$REPO_ROOT/sounds/dictionary.yml"
SOUNDS_DIR="$REPO_ROOT/sounds"
OVERRIDES_DIR="$SOUNDS_DIR/overrides"

ELEVENLABS_API_BASE="https://api.elevenlabs.io/v1/text-to-speech"
ELEVENLABS_OUTPUT_FORMAT="mp3_44100_128"

FORCE=0
ONLY_BANK=""
ONLY_TRACK=""
DRY_RUN=0

# === Parse args ===
while [[ $# -gt 0 ]]; do
  case "$1" in
    --force)
      FORCE=1
      shift
      ;;
    --bank)
      ONLY_BANK="$2"
      shift 2
      ;;
    --track)
      ONLY_TRACK="$2"
      shift 2
      ;;
    --dry-run)
      DRY_RUN=1
      shift
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
for cmd in yq jq curl; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "required command not found: $cmd" >&2
    echo "install with: sudo apt install yq jq curl" >&2
    exit 1
  fi
done

if [[ ! -f "$DICT" ]]; then
  echo "dictionary not found: $DICT" >&2
  exit 1
fi

if [[ $DRY_RUN -eq 0 && -z "${ELEVENLABS_API_KEY:-}" ]]; then
  cat >&2 <<EOF
ELEVENLABS_API_KEY not set in environment.

Get your API key at:
  https://elevenlabs.io/app/settings/api-keys

Then export it (add to ~/.bashrc or ~/.profile to persist):
  export ELEVENLABS_API_KEY='sk_...'

Use --dry-run to preview what would be generated without an API key.
EOF
  exit 1
fi

# === Read bank list ===
BANKS=()
while IFS= read -r line; do
  BANKS+=("$line")
done < <(yq -r '.banks | keys[]' "$DICT")

if [[ ${#BANKS[@]} -eq 0 ]]; then
  echo "no banks found in dictionary" >&2
  exit 1
fi

# === Read track list ===
TRACKS=()
while IFS= read -r line; do
  TRACKS+=("$line")
done < <(yq -r '.tracks | keys[]' "$DICT")

if [[ ${#TRACKS[@]} -eq 0 ]]; then
  echo "no tracks found in dictionary" >&2
  exit 1
fi

echo "dictionary: ${#BANKS[@]} banks, ${#TRACKS[@]} tracks"

# === Generate one bank ===
generate_bank() {
  local bank="$1"
  local out_dir="$SOUNDS_DIR/$bank"
  local override_dir="$OVERRIDES_DIR/$bank"

  # Read voice config for this bank.
  local voice_id model description
  voice_id="$(yq -r --arg b "$bank" '.banks[$b].voice_id // ""' "$DICT")"
  model="$(yq -r --arg b "$bank" '.banks[$b].model // "eleven_multilingual_v2"' "$DICT")"
  description="$(yq -r --arg b "$bank" '.banks[$b].description // ""' "$DICT")"

  if [[ -z "$voice_id" || "$voice_id" == "null" ]]; then
    echo "[$bank] no voice_id configured, skipping"
    return
  fi

  echo
  echo "=== bank: $bank ($description) ==="
  echo "    voice=$voice_id model=$model"

  mkdir -p "$out_dir"

  local generated=0 skipped=0 empty=0 failed=0
  for track in "${TRACKS[@]}"; do
    if [[ -n "$ONLY_TRACK" && "$track" != "$ONLY_TRACK" ]]; then
      continue
    fi

    local out_file="$out_dir/$track.mp3"
    local text
    text="$(yq -r --arg t "$track" --arg b "$bank" '.tracks[$t][$b] // ""' "$DICT")"

    if [[ -z "$text" || "$text" == "null" ]]; then
      empty=$((empty + 1))
      continue
    fi

    if [[ $FORCE -eq 0 && -f "$out_file" ]]; then
      skipped=$((skipped + 1))
      continue
    fi

    if [[ $DRY_RUN -eq 1 ]]; then
      printf "  [dry-run] %-28s -> %q\n" "$track" "$text"
      generated=$((generated + 1))
      continue
    fi

    printf "  %-28s -> %q ... " "$track" "$text"
    if call_elevenlabs "$voice_id" "$model" "$text" "$out_file"; then
      echo "ok"
      generated=$((generated + 1))
    else
      echo "FAILED"
      failed=$((failed + 1))
      rm -f "$out_file"
    fi
  done

  # === Apply overrides ===
  local override_count=0
  if [[ -d "$override_dir" ]]; then
    shopt -s nullglob
    for src in "$override_dir"/*.mp3 "$override_dir"/*.wav; do
      [[ -f "$src" ]] || continue
      local base="$(basename "$src")"
      cp "$src" "$out_dir/$base"
      override_count=$((override_count + 1))
    done
    shopt -u nullglob
  fi

  echo "[$bank] generated=$generated skipped=$skipped empty=$empty failed=$failed overrides=$override_count"
}

# === ElevenLabs API call ===
# Returns 0 on success, non-zero on failure. Writes audio to $out_file.
call_elevenlabs() {
  local voice_id="$1"
  local model="$2"
  local text="$3"
  local out_file="$4"

  local payload
  payload="$(jq -nc \
    --arg text "$text" \
    --arg model "$model" \
    '{text: $text, model_id: $model}')"

  local http_code
  http_code="$(curl -sS -X POST \
    "${ELEVENLABS_API_BASE}/${voice_id}?output_format=${ELEVENLABS_OUTPUT_FORMAT}" \
    -H "xi-api-key: $ELEVENLABS_API_KEY" \
    -H "Content-Type: application/json" \
    -d "$payload" \
    -o "$out_file" \
    -w "%{http_code}" 2>/dev/null || echo "000")"

  if [[ "$http_code" != "200" ]]; then
    # Read the error body that curl wrote to out_file (since the request
    # failed, the body is JSON, not audio).
    if [[ -f "$out_file" ]]; then
      local err
      err="$(jq -r '.detail.message // .detail.status // .detail // .' "$out_file" 2>/dev/null || cat "$out_file")"
      echo
      echo "    HTTP $http_code: $err" >&2
    fi
    return 1
  fi

  # Sanity check: response should be a real MP3, not an empty file
  # or a JSON error that snuck through with HTTP 200.
  if [[ ! -s "$out_file" ]]; then
    echo
    echo "    empty response body" >&2
    return 1
  fi
  if head -c 4 "$out_file" 2>/dev/null | grep -q '^{'; then
    echo
    echo "    response was JSON (likely error masquerading as 200)" >&2
    return 1
  fi
  return 0
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
  echo "dry run complete. set ELEVENLABS_API_KEY and re-run without --dry-run to generate."
else
  echo "done. point the daemon at $SOUNDS_DIR via -sounds-dir to use."
  echo "select a bank with -sounds-lang <bank>  (e.g. -sounds-lang pt-pirate)"
fi
