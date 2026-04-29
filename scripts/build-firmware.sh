#!/usr/bin/env bash
# Build the RP2040 firmware. Output: rp2040/build/zerotx-fw.uf2
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

PICO_SDK_PATH="$(resolve_pico_sdk)"
say "Using Pico SDK at: $PICO_SDK_PATH"

cd "$REPO_ROOT/rp2040"
mkdir -p build
cd build

# Reconfigure cmake if either CMakeCache.txt is missing or PICO_SDK_PATH
# in the cache differs from what we resolved (handles the case where the
# user changed env vars between builds).
need_configure=1
if [[ -f CMakeCache.txt ]]; then
  cached=$(awk -F= '/^PICO_SDK_PATH:/ {print $2}' CMakeCache.txt || true)
  if [[ "$cached" == "$PICO_SDK_PATH" ]]; then
    need_configure=0
  fi
fi

if [[ $need_configure -eq 1 ]]; then
  say "Configuring (cmake)"
  cmake -DPICO_SDK_PATH="$PICO_SDK_PATH" ..
fi

say "Compiling"
make -j"$(nproc)"

UF2=$(find . -maxdepth 2 -name '*.uf2' | head -1)
if [[ -n "$UF2" ]]; then
  say "Built: $REPO_ROOT/rp2040/build/$(basename "$UF2")"
else
  warn "Build finished but no .uf2 found"
fi
