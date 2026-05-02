#!/usr/bin/env bash
#
# fetch-voices.sh - download Piper voice models for ZeroTX TTS.
#
# Idempotent: skips files that already exist. Voices are not committed
# to the repo (~130MB total); this script fetches them on first setup
# or after voices/ is wiped.
#
# Voices used by the daemon:
#   en  -> en_US-amy-medium  (rhasspy/piper-voices, female, US English)
#   pt  -> pt_BR-faber-medium (rhasspy/piper-voices, male, Brazilian Portuguese)
#
# Output:
#   voices/en_US-amy-medium.onnx{,.json}
#   voices/pt_BR-faber-medium.onnx{,.json}

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
VOICES_DIR="$REPO_ROOT/voices"

mkdir -p "$VOICES_DIR"

# Each entry: <local-name>|<remote-onnx-url>|<remote-json-url>
voices=(
  "en_US-amy-medium|https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/amy/medium/en_US-amy-medium.onnx|https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/amy/medium/en_US-amy-medium.onnx.json"
  "pt_BR-faber-medium|https://huggingface.co/rhasspy/piper-voices/resolve/main/pt/pt_BR/faber/medium/pt_BR-faber-medium.onnx|https://huggingface.co/rhasspy/piper-voices/resolve/main/pt/pt_BR/faber/medium/pt_BR-faber-medium.onnx.json"
)

fetch_one() {
  local name="$1" url="$2" outfile="$3"
  if [[ -s "$outfile" ]]; then
    echo "  $name: present (skipping)"
    return 0
  fi
  echo "  $name: downloading..."
  if ! curl -fsSL -o "$outfile.partial" "$url"; then
    rm -f "$outfile.partial"
    echo "    FAILED: $url" >&2
    return 1
  fi
  mv "$outfile.partial" "$outfile"
}

failures=0
for entry in "${voices[@]}"; do
  IFS='|' read -r name onnx_url json_url <<<"$entry"
  echo "=== $name ==="
  fetch_one "$name (onnx)" "$onnx_url" "$VOICES_DIR/$name.onnx" || failures=$((failures + 1))
  fetch_one "$name (json)" "$json_url" "$VOICES_DIR/$name.onnx.json" || failures=$((failures + 1))
done

echo
if [[ $failures -gt 0 ]]; then
  echo "done with $failures failure(s)." >&2
  exit 1
fi

echo "done. voices in: $VOICES_DIR"
ls -lh "$VOICES_DIR"/*.onnx 2>/dev/null || true
