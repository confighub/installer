package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// plan and preflight are deferred but exposed as commands so the surface is
// visible from `installer --help`. They print a clear message about what's
// coming and (where useful) a manual fallback.

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

func trimExt(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[:i]
		}
	}
	return name
}
