# Top-level convenience Makefile for ZeroTX.
#
# Wraps scripts/ for the common operations. Use this when working on the
# repo as a whole; cd into pi/daemon/ for finer-grained Go work.

SHELL := /usr/bin/env bash

.PHONY: all daemon firmware tools manuals run run-idle flash test test-daemon clean distclean help

all: daemon tools firmware

daemon:
	@scripts/build-daemon.sh

tools:
	@scripts/build-tools.sh

firmware:
	@scripts/build-firmware.sh

manuals:
	@scripts/build-manuals.sh

run: daemon
	@scripts/run-daemon.sh $(ARGS)

run-idle: daemon
	@scripts/run-daemon.sh --idle $(ARGS)

flash: firmware
	@scripts/flash-firmware.sh

test: test-daemon

test-daemon:
	@cd pi/daemon && go test -count=1 -race ./...

# Remove build artifacts. Safe to run anytime; next build regenerates.
# All compiled outputs live in /bin/ now (daemon, tools, firmware .uf2/
# .elf); CMake's intermediate tree stays under firmware/crsf/build/.
# PlatformIO builds (firmware/io, firmware/tracker, firmware/display)
# still produce their own .pio dirs in-tree.
clean:
	@echo "==> Cleaning build artifacts"
	@rm -rf bin
	@rm -rf firmware/crsf/build
	@rm -rf firmware/io/.pio firmware/display/.pio firmware/tracker/.pio
	@find . -type d -name __pycache__ -prune -exec rm -rf {} +
	@find . -type f -name '*.pyc' -delete
	@cd pi/daemon && go clean ./... 2>/dev/null || true

# Remove locally-downloaded and locally-generated assets in addition to
# build artifacts. DESTRUCTIVE: re-running scripts/fetch-voices.sh,
# tools/build-geo.sh, and any tile/map build pipeline is required after
# this. Daemon runtime state (cache/) and operator-private notes
# (HANDOVER.md, JOURNAL.md) are NOT touched -- those live outside the
# clean/distclean scope.
distclean: clean
	@echo "==> Removing downloaded assets (third_party, tiles, geo DBs)"
	@rm -rf third_party maptiles
	@rm -f  geo/*.db

help:
	@echo "Targets:"
	@echo "  make            build daemon, tools, and firmware"
	@echo "  make daemon     build the Go daemon"
	@echo "  make tools      build the auxiliary Go tools"
	@echo "  make firmware   build the RP2040 firmware"
	@echo "  make manuals    build PDF versions of the manuals (pandoc + xelatex)"
	@echo "  make run        run daemon with Big Talon defaults"
	@echo "  make run-idle   run daemon in IDLE (no model, no joystick)"
	@echo "  make flash      copy .uf2 to RPI-RP2 (requires BOOTSEL)"
	@echo "  make test       run all daemon tests"
	@echo "  make clean      remove build artifacts (safe, regenerable)"
	@echo "  make distclean  also remove downloaded assets (voices, tiles, geo DBs)"
	@echo
	@echo "Pass extra daemon flags via ARGS:"
	@echo "  make run ARGS='-v'"
	@echo
	@echo "Run-daemon env overrides: ZTX_API ZTX_MODEL ZTX_MODEL_IMAGE ZTX_JOYSTICK"
