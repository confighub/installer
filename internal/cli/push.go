// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/pkg/api"
)

func newPushCmd() *cobra.Command {
	var sourceRepo, sourceRevision, sourcePath string
	var noGit bool
	cmd := &cobra.Command{
		Use:   "push <pkg.tgz|dir> <oci-ref>",
		Short: "Push a packaged installer to an OCI registry",
		Long: `Push uploads a packaged installer (either a .tgz produced by installer
package, or a source directory which is packaged in-memory first) to an
OCI registry as a native installer artifact (artifactType
application/vnd.confighub.installer.package.v1+json).

The package is NOT rendered — only bundled. This differs from Kustomizer's
push, which renders before uploading.

ref must include a tag: oci://host/repo:tag. Digest-only refs are not
supported because registries cannot accept blob pushes to a digest.

To attach a cosign signature, run "installer sign <ref>" after push.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			reg, err := registry(false)
			if err != nil {
				return err
			}
			opts := ipkg.PushOptions{NoGit: noGit}
			if sourceRepo != "" || sourceRevision != "" || sourcePath != "" {
				if noGit || sourceRepo == "" || sourceRevision == "" {
					return fmt.Errorf("--source-repo and --source-revision go together, without --no-git-source")
				}
				opts.Git = &api.GitSource{Repository: ipkg.CleanRepoURL(sourceRepo), Revision: sourceRevision, Path: sourcePath}
				if opts.Git.Repository == "" {
					return fmt.Errorf("invalid --source-repo")
				}
			}
			res, err := reg.Push(ctx, args[0], args[1], opts)
			if err != nil {
				return err
			}
			fmt.Printf("Pushed %s\n", res.Ref)
			fmt.Printf("  manifest: %s\n", res.ManifestDigest)
			fmt.Printf("  layer:    %s (%d bytes, %d files)\n", res.LayerDigest, res.LayerSize, len(res.Files))
			return nil
		},
	}
	cmd.Flags().StringVar(&sourceRepo, "source-repo", "", "git repository the package was built from")
	cmd.Flags().StringVar(&sourceRevision, "source-revision", "", "git commit the package was built from")
	cmd.Flags().StringVar(&sourcePath, "source-path", "", "package directory within the repository")
	cmd.Flags().BoolVar(&noGit, "no-git-source", false, "don't record git source details")
	return cmd
}
