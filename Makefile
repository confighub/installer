# Makefile for the installer CLI.
#
# The output binary is `bin/installer` because the cub plugin protocol
# names the plugin after its entrypoint basename: invoking it via cub
# reads as `cub installer ...`, matching the standalone form.

GO     ?= go
BIN    ?= bin/installer
PKG    ?= ./cmd/installer
# kustomize_disable_go_plugin_support drops kustomize's import of Go's plugin
# package, which only loads .so Go plugins. The installer never does: its
# transform step is an exec KRM function. With cgo on, the import keeps every
# exported method in the binary (7 MB); with cgo off, it costs nothing, but the
# builds stay alike.
TAGS   ?= kustomize_disable_go_plugin_support

.PHONY: all build release-build test vet fmt clean

all: build

build: $(BIN)

$(BIN): $(shell find . -name '*.go' -not -path './bin/*')
	$(GO) build -tags '$(TAGS)' -o $(BIN) $(PKG)

# Release-style build: cross-compile with version injection, used by the
# release workflow. Override GOOS, GOARCH, VERSION; OUT defaults to a per-arch
# binary name (installer-<goos>-<goarch>).
#
# Example: make release-build VERSION=0.4.0 GOOS=darwin GOARCH=arm64
release-build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) \
	  $(GO) build -trimpath -tags '$(TAGS)' \
	    -ldflags "-s -w -X github.com/confighub/installer/internal/version.Version=$(VERSION)" \
	    -o $(or $(OUT),installer-$(GOOS)-$(GOARCH)) $(PKG)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -f bin/install bin/installer
