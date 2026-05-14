package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSignCmd() *cobra.Command {
	var key string
	cmd := &cobra.Command{
		Use:   "sign <ref>",
		Short: "(not yet implemented) Sign an OCI artifact with cosign",
		Long: `Sign invokes cosign to attach a signature to the given OCI artifact. Keyless
mode (Sigstore Fulcio + Rekor) is used unless --key is provided.

Requires the cosign binary on PATH.

See docs/package-management-plan.md (Phase 7).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("sign not yet implemented; see docs/package-management-plan.md (Phase 7)")
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "cosign key reference (default: keyless Sigstore flow)")
	return cmd
}
