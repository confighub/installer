package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	ipkg "github.com/confighub/installer/internal/pkg"
)

func newPushCmd() *cobra.Command {
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
			res, err := ipkg.Push(ctx, args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Printf("Pushed %s\n", res.Ref)
			fmt.Printf("  manifest: %s\n", res.ManifestDigest)
			fmt.Printf("  layer:    %s (%d bytes, %d files)\n", res.LayerDigest, res.LayerSize, len(res.Files))
			return nil
		},
	}
	return cmd
}
