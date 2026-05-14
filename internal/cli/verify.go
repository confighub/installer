package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/confighub/installer/internal/sign"
	"github.com/confighub/installer/pkg/api"
)

func newVerifyCmd() *cobra.Command {
	var (
		key      string
		identity string
		issuer   string
		policy   bool
	)
	cmd := &cobra.Command{
		Use:   "verify <ref>",
		Short: "Verify the cosign signature on an OCI artifact",
		Long: `Verify checks the cosign signature attached to the given OCI artifact.

Verification mode is chosen by flags:

  --policy           run every matching entry in the trust policy (default
                     file: ~/.config/installer/policy.yaml, override via
                     INSTALLER_SIGNING_POLICY). Succeeds on first match.
  --key <ref>        keyed verification against this cosign public-key ref.
  --identity <id>    keyless verification — combine with --issuer.
  --issuer <url>     keyless OIDC issuer URL.

Exactly one mode must be specified. Requires the cosign binary on PATH.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			ref := args[0]
			modes := 0
			if policy {
				modes++
			}
			if key != "" {
				modes++
			}
			if identity != "" || issuer != "" {
				modes++
			}
			if modes == 0 {
				return fmt.Errorf("one of --policy, --key, or --identity+--issuer is required")
			}
			if modes > 1 {
				return fmt.Errorf("--policy, --key, and --identity/--issuer are mutually exclusive")
			}
			switch {
			case policy:
				p, err := sign.LoadPolicy()
				if err != nil {
					return err
				}
				if p == nil {
					return fmt.Errorf("no signing policy file found; pass --key or --identity/--issuer")
				}
				// Force enforcement for the duration of this verify call,
				// even if the file says enforce:false.
				p.Spec.Enforce = true
				// Re-use EnforceVerification's matching logic by writing
				// the loaded policy back via env… simpler: just iterate
				// the entries here and call Verify per match.
				return verifyAgainstPolicy(ctx, ref, p)
			case key != "":
				return sign.Verify(ctx, ref, sign.VerifyOptions{
					TrustedKey: &api.TrustedKey{PublicKey: key},
				})
			default:
				if identity == "" || issuer == "" {
					return fmt.Errorf("--identity and --issuer must both be set for keyless verification")
				}
				return sign.Verify(ctx, ref, sign.VerifyOptions{
					TrustedKeyless: &api.TrustedKeyless{Identity: identity, Issuer: issuer},
				})
			}
		},
	}
	cmd.Flags().BoolVar(&policy, "policy", false, "verify against the trust policy file (default: ~/.config/installer/policy.yaml)")
	cmd.Flags().StringVar(&key, "key", "", "cosign public-key reference for keyed verification")
	cmd.Flags().StringVar(&identity, "identity", "", "Sigstore certificate identity for keyless verification")
	cmd.Flags().StringVar(&issuer, "issuer", "", "Sigstore OIDC issuer URL for keyless verification")
	return cmd
}

func verifyAgainstPolicy(ctx context.Context, ref string, p *api.SigningPolicy) error {
	if len(p.Spec.TrustedKeys) == 0 && len(p.Spec.TrustedKeyless) == 0 {
		return fmt.Errorf("policy has no trust entries")
	}
	var attempts []string
	for _, k := range p.Spec.TrustedKeys {
		if err := sign.Verify(ctx, ref, sign.VerifyOptions{TrustedKey: &k}); err == nil {
			fmt.Printf("Verified %s via trustedKey %s\n", ref, k.PublicKey)
			return nil
		} else {
			attempts = append(attempts, fmt.Sprintf("key %s: %v", k.PublicKey, err))
		}
	}
	for _, k := range p.Spec.TrustedKeyless {
		if err := sign.Verify(ctx, ref, sign.VerifyOptions{TrustedKeyless: &k}); err == nil {
			fmt.Printf("Verified %s via trustedKeyless %s@%s\n", ref, k.Identity, k.Issuer)
			return nil
		} else {
			attempts = append(attempts, fmt.Sprintf("identity %s: %v", k.Identity, err))
		}
	}
	return fmt.Errorf("no trust entry verified %s: %v", ref, attempts)
}
