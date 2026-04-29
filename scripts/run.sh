#!/usr/bin/env bash
# Run the ZeroTX GUI from its build directory so asset paths resolve.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT/pi/gui"

if [[ ! -x "./zerotx" ]]; then
  echo "Binary not built. Run scripts/build.sh first." >&2
  exit 1
fi

exec ./zerotx "$@"
