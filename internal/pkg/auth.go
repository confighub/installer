package pkg

import (
	"context"
	"fmt"

	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// authClient builds an oras auth.Client that reads credentials from the
// docker-config-style store (~/.docker/config.json plus native helpers like
// osxkeychain). Anonymous access works when no credentials are configured —
// the store returns auth.EmptyCredential.
func authClient() (*auth.Client, error) {
	store, err := credentialStore()
	if err != nil {
		return nil, err
	}
	return &auth.Client{
		Client:     retry.DefaultClient,
		Cache:      auth.NewCache(),
		Credential: credentials.Credential(store),
	}, nil
}

// credentialStore returns the same docker-config-backed store used by
// authClient. Exposed so login / logout can Put / Delete entries.
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
