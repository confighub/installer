package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	ipkg "github.com/confighubai/installer/internal/pkg"
	"github.com/confighubai/installer/internal/render"
	"github.com/confighubai/installer/pkg/api"
)

func newRenderCmd() *cobra.Command {
	var (
		clean bool
	)
	cmd := &cobra.Command{
		Use:   "render <work-dir>",
		Short: "Render selection + inputs into per-resource Kubernetes YAML",
		Long: `Render reads <work-dir>/package + <work-dir>/out/spec/{selection,inputs}.yaml,
runs kustomize on the chosen base + components, then runs the package's
function chain template (resolved against the inputs) via the ConfigHub
function executor SDK. Output goes to <work-dir>/out/manifests/ as one YAML
file per resource. The resolved function-chain.yaml and a manifest-index.yaml
are written to <work-dir>/out/spec/.

The kustomize binary must be on PATH.

Render is deterministic: the same package + selection + inputs always produce
identical bytes. Re-render after editing selection.yaml or inputs.yaml.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workDir, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			pkgDir := filepath.Join(workDir, "package")
			outDir := filepath.Join(workDir, "out")
			specDir := filepath.Join(outDir, "spec")
			manifestsDir := filepath.Join(outDir, "manifests")

			loaded, err := ipkg.Load(pkgDir)
			if err != nil {
				return fmt.Errorf("load package from %s: %w", pkgDir, err)
			}

			sel, err := readSelection(filepath.Join(specDir, "selection.yaml"))
			if err != nil {
				return err
			}
			inputs, err := readInputs(filepath.Join(specDir, "inputs.yaml"))
			if err != nil {
				return err
			}

			if clean {
				if err := os.RemoveAll(manifestsDir); err != nil {
					return err
				}
			}

			ctx := context.Background()
			result, err := render.Render(ctx, render.Options{
				Loaded:    loaded,
				Selection: sel,
				Inputs:    inputs,
			}, outDir)
			if err != nil {
				return err
			}

			fmt.Printf("Rendered %d resource(s) to %s\n", len(result.Files), manifestsDir)
			fmt.Printf("Spec docs in %s\n", specDir)
			fmt.Printf("Next: installer upload %s --space <slug>\n", workDir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&clean, "clean", false, "remove out/manifests/ before rendering")
	return cmd
}

func readSelection(path string) (*api.Selection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return api.ParseSelection(data)
}

func readInputs(path string) (*api.Inputs, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return api.ParseInputs(data)
}
