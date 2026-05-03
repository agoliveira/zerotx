#!/usr/bin/env bash
# Launch zerotxd on the desktop for development.
#
# Builds the daemon if needed, then runs it with the standard set
# of flags for desktop testing: RP2040 connected by USB, Thrustmaster
# joystick, web GUI served from filesystem (so frontend edits are
# picked up without rebuild), online tile proxy enabled.
#
# Usage:
#   tools/run-daemon-desktop.sh                 # build + run
#   tools/run-daemon-desktop.sh --no-build      # skip build, run only

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
DAEMON_DIR="$REPO/pi/daemon"

NO_BUILD=0
if [[ "${1:-}" == "--no-build" ]]; then
    NO_BUILD=1
fi

if [[ "$NO_BUILD" -eq 0 ]]; then
    echo "==> building zerotxd"
    (cd "$DAEMON_DIR" && go build -o bin/zerotxd ./cmd/zerotxd)
fi

echo "==> launching zerotxd on http://127.0.0.1:8080"
cd "$DAEMON_DIR"
exec ./bin/zerotxd \
    -api 127.0.0.1:8080 \
    -model configs/big_talon_zerotx.yml \
    -joystick-name Thrustmaster \
    -web-dir web \
    -port /dev/serial/by-id/usb-Raspberry_Pi_Pico_E66138935F3C4824-if00
