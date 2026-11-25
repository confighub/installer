// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/confighub/installer/pkg/installer"
)

func newShowCmd() *cobra.Command {
	var (
		space  string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "show [<package>]",
		Short: "Show what was installed in a Space, from its installer record in ConfigHub",
		Long: `Show reads the installer record an upload keeps in ConfigHub and prints what it
says about the install: the package and where it was pulled from, the base and
the components selected among those the package offers, each input's answer
beside its default, the facts the collector found, the dependencies locked, the
cluster requirements of what was selected, and the functions render ran.

The record is the hidden Unit named installer in the install's Space, which
"cub unit data --space <space> installer" prints as stored. Name the Space with
--space, or the package, when the organization has one install of it.

Use --json for the record as stored, as JSON.`,
		Example: `  installer show --space nonprod-c3agent
  installer show c3agent`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			packageName := ""
			if len(args) == 1 {
				packageName = args[0]
			}
			if packageName == "" && space == "" {
				return fmt.Errorf("name the Space with --space, or the package")
			}
			ctx := commandContext(cmd)
			inst, err := newInstaller(ctx, false)
			if err != nil {
				return err
			}
			found, err := inst.FindRecord(ctx, space, packageName)
			switch {
			case errors.Is(err, installer.ErrNoConfigHub) && inst.hubErr != nil:
				return notAuthenticated(inst.hubErr)
			case errors.Is(err, installer.ErrNotAuthenticated):
				return notAuthenticated(err)
			case errors.Is(err, installer.ErrSeveralPackages):
				return fmt.Errorf("%v; name the package: `%s show <package> --space %s`", err, InvocationName(), space)
			case errors.Is(err, installer.ErrSeveralInstalls):
				return fmt.Errorf("%v; pass --space to choose one", err)
			case err != nil:
				return err
			}
			if asJSON {
				var doc any
				if err := yaml.Unmarshal(found.Data, &doc); err != nil {
					return fmt.Errorf("parse installer record: %w", err)
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(doc)
			}
			return installer.WriteRecord(os.Stdout, found)
		},
	}
	cmd.Flags().StringVar(&space, "space", "", "Space the package is installed in (default: the package's only install in the organization)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the record as stored, as JSON")
	return cmd
}
