// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package render

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"sigs.k8s.io/kustomize/api/konfig"
	"sigs.k8s.io/kustomize/api/pkg/util"
	"sigs.k8s.io/kustomize/api/types"

	"github.com/confighub/installer/internal/kustomize"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/pkg/api"
)

// applyImageOverrides sets each entry in overrides as `kustomize edit set
// image <name>=<ref>` would, and returns the images: block of the composed
// kustomization, which carries every override.
//
// A kustomization's images: block changes only the containers of its own
// resources, so the base's block never reaches an image that only a
// component uses. The composed kustomization includes the base and the
// selected components, so its block reaches every container of the render.
// The base and each selected component that declares a name in its own
// images: block have that entry edited too: an entry that sets newName
// renames the image before the composed kustomization sees it.
//
// Editing them mutates the package working copy in place; the next
// `installer pull` / `installer upgrade` re-fetches and starts clean, so
// the mutation is non-persistent at the package source level. Allowed by
// Principle 1's carve-out for the installer's own `--set-image` mutations.
//
// Pre-flight: every name in overrides must be one the render would change
// (ValidateImageOverrides).
//
// No-op when overrides is empty.
func applyImageOverrides(loaded *ipkg.Loaded, sel *api.Selection, overrides map[string]string) ([]types.Image, error) {
	if len(overrides) == 0 {
		return nil, nil
	}
	if err := ValidateImageOverrides(loaded.Package, loaded.Root, sel.Spec.Base, sel.Spec.Components, overrides); err != nil {
		return nil, err
	}
	paths, err := selectedKustomizations(loaded.Package, loaded.Root, sel.Spec.Base, sel.Spec.Components)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, path := range paths {
		declared, err := declaredImages(path)
		if err != nil {
			return nil, err
		}
		var args []string
		for _, name := range names {
			if declared[name] {
				args = append(args, name+"="+overrides[name])
			}
		}
		if len(args) == 0 {
			continue
		}
		if err := kustomize.SetImages(path, args); err != nil {
			return nil, fmt.Errorf("--set-image in %s: %w", filepath.Dir(path), err)
		}
	}

	images := make([]types.Image, 0, len(names))
	for _, name := range names {
		img, err := kustomize.ParseImage(name + "=" + overrides[name])
		if err != nil {
			return nil, fmt.Errorf("--set-image: %w", err)
		}
		images = append(images, img)
	}
	return images, nil
}

// ValidateImageOverrides checks that the render of base and components,
// under packageRoot, would change every name overrides sets: one declared in
// the images: block of the base or of a selected component, or the name,
// without tag or digest, of the image of a container they include. An entry
// for any other name matches no container, and kustomize ignores it without
// a word, so a mistyped name would change nothing.
func ValidateImageOverrides(pkg *api.Package, packageRoot, base string, components []string, overrides map[string]string) error {
	if len(overrides) == 0 {
		return nil
	}
	paths, err := selectedKustomizations(pkg, packageRoot, base, components)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, path := range paths {
		declared, err := declaredImages(path)
		if err != nil {
			return err
		}
		for name := range declared {
			known[name] = true
		}
	}
	var unknown []string
	for name := range overrides {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}

	// Only a name no images: block declares needs the build.
	used, err := containerImageNames(pkg, packageRoot, base, components)
	if err != nil {
		return err
	}
	unknown = slices.DeleteFunc(unknown, func(name string) bool { return used[name] })
	if len(unknown) == 0 {
		return nil
	}
	for name := range used {
		known[name] = true
	}
	sort.Strings(unknown)
	var what string
	if len(components) > 0 {
		what = fmt.Sprintf("the base %q and components %s", base, strings.Join(components, ", "))
	} else {
		what = fmt.Sprintf("the base %q", base)
	}
	if len(known) == 0 {
		return fmt.Errorf("--set-image names %s, but %s run no containers and declare no images: block",
			strings.Join(unknown, ", "), what)
	}
	return fmt.Errorf("--set-image names %s, which no container or images: block of %s uses; "+
		"the names they use, which are images without a tag or digest, are %s",
		strings.Join(unknown, ", "), what, strings.Join(slices.Sorted(maps.Keys(known)), ", "))
}

// selectedKustomizations returns the path of the kustomization file of the
// base and of each component, in that order.
func selectedKustomizations(pkg *api.Package, packageRoot, base string, components []string) ([]string, error) {
	baseRel, err := basePathForName(pkg, base)
	if err != nil {
		return nil, err
	}
	dirs := []string{baseRel}
	for _, name := range components {
		rel, err := componentPathForName(pkg, name)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, rel)
	}
	paths := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		path, err := kustomizationFile(filepath.Join(packageRoot, dir))
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// kustomizationFile returns the path of the kustomization file in dir,
// under any of the names kustomize recognizes.
func kustomizationFile(dir string) (string, error) {
	for _, name := range konfig.RecognizedKustomizationFileNames() {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("%s has no kustomization file", dir)
}

// declaredImages returns the names in the images: block of the
// kustomization file at path, empty when it has none.
func declaredImages(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var k types.Kustomization
	if err := k.Unmarshal(data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	declared := map[string]bool{}
	for _, img := range k.Images {
		declared[img.Name] = true
	}
	return declared, nil
}

// containerImageNames returns the name, without tag or digest, of the image
// of every container that base and components include, built as the
// composed kustomization includes them. The package's transformers, which
// run after the composed kustomization's images: block, are left out.
func containerImageNames(pkg *api.Package, packageRoot, base string, components []string) (map[string]bool, error) {
	dir, err := os.MkdirTemp("", "installer-images-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	k, err := wrapSelection(pkg, packageRoot, base, components, dir)
	if err != nil {
		return nil, err
	}
	body, err := yaml.Marshal(k)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), body, 0o644); err != nil {
		return nil, err
	}
	m, err := kustomize.Build(dir)
	if err != nil {
		return nil, fmt.Errorf("--set-image: build the base %q and its components to find the images they use: %w", base, err)
	}
	names := map[string]bool{}
	for _, r := range m.Resources() {
		obj, err := r.Map()
		if err != nil {
			return nil, err
		}
		for _, ref := range kustomize.ContainerImages(obj) {
			name, _, _ := util.SplitImageName(ref)
			names[name] = true
		}
	}
	return names, nil
}
