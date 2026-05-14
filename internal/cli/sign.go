package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/confighub/installer/internal/sign"
)

func newSignCmd() *cobra.Command {
	var (
		key       string
		recursive bool
		yes       bool
	)
	cmd := &cobra.Command{
		Use:   "sign <ref>",
		Short: "Sign an OCI artifact with cosign",
		Long: `Sign attaches a cosign signature to the given OCI artifact. Keyless
mode (Sigstore Fulcio + Rekor) is used unless --key is provided.

Requires the cosign binary on PATH (override via INSTALLER_COSIGN_BIN).

Examples:
  # Keyless (interactive OIDC flow):
  installer sign oci://ghcr.io/me/pkg:0.1.0 --yes

  # Keyed:
  installer sign oci://ghcr.io/me/pkg:0.1.0 --key cosign.key`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			if err := sign.Sign(ctx, args[0], sign.SignOptions{
				Key: key, Recursive: recursive, Yes: yes,
			}); err != nil {
				return err
			}
			fmt.Printf("Signed %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "cosign key reference (default: keyless Sigstore flow)")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "sign every layer in addition to the manifest")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip cosign's interactive confirmation prompt")
	return cmd
}
