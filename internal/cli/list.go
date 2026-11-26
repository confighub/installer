// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <package|oci-repo>",
		Short: "List tags of an OCI repo holding installer artifacts",
		Long: `List queries the OCI /tags/list endpoint for the given repo and prints
the tags. A bare package name lists the releases of a package ConfigHub
publishes.

Examples:
  installer list k8sbot
  installer list oci://ghcr.io/confighubai/gateway-api`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			reg, err := registry(false)
			if err != nil {
				return err
			}
			tags, err := reg.List(ctx, args[0])
			if err != nil {
				return err
			}
			for _, t := range tags {
				fmt.Println(t)
			}
			return nil
		},
	}
	return cmd
}
