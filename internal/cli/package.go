package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/confighubai/installer/internal/bundle"
	ipkg "github.com/confighubai/installer/internal/pkg"
)

func newPackageCmd() *cobra.Command {
	var outFile string
	cmd := &cobra.Command{
		Use:   "package <dir>",
		Short: "Bundle a package source tree into a deterministic .tgz",
		Long: `Package bundles a package source tree (the directory containing installer.yaml,
bases/, components/, validation/, and an optional collector) into a
byte-deterministic .tgz suitable for installer push.

Refuses to bundle: *.env.secret files, anything under out/, anything matched
by .installerignore (gitignore syntax). The package is NOT rendered —
distribution carries the source tree so wizard customization can run at
install time.

Determinism: the same source tree always produces a .tgz with the same
sha256, regardless of host platform, file mtimes, or walk order. This is
what makes signing and digest-pinning meaningful.

See docs/package-management-plan.md (Phase 1).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			loaded, err := ipkg.Load(src)
			if err != nil {
				return fmt.Errorf("load %s: %w", src, err)
			}
			dst := outFile
			if dst == "" {
				name := loaded.Package.Metadata.Name
				ver := loaded.Package.Metadata.Version
				if name == "" {
					return fmt.Errorf("installer.yaml is missing metadata.name; pass --out to override")
				}
				if ver == "" {
					dst = name + ".tgz"
				} else {
					dst = name + "-" + ver + ".tgz"
				}
			}
			res, err := bundle.Bundle(src, dst)
			if err != nil {
				return err
			}
			fmt.Printf("Bundled %s@%s\n", loaded.Package.Metadata.Name, loaded.Package.Metadata.Version)
			fmt.Printf("  output: %s\n", dst)
			fmt.Printf("  files:  %d\n", len(res.Files))
			fmt.Printf("  size:   %d bytes\n", res.Size)
			fmt.Printf("  digest: sha256:%s\n", res.Digest)
			return nil
		},
	}
	cmd.Flags().StringVarP(&outFile, "out", "o", "", "output .tgz path (default: <name>-<version>.tgz in cwd)")
	return cmd
}
