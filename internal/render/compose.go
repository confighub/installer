// Package render takes a loaded package + Selection + Inputs and produces
// rendered Kubernetes manifests by:
//
//  1. Synthesizing a top-level kustomization.yaml that references the chosen
//     base and Components inside the package.
//  2. Shelling out to `kustomize build` to render the kustomize tree.
//  3. Resolving the package's FunctionChainTemplate against the Inputs (Go
//     templates), running each group via the ConfigHub function executor
//     SDK with its declared toolchain and whereResource.
//  4. Splitting the resulting multi-doc YAML into per-resource files with
//     deterministic naming, written to out/manifests/.
//  5. Persisting the resolved FunctionChain alongside selection.yaml and
//     inputs.yaml in out/spec/ so re-render is reproducible and the exact
//     transforms applied are inspectable.
package render

import (
	"fmt"
	"os"
	"path/filepath"

	ipkg "github.com/confighubai/installer/internal/pkg"
	"github.com/confighubai/installer/pkg/api"
	"gopkg.in/yaml.v3"
)

// composeKustomization writes a synthesized top-level kustomization.yaml into
// a fresh temp directory, referencing the chosen Base and Components by
// absolute path inside the loaded package. Returns the temp dir path; caller
// is responsible for cleanup.
func composeKustomization(loaded *ipkg.Loaded, sel *api.Selection) (string, error) {
	pkg := loaded.Package
	baseDir, err := basePathForName(pkg, sel.Spec.Base)
	if err != nil {
		return "", err
	}

	// kustomize rejects absolute paths in resources/components; compute
	// relative paths from the compose dir to each referenced directory.
	// Both paths must be symlink-resolved or kustomize's own EvalSymlinks
	// step produces nonsense (notably on macOS, where /var → /private/var).
	tmp, err := os.MkdirTemp("", "installer-compose-*")
	if err != nil {
		return "", err
	}
	tmpReal, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	rootReal, err := filepath.EvalSymlinks(loaded.Root)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}

	rel := func(target string) (string, error) {
		return filepath.Rel(tmpReal, filepath.Join(rootReal, target))
	}

	baseRel, err := rel(baseDir)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	composed := composedKustomization{
		APIVersion: "kustomize.config.k8s.io/v1beta1",
		Kind:       "Kustomization",
		Resources:  []string{baseRel},
	}
	for _, name := range sel.Spec.Components {
		path, err := componentPathForName(pkg, name)
		if err != nil {
			_ = os.RemoveAll(tmp)
			return "", err
		}
		r, err := rel(path)
		if err != nil {
			_ = os.RemoveAll(tmp)
			return "", err
		}
		composed.Components = append(composed.Components, r)
	}

	body, err := yaml.Marshal(composed)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, "kustomization.yaml"), body, 0o644); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	return tmp, nil
}

func basePathForName(pkg *api.Package, name string) (string, error) {
	for _, b := range pkg.Spec.Bases {
		if b.Name == name {
			return b.Path, nil
		}
	}
	return "", fmt.Errorf("base %q not found in package %q", name, pkg.Metadata.Name)
}

func componentPathForName(pkg *api.Package, name string) (string, error) {
	for _, c := range pkg.Spec.Components {
		if c.Name == name {
			return c.Path, nil
		}
	}
	return "", fmt.Errorf("component %q not found in package %q", name, pkg.Metadata.Name)
}

type composedKustomization struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Resources  []string `yaml:"resources,omitempty"`
	Components []string `yaml:"components,omitempty"`
}
