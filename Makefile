# Development builds. Everything here is stamped channel=dev, so these
# binaries use ~/.config/opsie-dev and ~/.local/state/opsie-dev and never
# update themselves: they can't touch the opsie you installed from a release.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.channel=dev
W ?= 45
H ?= 32

.PHONY: run build test check preview daemon clean

run: build
	./bin/opsie

build:
	@mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/opsie ./cmd/opsie

test:
	go test ./...

# check runs everything CI runs.
check:
	go vet ./...
	go test ./...  # CI also runs -race
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

# preview prints one frame at W×H: make preview W=45 H=32 [SCREEN=help|detail|docker|container:NAME|project:NAME|prune|logs:NAME]
preview: build
	./bin/opsie preview $(W)x$(H) $(SCREEN)

# daemon runs the background checks in the foreground with your dev
# config, keeping status in /run/opsie-dev where the dev dashboard looks.
# Ctrl+C stops it. It's never installed as a service.
daemon: build
	sudo ./bin/opsie daemon --config $(HOME)/.config/opsie-dev/config.toml --user $(USER) --state /run/opsie-dev

clean:
	rm -rf bin dist
