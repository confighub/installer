// Package pkg loads installer packages — kustomize trees containing an
// installer.yaml manifest, distributed as local directories, .tgz archives,
// or Helm OCI artifacts.
package pkg

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/confighubai/installer/pkg/api"
)

// Loaded is a parsed package on disk: the root directory and the parsed manifest.
type Loaded struct {
	// Root is the absolute path to the package's root directory (the dir that
	// contains installer.yaml and the kustomize tree).
	Root string
	// Package is the parsed installer.yaml.
	Package *api.Package
}

const ManifestFile = "installer.yaml"

// Load reads installer.yaml from a local directory and returns the parsed
// package. Use Pull to fetch a remote artifact first.
func Load(dir string) (*Loaded, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", dir, err)
	}
	manifestPath := filepath.Join(abs, ManifestFile)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", manifestPath, err)
	}
	pkg, err := api.ParsePackage(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", manifestPath, err)
	}
	if err := validateLayout(abs, pkg); err != nil {
		return nil, err
	}
	return &Loaded{Root: abs, Package: pkg}, nil
}

// validateLayout sanity-checks that referenced base/component paths exist.
func validateLayout(root string, pkg *api.Package) error {
	for _, b := range pkg.Spec.Bases {
		if err := requireKustomization(filepath.Join(root, b.Path)); err != nil {
			return fmt.Errorf("base %q: %w", b.Name, err)
		}
	}
	for _, c := range pkg.Spec.Components {
		if err := requireKustomization(filepath.Join(root, c.Path)); err != nil {
			return fmt.Errorf("component %q: %w", c.Name, err)
		}
	}
	return nil
}

func requireKustomization(dir string) error {
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no kustomization.yaml in %s", dir)
}
