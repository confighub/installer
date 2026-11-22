// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/confighub/installer/internal/transform"
)

func newTransformerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "transformer",
		Short: "Run ConfigHub functions as a kustomize exec transformer (reads ResourceList from stdin)",
		Long: `Transformer is a KRM Functions exec plugin for kustomize. It reads a
ResourceList from stdin, runs the ConfigHub function chain or validators
encoded in the ResourceList's functionConfig against the items, and writes
the resulting ResourceList to stdout.

Two functionConfig kinds are recognized:

  ConfigHubTransformers — mutating. Goes under kustomization.transformers:.
                          spec.groups is a list of FunctionGroups whose
                          output feeds the next.
  ConfigHubValidators   — non-mutating. Goes under kustomization.validators:.
                          spec.groups is a list of validator groups.
                          Failures are emitted as ResourceList.results with
                          severity=error; items are not modified. Kustomize
                          fails the build when any result has severity=error.

Each group's whereResource uses ConfigHub filter syntax (e.g.
"ConfigHub.ResourceType = 'apps/v1/Deployment'"), not CEL — the expression
is passed through verbatim to the function executor's WhereResource option.

Usable standalone with raw kustomize: reference this subcommand from your
kustomization's transformers: list via a KRM function config whose
config.kubernetes.io/function annotation points at a wrapper that runs
'installer transformer'. Render itself runs the same step by re-executing
the installer with INSTALLER_RUN_TRANSFORMER=1.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			in, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			out, err := transform.Run(ctx, in)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(out)
			return err
		},
	}
}
