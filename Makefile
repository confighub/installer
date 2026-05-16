# Makefile for the installer CLI.
#
# The output binary is `bin/install` because the cub plugin protocol
# names the plugin after its entrypoint basename: invoking it via cub
# reads as `cub install ...`, matching the standalone form.

GO     ?= go
BIN    ?= bin/install
PKG    ?= ./cmd/installer

.PHONY: all build install test vet fmt clean

all: build

build: $(BIN)

$(BIN): $(shell find . -name '*.go' -not -path './bin/*')
	$(GO) build -o $(BIN) $(PKG)

# Alias: `make install` builds the binary (it does NOT install it on
# PATH — name matches the binary, not the verb).
install: build

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -f bin/install bin/installer
