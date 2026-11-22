// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package pkg

import (
	"context"
	"fmt"

	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/confighub/installer/internal/sign"
)

// Registry is how the installer reaches OCI registries: the credentials it
// presents, whether it uses plain HTTP, and the signature check every package
// pulled from one has to pass. The zero value uses the Docker credential
// store, HTTPS except for localhost, and checks no signatures.
type Registry struct {
	// Credentials authenticate to registries; nil uses the docker-config
	// store (~/.docker/config.json plus native helpers like osxkeychain).
	Credentials credentials.Store
	// PlainHTTP uses HTTP for every registry. localhost and 127.0.0.1 always
	// use HTTP.
	PlainHTTP bool
	// Verifier checks each package pulled from a registry, including
	// dependencies; nil checks none.
	Verifier *sign.Verifier
}

// authClient builds an oras auth.Client that presents reg's credentials.
// Anonymous access works when no credentials are configured — the store
// returns auth.EmptyCredential.
func (reg Registry) authClient() (*auth.Client, error) {
	store := reg.Credentials
	if store == nil {
		var err error
		if store, err = credentialStore(); err != nil {
			return nil, err
		}
	}
	return &auth.Client{
		Client:     retry.DefaultClient,
		Cache:      auth.NewCache(),
		Credential: credentials.Credential(store),
	}, nil
}

// credentialStore returns the docker-config-backed store authClient uses
// when a Registry names none, and login and logout write to.
func credentialStore() (credentials.Store, error) {
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{
		AllowPlaintextPut: true,
	})
	if err != nil {
		return nil, fmt.Errorf("credential store: %w", err)
	}
	return store, nil
}

// Login stores a credential for registry. Credentials are written to the
// docker-config store, so they are shared with docker, podman, and oras.
func Login(ctx context.Context, registry, username, password string) error {
	store, err := credentialStore()
	if err != nil {
		return err
	}
	return store.Put(ctx, registry, auth.Credential{Username: username, Password: password})
}

// Logout removes credentials for registry from the docker-config store.
func Logout(ctx context.Context, registry string) error {
	store, err := credentialStore()
	if err != nil {
		return err
	}
	return store.Delete(ctx, registry)
}
