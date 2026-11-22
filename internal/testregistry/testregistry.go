// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Package testregistry runs an in-memory OCI registry for tests that push and
// pull packages, so they need no Docker. It listens on 127.0.0.1, which the
// installer always reaches over plain HTTP.
package testregistry

import (
	"io"
	"log"
	"net/http/httptest"
	"strings"

	"github.com/google/go-containerregistry/pkg/registry"
)

// Registry is a running in-memory registry.
type Registry struct {
	*httptest.Server
	// Host is the registry's host:port, for references such as
	// oci://<Host>/repo:tag.
	Host string
}

// New starts a registry. Close it when done.
func New() *Registry {
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	return &Registry{Server: srv, Host: strings.TrimPrefix(srv.URL, "http://")}
}
