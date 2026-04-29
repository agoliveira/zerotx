# ZeroTX daemon Makefile.
#
# Works the same on Ubuntu desktop and Pi.
#
# One-time host setup:
#   sudo apt install golang-go libsdl2-dev
#   sudo usermod -aG dialout $USER     # then log out/in for /dev/ttyACM access

GO              ?= go
GOFLAGS         ?=
BUILD_DIR       ?= bin
DAEMON          := zerotxd
INSPECT         := zerotx-inspect
PKGS_NO_CGO     := ./internal/ipc/... ./internal/model/... ./internal/mapper/...
PKGS_ALL        := ./...

# Optional flags for `make run`.
PORT            ?=
MODEL           ?=
JOYSTICK_NAME   ?=

.PHONY: all build test test-fast vet fmt clean run list-joysticks show-axes tidy help

all: build

help:
	@echo "Targets:"
	@echo "  build           build daemon + inspect + axes-debug into ./bin"
	@echo "  test            run all tests (cgo SDL2 bindings: slow first time)"
	@echo "  test-fast       run only non-cgo tests (ipc, model, mapper)"
	@echo "  vet             go vet all packages"
	@echo "  fmt             gofmt all sources"
	@echo "  run             build then run zerotxd with default flags"
	@echo "  show-axes       build then run zerotx-axes (live axis/button display)"
	@echo "  list-joysticks  build then list attached joysticks"
	@echo "  tidy            go mod tidy"
	@echo "  clean           remove build outputs"

build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(DAEMON) ./cmd/zerotxd
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(INSPECT) ./cmd/zerotx-inspect
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/zerotx-axes ./cmd/zerotx-axes

test:
	$(GO) test $(PKGS_ALL)

test-fast:
	$(GO) test $(PKGS_NO_CGO)

vet:
	$(GO) vet $(PKGS_ALL)

fmt:
	gofmt -w .

tidy:
	$(GO) mod tidy

run: build
	./$(BUILD_DIR)/$(DAEMON) \
		$(if $(PORT),-port $(PORT)) \
		$(if $(MODEL),-model $(MODEL)) \
		$(if $(JOYSTICK_NAME),-joystick-name "$(JOYSTICK_NAME)") \
		-v

list-joysticks: build
	./$(BUILD_DIR)/$(DAEMON) -list-joysticks

show-axes: build
	./$(BUILD_DIR)/zerotx-axes $(if $(JOYSTICK_NAME),-name "$(JOYSTICK_NAME)")

clean:
	rm -rf $(BUILD_DIR)
