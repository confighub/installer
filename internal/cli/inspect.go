// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newInspectCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "inspect <package>",
		Short: "Print installer.yaml from an OCI artifact without pulling the layer",
		Long: `Inspect fetches only the manifest + config blob for a native installer OCI
artifact and prints the embedded installer.yaml plus bundle metadata
(layer digest, file list, the installer version that produced the bundle).

<package> is oci://<ref>, or <name>:<version> for a package ConfigHub
publishes, the newest release without :<version>.

This is the cheap-read path used by the resolver: it avoids downloading the
package layer when only metadata is needed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			reg, err := registry(false)
			if err != nil {
				return err
			}
			res, err := reg.Inspect(ctx, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}
			cb := res.Config
			fmt.Printf("Package:  %s@%s\n", cb.Manifest.Metadata.Name, cb.Manifest.InstallerMetadata.Version)
			fmt.Printf("Manifest: %s\n", res.ManifestDigest)
			if cb.Bundle.InstallerVersion != "" {
				fmt.Printf("Built with installer %s\n", cb.Bundle.InstallerVersion)
			}
			fmt.Printf("Layer:    %s\n", cb.Bundle.LayerDigest)
			fmt.Printf("Size:     %d bytes\n", cb.Bundle.LayerSize)
			fmt.Printf("Files:    %d\n", len(cb.Bundle.Files))
			fmt.Println()
			printPackageDoc(cb.Manifest)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}
