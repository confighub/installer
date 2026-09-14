// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Package cubctx reads the active cub CLI context (organization ID,
// server URL), for commands that record where an install lives.
package cubctx

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Context bundles the fields we read from `cub context get`.
type Context struct {
	OrganizationID string
	ServerURL      string
}

// Get reads the active cub context. Shells out to `cub context get -o
// jq=...` once per field — both calls inherit the user's cub session.
func Get(ctx context.Context) (*Context, error) {
	org, err := runJQ(ctx, ".coordinate.organizationID")
	if err != nil {
		return nil, fmt.Errorf("read cub context organizationID: %w", err)
	}
	server, err := runJQ(ctx, ".coordinate.serverURL")
	if err != nil {
		return nil, fmt.Errorf("read cub context serverURL: %w", err)
	}
	return &Context{
		OrganizationID: org,
		ServerURL:      server,
	}, nil
}

func runJQ(ctx context.Context, expr string) (string, error) {
	cmd := exec.CommandContext(ctx, "cub", "context", "get", "-o", "jq="+expr)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("cub context get -o jq=%s: %w\n%s", expr, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}
