// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"os"
	"strings"

	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"
	"github.com/spf13/cobra"

	"github.com/confighub/installer/internal/upload"
	"github.com/confighub/installer/pkg/installer"
)

func newPlanCmd() *cobra.Command {
	var flags uploadFlags
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show what installer upload would change in ConfigHub",
		Long: `Plan runs the requests installer upload would send, as dry runs, and prints
what each would do per Space: the Units it would create, update, empty, or
revive, the paths each update changes and any it withholds as conflicts, and
the Links it would create or delete. ConfigHub computes the plan with the same
code an upload runs, so an upload with the same flags does what plan shows.

Plan takes the same flags as upload, since they decide the Spaces. After each
Space it prints the images the rendered manifests run, whether or not anything
changes.

Plan writes nothing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := commandContext(cmd)
			flags.noteChanged(cmd)
			inst, err := newInstaller(ctx, false)
			if err != nil {
				return err
			}
			plan, err := prepareUpload(ctx, inst, flags)
			if err != nil {
				return err
			}
			// A failed dry run still shows the packages previewed before it.
			previewErr := inst.PreviewUpload(ctx, plan, installer.PreviewOptions{Mutations: true})
			var results []*goclientnew.UploadResult
			for _, pkg := range plan.Packages {
				if pkg.Preview == nil {
					break
				}
				upload.Report(os.Stdout, pkg.Preview, true)
				if len(pkg.PreviewOmitted) > 0 {
					fmt.Printf("  DependsOn %s is left out of this dry run: upload creates those Components first\n", strings.Join(pkg.PreviewOmitted, ", "))
				}
				if err := upload.ReportImages(os.Stdout, upload.Package{ManifestsDir: pkg.ManifestsDir, SpaceSlug: pkg.Space}); err != nil {
					return err
				}
				results = append(results, pkg.Preview)
			}
			if previewErr != nil {
				return previewErr
			}
			fmt.Printf("\n%s\n", upload.Summarize(results).Plan())
			return nil
		},
	}
	flags.register(cmd)
	return cmd
}
