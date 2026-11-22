// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Package testregistry runs an in-memory OCI registry for tests that push and
// pull packages, so they need no Docker. It listens on 127.0.0.1, which the
// installer always reaches over plain HTTP.
package testregistry

import (
	"io"
	"log"
	"net/http"
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

// Option configures a Registry.
type Option func(http.Handler) http.Handler

// BasicAuth makes the registry refuse requests without these credentials.
func BasicAuth(username, password string) Option {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, p, ok := r.BasicAuth(); !ok || u != username || p != password {
				w.Header().Set("WWW-Authenticate", `Basic realm="testregistry"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// New starts a registry. Close it when done.
func New(opts ...Option) *Registry {
	var h http.Handler = registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	for _, opt := range opts {
		h = opt(h)
	}
	srv := httptest.NewServer(h)
	return &Registry{Server: srv, Host: strings.TrimPrefix(srv.URL, "http://")}
}
