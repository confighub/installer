package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/confighub/installer/internal/deps"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/render"
	"github.com/confighub/installer/pkg/api"
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
				if err := os.RemoveAll(filepath.Join(outDir, "secrets")); err != nil {
					return err
				}
			}

			facts, err := readFactsOptional(filepath.Join(specDir, "facts.yaml"))
			if err != nil {
				return err
			}

			// Lock-state preflight: fail fast before any output is written
			// if the package declares dependencies but the lock is missing
			// or stale. Read the lock here; render reuses it after the
			// parent renders.
			var lock *api.Lock
			if len(loaded.Package.Spec.Dependencies) > 0 {
				lock, err = deps.ReadLock(workDir)
				if err != nil {
					return err
				}
				if lock == nil {
					return fmt.Errorf("package declares dependencies but %s does not exist; run `installer deps update %s`",
						deps.LockPath(workDir), workDir)
				}
				if deps.IsStale(lock, loaded.Package) {
					return fmt.Errorf("lock at %s is stale (installer.yaml's dependencies have changed); run `installer deps update %s`",
						deps.LockPath(workDir), workDir)
				}
			}

			ctx := context.Background()
			result, err := render.Render(ctx, render.Options{
				Loaded:    loaded,
				Selection: sel,
				Inputs:    inputs,
				Facts:     facts,
			}, outDir)
			if err != nil {
				return err
			}

			fmt.Printf("Rendered %d manifest(s) to %s\n", len(result.Manifests), manifestsDir)
			if len(result.Secrets) > 0 {
				fmt.Printf("Rendered %d secret(s) to %s/secrets (not uploaded)\n", len(result.Secrets), outDir)
			}
			fmt.Printf("Spec docs in %s\n", specDir)

			// Multi-package render: lock was preflighted above; render each
			// dependency into its own subtree under out/<dep-name>/.
			if lock != nil {
				depResults, err := render.RenderDependencies(ctx, render.DepsOptions{
					Lock:         lock,
					ParentInputs: inputs,
					WorkDir:      workDir,
				})
				if err != nil {
					return err
				}
				for _, dr := range depResults {
					fmt.Printf("Rendered dep %s: %d manifest(s) to %s\n", dr.Name, len(dr.Manifests), filepath.Join(dr.OutDir, "manifests"))
					if len(dr.Secrets) > 0 {
						fmt.Printf("  secrets: %d (not uploaded)\n", len(dr.Secrets))
					}
				}
			}

			fmt.Printf("Next: installer upload %s --space <slug>\n", workDir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&clean, "clean", false, "remove out/manifests/ and out/secrets/ before rendering")
	return cmd
}

// readFactsOptional reads facts.yaml if it exists, returning nil otherwise.
func readFactsOptional(path string) (*api.Facts, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return api.ParseFacts(data)
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
