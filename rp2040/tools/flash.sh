#!/usr/bin/env bash
# Flash zerotx-fw.uf2 to an RP2040 that is currently in BOOTSEL mode.
#
# Hold the BOOT button while plugging USB to enter BOOTSEL. The device
# enumerates as a mass storage volume labeled RPI-RP2.
#
# Usage:
#   tools/flash.sh                         # auto-find RPI-RP2 mount
#   tools/flash.sh /path/to/firmware.uf2   # custom firmware

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UF2="${1:-$REPO_ROOT/build/zerotx-fw.uf2}"

if [[ ! -f "$UF2" ]]; then
  echo "firmware not found: $UF2" >&2
  echo "build first:" >&2
  echo "  mkdir -p $REPO_ROOT/build && cd $_ && cmake .. && make -j" >&2
  exit 2
fi

# Try to find a mounted RPI-RP2 volume.
MOUNT="$(lsblk -o LABEL,MOUNTPOINT -nr 2>/dev/null | awk '$1=="RPI-RP2" {print $2; exit}')"
if [[ -z "${MOUNT:-}" ]]; then
  # Fallback: common mount roots
  for guess in "/media/$USER/RPI-RP2" "/run/media/$USER/RPI-RP2" "/mnt/RPI-RP2"; do
    if [[ -d "$guess" ]]; then MOUNT="$guess"; break; fi
  done
fi

if [[ -z "${MOUNT:-}" ]]; then
  echo "RPI-RP2 volume not found." >&2
  echo "Hold BOOT, plug the USB cable, wait for the volume to appear, then re-run." >&2
  exit 3
fi

echo "flashing: $UF2"
echo "      -> $MOUNT"
cp "$UF2" "$MOUNT/"
sync
echo "done. RP2040 will reboot into the new firmware."
