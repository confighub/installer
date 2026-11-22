// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/confighub/installer/pkg/installer"
)

func newRenderCmd() *cobra.Command {
	var workDir string
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Render selection + inputs into per-resource Kubernetes YAML",
		Long: `Render reads <work-dir>/package + <work-dir>/out/record/{selection,inputs}.yaml,
runs kustomize on the chosen base + components, then runs the package's
function chain template (resolved against the inputs) via the ConfigHub
function executor SDK. Output goes to <work-dir>/out/manifests/ as one YAML
file per resource. The resolved function-chain.yaml and a manifest-index.yaml
are written to <work-dir>/out/record/.

Kustomize is built into the installer; the kustomize binary is not needed.

Render is deterministic: the same package + selection + inputs always produce
identical bytes. Re-render after editing selection.yaml or inputs.yaml.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			absWork, err := filepath.Abs(workDir)
			if err != nil {
				return err
			}
			inst, err := newInstaller()
			if err != nil {
				return err
			}
			if _, err := inst.Render(commandContext(cmd), absWork); err != nil {
				if errors.Is(err, installer.ErrNoLock) || errors.Is(err, installer.ErrStaleLock) {
					return fmt.Errorf("%v; run `%s deps update --work-dir %s`", err, InvocationName(), absWork)
				}
				return err
			}
			fmt.Printf("Next: %s upload --work-dir %s --space <slug>\n", InvocationName(), absWork)
			return nil
		},
	}
	cmd.Flags().StringVar(&workDir, "work-dir", ".", "working directory (reads ./package + ./out/record, writes ./out/manifests)")
	return cmd
}
