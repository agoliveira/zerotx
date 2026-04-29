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
ELF=$(find . -maxdepth 2 -name '*.elf' | head -1)

if [[ -z "$UF2" && -n "$ELF" ]]; then
  # Pico SDK quirk: pico_add_extra_outputs is a POST_BUILD hook on the
  # executable target. If the .elf is up-to-date but the .uf2 was moved
  # or deleted (e.g. you copied it onto an RP2040 last time), make won't
  # regenerate it. Use picotool from the build's _deps directory to
  # synthesize the .uf2 from the .elf without rebuilding the world.
  say "uf2 missing despite up-to-date elf; regenerating via picotool"
  PICOTOOL=$(find _deps -name 'picotool' -type f -executable 2>/dev/null | head -1)
  if [[ -z "$PICOTOOL" ]]; then
    PICOTOOL=$(command -v picotool || true)
  fi
  if [[ -z "$PICOTOOL" ]]; then
    warn "picotool not found, cannot regenerate .uf2"
  else
    out_dir=$(dirname "$ELF")
    out_uf2="${out_dir}/$(basename "$ELF" .elf).uf2"
    "$PICOTOOL" uf2 convert "$ELF" "$out_uf2" -t elf
    UF2="$out_uf2"
  fi
fi

if [[ -n "$UF2" ]]; then
  say "Built: $REPO_ROOT/rp2040/build/$(basename "$UF2")"
else
  warn "Build finished but no .uf2 found"
fi
