package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

// plan, preflight, and upload are deferred but exposed as commands so the
// surface is visible from `installer --help`. They print a clear message
// about what's coming and (where useful) a manual fallback.

func newPlanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "plan <work-dir>",
		Short: "(not yet implemented) Show what render would change",
		Long: `Plan re-renders into a temp dir and diffs against the previous render
and (with --space) against ConfigHub. Not yet implemented.

For now: re-run "installer render --clean" and inspect the diff manually with
git diff or cub unit diff.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("plan not yet implemented; use 'installer render --clean' and diff out/manifests")
		},
	}
}

func newPreflightCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "preflight <work-dir>",
		Short: "(not yet implemented) Check cluster against the package's externalRequires",
		Long: `Preflight evaluates the package's externalRequires (CRDs, ClusterFeatures,
GatewayClass, WebhookCertProvider, Operator) against a live cluster. Not yet
implemented; the schema is parsed but no probes run yet.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("preflight not yet implemented")
		},
	}
}

func newUploadCmd() *cobra.Command {
	var space string
	cmd := &cobra.Command{
		Use:   "upload <work-dir>",
		Short: "Upload rendered manifests to a ConfigHub space",
		Long: `Upload sends <work-dir>/out/manifests/*.yaml to a ConfigHub space as one
Unit per file. Today this requires the cub CLI on PATH and one cub unit
create per file (the bulk -f mode is a planned cub addition); future
versions will batch via 'cub unit create -f <dir>' and label with phases.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if space == "" {
				return fmt.Errorf("--space is required")
			}
			if _, err := exec.LookPath("cub"); err != nil {
				return fmt.Errorf("cub CLI not found on PATH: %w", err)
			}
			workDir, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			manifestsDir := filepath.Join(workDir, "out", "manifests")
			entries, err := os.ReadDir(manifestsDir)
			if err != nil {
				return fmt.Errorf("read %s: %w", manifestsDir, err)
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				slug := trimExt(e.Name())
				path := filepath.Join(manifestsDir, e.Name())
				ccmd := exec.Command("cub", "unit", "create", "--space", space, slug, path)
				ccmd.Stdout = os.Stdout
				ccmd.Stderr = os.Stderr
				if err := ccmd.Run(); err != nil {
					return fmt.Errorf("cub unit create %s: %w", slug, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&space, "space", "", "destination ConfigHub space slug")
	return cmd
}

func trimExt(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[:i]
		}
	}
	return name
}
