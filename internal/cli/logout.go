package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	ipkg "github.com/confighub/installer/internal/pkg"
)

func newLogoutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout <registry>",
		Short: "Remove stored credentials for a registry",
		Long: `Logout removes the stored credential for the given registry from the
docker-config-style credential store.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			if err := ipkg.Logout(ctx, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Removed credentials for %s\n", args[0])
			return nil
		},
	}
	return cmd
}
