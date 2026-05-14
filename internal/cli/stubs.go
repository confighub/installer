package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// preflight is deferred but exposed as a command so the surface is
// visible from `installer --help`.

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
