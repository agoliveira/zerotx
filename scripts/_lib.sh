#!/usr/bin/env bash
# Common helpers sourced by other scripts in this directory.
# Not intended to be executed directly.

# Repo root regardless of where the caller cd'd to.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export REPO_ROOT

# Output and dependency conventions:
#
#   $BIN_DIR          our compiled binaries (Go daemon, Go tools,
#                     firmware artifacts copied from CMake build dir).
#                     All build scripts target this directory; nothing
#                     in here is committed.
#
#   $THIRD_PARTY_DIR  downloaded third-party tools and data (Piper TTS
#                     binary, ONNX voice models, anything else fetched
#                     from upstream rather than built from source).
#
# The .gitignore excludes both. Operators see one place for "ours" and
# one place for "theirs"; before this convention the binaries scattered
# across pi/daemon/bin/, tools/*/, firmware/crsf/build/, and bin/piper.
BIN_DIR="$REPO_ROOT/bin"
THIRD_PARTY_DIR="$REPO_ROOT/third_party"
export BIN_DIR THIRD_PARTY_DIR

# Pretty step output.
say() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!! %s\033[0m\n' "$*" >&2; }
die() { printf '\033[1;31m!! %s\033[0m\n' "$*" >&2; exit 1; }

# Resolve PICO_SDK_PATH: env var wins, then ~/pico-sdk fallback, else error.
resolve_pico_sdk() {
  if [[ -n "${PICO_SDK_PATH:-}" ]]; then
    [[ -f "$PICO_SDK_PATH/pico_sdk_init.cmake" ]] \
      || die "PICO_SDK_PATH=$PICO_SDK_PATH does not contain pico_sdk_init.cmake"
    echo "$PICO_SDK_PATH"
    return
  fi
  local fallback="$HOME/pico-sdk"
  if [[ -f "$fallback/pico_sdk_init.cmake" ]]; then
    echo "$fallback"
    return
  fi
  die "Pico SDK not found. Set PICO_SDK_PATH or install at \$HOME/pico-sdk"
}
