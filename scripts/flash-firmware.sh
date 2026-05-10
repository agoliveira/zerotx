#!/usr/bin/env bash
# Flash the RP2040 firmware. Requires the RP2040 to be in BOOTSEL mode
# (mounted as RPI-RP2 USB drive).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

UF2=$(find "$REPO_ROOT/firmware/crsf/build" -maxdepth 2 -name '*.uf2' 2>/dev/null | head -1)
[[ -n "$UF2" ]] || die "No .uf2 in firmware/crsf/build/. Run scripts/build-firmware.sh first."

# Try common mount points. /media/$USER/RPI-RP2 covers most desktop distros.
candidates=(
  "/media/$USER/RPI-RP2"
  "/media/$USER/RPI-RP2 "       # trailing-space variant some mounts use
  "/run/media/$USER/RPI-RP2"
  "/mnt/RPI-RP2"
)

mount_point=""
for c in "${candidates[@]}"; do
  if [[ -d "$c" ]]; then
    mount_point="$c"
    break
  fi
done

if [[ -z "$mount_point" ]]; then
  die "RPI-RP2 not mounted. Hold BOOTSEL while plugging USB, then re-run."
fi

say "Flashing $(basename "$UF2") -> $mount_point"
cp "$UF2" "$mount_point/"
sync

say "Flash complete. The board will reboot automatically."
