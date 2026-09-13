// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"os"

	"github.com/confighub/sdk/core/cubapi"
	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"
	"github.com/spf13/cobra"

	"github.com/confighub/installer/internal/upload"
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
			prepared, err := prepareUpload(ctx, flags)
			if err != nil {
				return err
			}
			var results []*goclientnew.UploadResult
			for i, req := range prepared.requests {
				pkg := prepared.packages[i]
				result, err := cubapi.Upload(ctx, prepared.client, req, true)
				if err != nil {
					return fmt.Errorf("plan %s: %w", pkg.Name, err)
				}
				upload.Report(os.Stdout, result, true)
				if err := upload.ReportImages(os.Stdout, pkg); err != nil {
					return err
				}
				results = append(results, result)
			}
			fmt.Printf("\n%s\n", upload.Summarize(results).Plan())
			return nil
		},
	}
	flags.register(cmd)
	return cmd
}
