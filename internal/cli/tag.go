package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	ipkg "github.com/confighubai/installer/internal/pkg"
)

func newTagCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tag <src-ref> <dst-tag>",
		Short: "Apply a new tag to an existing OCI artifact",
		Long: `Tag adds a new tag (typically a channel like 'stable' or '0.3') pointing at
the same manifest digest as src-ref. The original tag is preserved. Channel
tags are convention-only — installer treats them as mutable aliases that
authors maintain.

Example:
  installer tag oci://ghcr.io/confighubai/foo:0.3.2 stable`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			if err := ipkg.Tag(ctx, args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("Tagged %s as %s\n", args[0], args[1])
			return nil
		},
	}
	return cmd
}
