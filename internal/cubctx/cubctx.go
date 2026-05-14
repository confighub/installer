// Package cubctx reads the active cub CLI context (organization ID,
// server URL) and offers a sanity-check helper that compares the live
// context against values recorded in a work-dir's upload.yaml.
//
// Used by every installer command that touches ConfigHub (wizard, plan,
// update, upgrade) so an operator who switches accounts between
// sessions cannot silently materialize Units in the wrong organization
// or against the wrong server.
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

// CheckMatches compares the active cub context against values recorded
// in a work-dir's upload.yaml. Empty `wantOrg` or `wantServer` skips
// that check (e.g., a freshly created upload.yaml from an older
// installer that did not record one of the fields).
//
// The returned error names both the recorded and current values plus
// the remediation command, so the operator can fix it without
// re-reading documentation.
func CheckMatches(ctx context.Context, wantOrg, wantServer string) error {
	if wantOrg == "" && wantServer == "" {
		return nil
	}
	got, err := Get(ctx)
	if err != nil {
		return err
	}
	if wantOrg != "" && got.OrganizationID != wantOrg {
		return fmt.Errorf(
			"cub context organization mismatch: upload.yaml recorded %s, current cub context is %s — run `cub context set <name>` or `cub auth login` against the recorded organization",
			wantOrg, got.OrganizationID,
		)
	}
	if wantServer != "" && got.ServerURL != wantServer {
		return fmt.Errorf(
			"cub context server mismatch: upload.yaml recorded %s, current cub context is %s — run `cub context set <name>` or `cub auth login --server %s`",
			wantServer, got.ServerURL, wantServer,
		)
	}
	return nil
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
