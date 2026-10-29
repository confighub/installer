// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package render

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

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
// Pre-flight: the chosen base's kustomization.yaml must declare an
// `images:` block. If it does not, returns an error pointing the
// operator at the recommended alternatives. The check exists because
// setting an image would otherwise silently inject an `images:` block
// on a package whose author did not intend image overrides.
//
// No-op when overrides is empty.
func applyImageOverrides(loaded *ipkg.Loaded, sel *api.Selection, overrides map[string]string) error {
	if len(overrides) == 0 {
		return nil
	}
	baseRel, err := basePathForName(loaded.Package, sel.Spec.Base)
	if err != nil {
		return err
	}
	baseDir := filepath.Join(loaded.Root, baseRel)
	kustPath := filepath.Join(baseDir, "kustomization.yaml")

	if err := requireImagesBlock(kustPath); err != nil {
		return err
	}

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

// requireImagesBlock parses kustomization.yaml at path and returns an
// error if the top-level `images:` field is absent. Empty file or
// missing file is treated as "no images block" — render fails fast
// either way.
func requireImagesBlock(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("--set-image was given but %s does not exist; the chosen base must contain a kustomization.yaml that declares an `images:` block", path)
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if _, ok := doc["images"]; !ok {
		return fmt.Errorf(
			"%s has no `images:` block; declare one to use --set-image, "+
				"or have the package author declare image inputs and a "+
				"set-container-image group in transformers (see "+
				"docs/principles.md Principle 5)", path)
	}
	return nil
}
