# Top-level convenience Makefile for ZeroTX.
#
# Wraps scripts/ for the common operations. Use this when working on the
# repo as a whole; cd into pi/daemon/ for finer-grained Go work.

SHELL := /usr/bin/env bash

.PHONY: all daemon firmware run run-idle flash test test-daemon clean help

all: daemon firmware

daemon:
	@scripts/build-daemon.sh

firmware:
	@scripts/build-firmware.sh

run: daemon
	@scripts/run-daemon.sh $(ARGS)

run-idle: daemon
	@scripts/run-daemon.sh --idle $(ARGS)

flash: firmware
	@scripts/flash-firmware.sh

test: test-daemon

test-daemon:
	@cd pi/daemon && go test -count=1 -race ./...

clean:
	@echo "==> Cleaning build artifacts"
	@rm -rf pi/daemon/bin
	@rm -rf rp2040/build
	@cd pi/daemon && go clean ./... 2>/dev/null || true

help:
	@echo "Targets:"
	@echo "  make            build daemon and firmware"
	@echo "  make daemon     build the Go daemon"
	@echo "  make firmware   build the RP2040 firmware"
	@echo "  make run        run daemon with Big Talon defaults"
	@echo "  make run-idle   run daemon in IDLE (no model, no joystick)"
	@echo "  make flash      copy .uf2 to RPI-RP2 (requires BOOTSEL)"
	@echo "  make test       run all daemon tests"
	@echo "  make clean      remove build artifacts"
	@echo
	@echo "Pass extra daemon flags via ARGS:"
	@echo "  make run ARGS='-v'"
	@echo
	@echo "Run-daemon env overrides: ZTX_API ZTX_MODEL ZTX_MODEL_IMAGE ZTX_JOYSTICK"
