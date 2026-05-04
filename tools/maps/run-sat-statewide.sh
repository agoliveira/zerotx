#!/usr/bin/env bash
# Phased São Paulo state satellite tile build, designed to run on a
# remote always-on host (e.g. "stan") via systemd-user. Each phase is a
# separate sat-download invocation against the same MBTiles file:
#
#   Phase 1: zoom 5-14, full state. Quick orientation coverage.
#   Phase 2: zoom 15.    Neighborhood detail.
#   Phase 3: zoom 16.    Building detail.
#
# All phases are resumable: re-running this script after an interrupt
# resumes from where it stopped. The MBTiles is the durable artifact;
# pmtiles conversion happens once at the end, only after all requested
# phases are complete.
#
# Usage:
#   tools/maps/run-sat-statewide.sh [PHASE]
#
#   PHASE: 1 | 2 | 3 | all (default: all)
#
# Configuration is via environment variables (with sensible defaults):
#   MAPTILES_DIR  default: ~/zerotx/maptiles
#   BBOX          default: SP state W,S,E,N
#   WORKERS       default: 4
#   RATE          default: 12 req/s
#   MBTILES_NAME  default: sp-state-sat
#
# Logs to $MAPTILES_DIR/$MBTILES_NAME.log. Tail it: tail -f <path>
#
# Exit codes:
#   0   - all requested phases completed (or already complete)
#   2   - configuration error
#   130 - interrupted (Ctrl-C / SIGTERM); resumable
#   3   - pmtiles convert failed
#   4   - sat-download itself failed (non-recoverable)

set -euo pipefail

PHASE="${1:-all}"

MAPTILES_DIR="${MAPTILES_DIR:-$HOME/zerotx/maptiles}"
BBOX="${BBOX:--53.20,-25.40,-44.10,-19.70}"
WORKERS="${WORKERS:-4}"
RATE="${RATE:-12}"
MBTILES_NAME="${MBTILES_NAME:-sp-state-sat}"

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
SAT_DOWNLOAD_DIR="$REPO/tools/maps/sat-download"
SAT_DOWNLOAD_BIN="$SAT_DOWNLOAD_DIR/sat-download"

MBTILES_PATH="$MAPTILES_DIR/${MBTILES_NAME}.mbtiles"
PMTILES_PATH="$MAPTILES_DIR/${MBTILES_NAME}.pmtiles"
LOG_PATH="$MAPTILES_DIR/${MBTILES_NAME}.log"

mkdir -p "$MAPTILES_DIR"

# Build the binary if missing or older than the source.
need_build=0
if [[ ! -x "$SAT_DOWNLOAD_BIN" ]]; then
    need_build=1
elif [[ "$SAT_DOWNLOAD_DIR/main.go" -nt "$SAT_DOWNLOAD_BIN" ]]; then
    need_build=1
fi
if [[ "$need_build" -eq 1 ]]; then
    echo "==> building sat-download" | tee -a "$LOG_PATH"
    (cd "$SAT_DOWNLOAD_DIR" && go mod tidy && go build -o sat-download .)
fi

# Verify pmtiles binary exists for end-of-run conversion.
if ! command -v pmtiles >/dev/null 2>&1; then
    echo "ERROR: 'pmtiles' binary not in PATH" >&2
    echo "Install with: go install github.com/protomaps/go-pmtiles/cmd/pmtiles@latest" >&2
    echo "Then ensure \$HOME/go/bin is on PATH." >&2
    exit 2
fi

run_phase() {
    local phase_num="$1"
    local zoom_range="$2"
    local with_pmtiles="$3"

    echo "" | tee -a "$LOG_PATH"
    echo "================================================================" | tee -a "$LOG_PATH"
    echo "Phase $phase_num: zoom $zoom_range  (started $(date -Iseconds))" | tee -a "$LOG_PATH"
    echo "================================================================" | tee -a "$LOG_PATH"

    local pmtiles_arg=()
    if [[ "$with_pmtiles" == "yes" ]]; then
        pmtiles_arg=(-pmtiles-out "$PMTILES_PATH")
    fi

    "$SAT_DOWNLOAD_BIN" \
        -bbox "$BBOX" \
        -zoom "$zoom_range" \
        -out "$MBTILES_PATH" \
        -workers "$WORKERS" \
        -rate "$RATE" \
        "${pmtiles_arg[@]}" \
        2>&1 | tee -a "$LOG_PATH"
}

case "$PHASE" in
    1)
        run_phase 1 "5-14" no
        echo "Phase 1 complete. To continue: $0 2" | tee -a "$LOG_PATH"
        ;;
    2)
        run_phase 2 "15-15" no
        echo "Phase 2 complete. To continue: $0 3" | tee -a "$LOG_PATH"
        ;;
    3)
        run_phase 3 "16-16" yes
        echo "Phase 3 complete. pmtiles archive: $PMTILES_PATH" | tee -a "$LOG_PATH"
        ;;
    all)
        run_phase 1 "5-14" no
        run_phase 2 "15-15" no
        run_phase 3 "16-16" yes
        echo "" | tee -a "$LOG_PATH"
        echo "All phases complete." | tee -a "$LOG_PATH"
        echo "MBTiles: $MBTILES_PATH" | tee -a "$LOG_PATH"
        echo "PMTiles: $PMTILES_PATH" | tee -a "$LOG_PATH"
        ;;
    *)
        echo "Unknown phase: $PHASE (use 1, 2, 3, or all)" >&2
        exit 2
        ;;
esac
