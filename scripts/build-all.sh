#!/usr/bin/env bash
# Build daemon, tools, and firmware. Useful before commits and CI.
# All outputs land in $BIN_DIR (default $REPO_ROOT/bin).
set -euo pipefail
SCRIPTS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

"$SCRIPTS/build-daemon.sh"
echo
"$SCRIPTS/build-tools.sh"
echo
"$SCRIPTS/build-firmware.sh"
