# Makefile for the installer CLI.
#
# The output binary is `bin/installer` because the cub plugin protocol
# names the plugin after its entrypoint basename: invoking it via cub
# reads as `cub installer ...`, matching the standalone form.

GO     ?= go
BIN    ?= bin/installer
PKG    ?= ./cmd/installer

.PHONY: all build test vet fmt clean

all: build

build: $(BIN)

$(BIN): $(shell find . -name '*.go' -not -path './bin/*')
	$(GO) build -o $(BIN) $(PKG)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -f bin/install bin/installer
