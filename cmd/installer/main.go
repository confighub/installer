// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Command installer renders config-as-data Kubernetes packages into per-
// resource YAML files for upload to ConfigHub. It can be invoked standalone
// or as a `cub` plugin (cub installer ...).
package main

import (
	"fmt"
	"os"

	"github.com/confighub/sdk/core/plugin"

	"github.com/confighub/installer/internal/cli"
	"github.com/confighub/installer/internal/version"
)

func main() {
	// When cub installs or upgrades this plugin it invokes the binary as a
	// hook; HandleHook writes cub-plugin.yaml into CUB_PLUGIN_DIR and we exit
	// without running the normal command tree.
	manifest := plugin.Manifest{
		Name:    "installer",
		Version: version.Version,
		Commands: []plugin.Command{{
			Name:    "installer",
			Summary: "Render and install Kubernetes config-as-data packages",
		}},
	}
	if handled, err := plugin.HandleHook(manifest); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	cmd := cli.NewRoot()
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
