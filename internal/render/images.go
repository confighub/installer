// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package render

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/confighub/installer/internal/kustomize"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/pkg/api"
)

// applyImageOverrides sets each entry in overrides in the chosen base's
// kustomization.yaml, as `kustomize edit set image <name>=<ref>` would.
// Mutates the package working copy in place; the next `installer pull` /
// `installer upgrade` re-fetches and starts clean, so the mutation is
// non-persistent at the package source level. Allowed by Principle 1's carve-out for the installer's own
// `--set-image` mutations.
//
// Pre-flight: every name in overrides must be declared in the chosen
// base's `images:` block (ValidateImageOverrides).
//
// No-op when overrides is empty.
func applyImageOverrides(loaded *ipkg.Loaded, sel *api.Selection, overrides map[string]string) error {
	if len(overrides) == 0 {
		return nil
	}
	if err := ValidateImageOverrides(loaded.Package, loaded.Root, sel.Spec.Base, overrides); err != nil {
		return err
	}
	baseRel, err := basePathForName(loaded.Package, sel.Spec.Base)
	if err != nil {
		return err
	}
	baseDir := filepath.Join(loaded.Root, baseRel)
	kustPath := filepath.Join(baseDir, "kustomization.yaml")

	args := make([]string, 0, len(overrides))
	for name, ref := range overrides {
		args = append(args, name+"="+ref)
	}
	sort.Strings(args)
	if err := kustomize.SetImages(kustPath, args); err != nil {
		return fmt.Errorf("--set-image in %s: %w", baseDir, err)
	}
	return nil
}

// ValidateImageOverrides checks that every name overrides sets is declared
// in the images: block of base's kustomization.yaml, under packageRoot. The
// block is how a package author says which images may be overridden. An
// entry for any other name matches no container, and kustomize ignores it
// without a word, so a mistyped name would change nothing.
func ValidateImageOverrides(pkg *api.Package, packageRoot, base string, overrides map[string]string) error {
	if len(overrides) == 0 {
		return nil
	}
	baseRel, err := basePathForName(pkg, base)
	if err != nil {
		return err
	}
	kustPath := filepath.Join(packageRoot, baseRel, "kustomization.yaml")
	declared, err := declaredImages(kustPath)
	if err != nil {
		return err
	}
	var undeclared []string
	for name := range overrides {
		if !declared[name] {
			undeclared = append(undeclared, name)
		}
	}
	if len(undeclared) == 0 {
		return nil
	}
	sort.Strings(undeclared)
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Errorf("--set-image names %s, which the base %q doesn't declare in the images: block of %s; it declares %s",
		strings.Join(undeclared, ", "), base, kustPath, strings.Join(names, ", "))
}

// declaredImages returns the names in the images: block of the
// kustomization.yaml at path. A missing file, or one with no images: block,
// is an error that points the operator at the alternatives: setting an
// image would otherwise silently inject an images: block on a package
// whose author did not intend image overrides.
func declaredImages(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("--set-image was given but %s does not exist; the chosen base must contain a kustomization.yaml that declares an `images:` block", path)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		Images *[]struct {
			Name string `yaml:"name"`
		} `yaml:"images"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Images == nil {
		return nil, fmt.Errorf(
			"%s has no `images:` block; declare one to use --set-image, "+
				"or have the package author declare image inputs and a "+
				"set-container-image group in transformers (see "+
				"docs/principles.md Principle 5)", path)
	}
	declared := map[string]bool{}
	for _, img := range *doc.Images {
		declared[img.Name] = true
	}
	return declared, nil
}
