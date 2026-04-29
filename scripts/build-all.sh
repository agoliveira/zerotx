#!/usr/bin/env bash
# Build daemon and firmware. Useful before commits and CI.
set -euo pipefail
SCRIPTS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

"$SCRIPTS/build-daemon.sh"
echo
"$SCRIPTS/build-firmware.sh"
