package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVerifyCmd() *cobra.Command {
	var policy string
	cmd := &cobra.Command{
		Use:   "verify <ref>",
		Short: "(not yet implemented) Verify the cosign signature on an OCI artifact",
		Long: `Verify checks the cosign signature attached to the given OCI artifact
against the trust policy at --policy (default: ~/.config/installer/policy.yaml).

Requires the cosign binary on PATH.

See docs/package-management-plan.md (Phase 7).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("verify not yet implemented; see docs/package-management-plan.md (Phase 7)")
		},
	}
	cmd.Flags().StringVar(&policy, "policy", "", "trust policy file (default: ~/.config/installer/policy.yaml)")
	return cmd
}
