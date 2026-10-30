// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Package cubctx reads the active cub CLI context (organization ID,
// server URL), for commands that record where an install lives.
package cubctx

import (
	"context"
	"fmt"

	"github.com/confighub/sdk/core/cubapi"
)

// Context bundles the fields we read from the active cub context.
type Context struct {
	OrganizationID string
	ServerURL      string
}

// Get reads the active cub context from the cub configuration directory,
// the one CUB_CONFIG names or the default, choosing the context CUB_CONTEXT
// names or else the current one, as cub does.
func Get(ctx context.Context) (*Context, error) {
	env, err := cubapi.LoadEnvironment(ctx)
	if err != nil {
		return nil, err
	}
	store, err := cubapi.LoadConfig(env.Config)
	if err != nil {
		return nil, fmt.Errorf("read cub configuration: %w", err)
	}
	if env.Context != "" {
		if err := store.Use(env.Context); err != nil {
			return nil, err
		}
	}
	active, err := store.ActiveContext()
	if err != nil {
		return nil, err
	}
	return &Context{
		OrganizationID: active.Coordinate.OrganizationID,
		ServerURL:      active.Coordinate.ServerURL,
	}, nil
}
