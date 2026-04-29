#!/usr/bin/env bash
# Build the ZeroTX Go daemon into pi/daemon/bin/zerotxd.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

cd "$REPO_ROOT/pi/daemon"

# First-run safety: generate go.sum if missing.
if [[ ! -f go.sum ]]; then
  say "go.sum not found, running go mod tidy"
  go mod tidy
fi

say "Building daemon"
mkdir -p bin
go build -o bin/zerotxd ./cmd/zerotxd

say "Built: $REPO_ROOT/pi/daemon/bin/zerotxd"
"$REPO_ROOT/pi/daemon/bin/zerotxd" -h 2>&1 | head -1 || true
