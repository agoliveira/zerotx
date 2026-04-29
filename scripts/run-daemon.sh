#!/usr/bin/env bash
# Run the ZeroTX daemon with Big Talon defaults. Pass --idle to skip
# preloading model and joystick, leaving the daemon in IDLE for API-driven
# selection.
#
# Override defaults via env vars:
#   ZTX_API           default: 127.0.0.1:8080
#   ZTX_MODEL         default: configs/big_talon_zerotx.yml
#   ZTX_MODEL_IMAGE   default: ~/fpv/Edgetx/sd/IMAGES/talong.png (skipped if missing)
#   ZTX_JOYSTICK      default: Thrustmaster
#
# Anything passed on the command line after --idle (or directly) is
# forwarded to zerotxd, so you can layer e.g. -v on top.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

BIN="$REPO_ROOT/pi/daemon/bin/zerotxd"
[[ -x "$BIN" ]] || die "Daemon not built. Run scripts/build-daemon.sh first."

API="${ZTX_API:-127.0.0.1:8080}"

idle=0
if [[ "${1:-}" == "--idle" ]]; then
  idle=1
  shift
fi

cd "$REPO_ROOT/pi/daemon"

if [[ $idle -eq 1 ]]; then
  say "Starting daemon in IDLE (no model, no joystick)"
  exec "$BIN" -api "$API" "$@"
fi

MODEL="${ZTX_MODEL:-configs/big_talon_zerotx.yml}"
JOYSTICK="${ZTX_JOYSTICK:-Thrustmaster}"
MODEL_IMAGE="${ZTX_MODEL_IMAGE:-$HOME/fpv/Edgetx/sd/IMAGES/talong.png}"

[[ -f "$MODEL" ]] || die "Model not found: $MODEL"

img_arg=()
if [[ -f "$MODEL_IMAGE" ]]; then
  img_arg=(-model-image "$MODEL_IMAGE")
else
  warn "Model image not found at $MODEL_IMAGE, running without bitmap"
fi

say "Starting daemon (model=$MODEL, joystick=$JOYSTICK, api=$API)"
exec "$BIN" \
  -api "$API" \
  -model "$MODEL" \
  -joystick-name "$JOYSTICK" \
  -panel-stdin \
  "${img_arg[@]}" \
  "$@"
