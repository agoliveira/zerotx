#!/usr/bin/env bash
# Build the ZeroTX auxiliary Go tools into $BIN_DIR.
#
# Each tool lives under tools/<name>/ with its own go.mod (independent
# module) and exposes a single main package. The script iterates every
# such directory; adding a new tool means creating its directory and
# go.mod -- this script picks it up automatically on the next run.
#
# Not all tools/ entries are Go modules: some are shell scripts
# (build-geo.sh, fetch-hud-fonts.sh) or non-tool subdirs (maps/). The
# script discriminates by presence of go.mod.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

mkdir -p "$BIN_DIR"

shopt -s nullglob
built=0
for tool_dir in "$REPO_ROOT"/tools/*/; do
  tool_name=$(basename "$tool_dir")
  if [[ ! -f "$tool_dir/go.mod" ]]; then
    continue
  fi
  say "Building $tool_name"
  (
    cd "$tool_dir"
    if [[ ! -f go.sum ]]; then
      go mod tidy
    fi
    go build -o "$BIN_DIR/$tool_name" .
  )
  built=$((built + 1))
done

if [[ $built -eq 0 ]]; then
  warn "No Go tools found under tools/ (looked for */go.mod)"
else
  say "Built $built tool(s) into $BIN_DIR/"
fi
