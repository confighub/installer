// Command installer renders config-as-data Kubernetes packages into per-
// resource YAML files for upload to ConfigHub. It can be invoked standalone
// or as a `cub` plugin (cub install ...).
package main

import (
	"fmt"
	"os"

	"github.com/confighubai/installer/internal/cli"
)

func main() {
	cmd := cli.NewRoot()
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
