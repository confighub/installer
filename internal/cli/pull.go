// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	ipkg "github.com/confighub/installer/internal/pkg"
)

func newPullCmd() *cobra.Command {
	var workDir string
	cmd := &cobra.Command{
		Use:   "pull <package>",
		Short: "Fetch a package into <work-dir>/package/",
		Long: `Pull a package into <work-dir>/package/. Subsequent commands (wizard,
render, setup) read from the same work-dir.

` + packageRefHelp + `

The fetch is staged into a sibling temp directory and atomically
renamed into <work-dir>/package/ on success — a failed pull leaves any
prior <work-dir>/package/ intact.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			reg, err := registry(true)
			if err != nil {
				return err
			}
			dir, err := reg.PullToWorkDir(ctx, args[0], workDir)
			if err != nil {
				return err
			}
			loaded, err := ipkg.Load(dir)
			if err != nil {
				return err
			}
			fmt.Printf("Pulled %s@%s to %s\n",
				loaded.Package.Metadata.Name,
				loaded.Package.InstallerMetadata.Version,
				loaded.Root)
			return nil
		},
	}
	cmd.Flags().StringVar(&workDir, "work-dir", ".", "working directory; package is written to <work-dir>/package/")
	return cmd
}
