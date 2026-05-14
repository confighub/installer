package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	ipkg "github.com/confighub/installer/internal/pkg"
)

func newPullCmd() *cobra.Command {
	var outDir string
	cmd := &cobra.Command{
		Use:   "pull <package-ref>",
		Short: "Fetch a package to a local directory",
		Long: `Pull a package reference (local path, .tgz, or oci://...) to a local
directory. Subsequent commands (wizard, render) read from the directory.

For local-directory references with no --out, this is a no-op that just
resolves and prints the absolute path.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			dir, err := ipkg.Pull(ctx, args[0], outDir)
			if err != nil {
				return err
			}
			loaded, err := ipkg.Load(dir)
			if err != nil {
				return err
			}
			fmt.Printf("Pulled %s@%s to %s\n",
				loaded.Package.Metadata.Name,
				loaded.Package.Metadata.Version,
				loaded.Root)
			return nil
		},
	}
	cmd.Flags().StringVar(&outDir, "out", "", "destination directory for fetched package")
	return cmd
}
