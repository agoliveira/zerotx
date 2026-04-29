#!/usr/bin/env bash
# Build the ZeroTX GUI. Requires lazbuild from a Lazarus 3.x install.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT/pi/gui"

if ! command -v lazbuild >/dev/null 2>&1; then
  echo "lazbuild not found. Install Lazarus or add it to PATH." >&2
  exit 1
fi

lazbuild --build-mode=Default zerotx.lpi
echo "Built: $REPO_ROOT/pi/gui/zerotx"
