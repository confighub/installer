// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Package kustomize runs the kustomize operations the installer needs
// in-process, through kustomize's Go API, so neither authors nor operators
// need the kustomize binary. The kustomize version is the one this module
// requires, which moves with the ConfigHub SDK's Kubernetes dependencies.
package kustomize

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/pkg/util"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	"github.com/confighub/installer/internal/chainexec"
	"github.com/confighub/installer/pkg/api"
)

// Build builds the kustomization in dir as
// `kustomize build --enable-exec --enable-alpha-plugins dir` does, so the
// synthesized out/compose/ tree still reproduces with the binary.
func Build(dir string) (resmap.ResMap, error) {
	opts := krusty.MakeDefaultOptions()
	// The CLI's --reorder defaults to legacy; the library's to none.
	opts.Reorder = krusty.ReorderOptionLegacy
	opts.PluginConfig = types.EnabledPluginConfig(types.BploUseStaticallyLinked)
	opts.PluginConfig.FnpLoadingOptions = types.FnPluginLoadingOptions{
		EnableExec:  true,
		NetworkName: "bridge",
	}
	return krusty.MakeKustomizer(opts).Run(filesys.MakeFsOnDisk(), dir)
}

// BuildYAML is Build, returning the multi-document YAML the CLI prints.
func BuildYAML(dir string) ([]byte, error) {
	m, err := Build(dir)
	if err != nil {
		return nil, err
	}
	return m.AsYaml()
}

// preserve as a new name, tag or digest keeps the existing entry's value.
const preserve = "*"

// SetImages updates the images: block of the kustomization file at path as
// `kustomize edit set image args...` does. Each arg is <name>=<ref> or
// <ref>; a ref part of "*" keeps that part of an existing entry. An entry
// for a name in args replaces the existing one, and the block is sorted by
// name. Unlike the CLI, the rest of the file, comments included, is left as
// it was.
func SetImages(path string, args []string) error {
	byName := map[string]types.Image{}
	for _, arg := range args {
		img, err := parseImageArg(arg)
		if err != nil {
			return err
		}
		byName[img.Name] = img
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var k types.Kustomization
	if err := k.Unmarshal(data); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, existing := range k.Images {
		img, ok := byName[existing.Name]
		if !ok {
			byName[existing.Name] = existing
			continue
		}
		if img.NewName == preserve {
			img.NewName = existing.NewName
		}
		if img.NewTag == preserve {
			img.NewTag = existing.NewTag
		}
		if img.Digest == preserve {
			img.Digest = existing.Digest
		}
		byName[existing.Name] = img
	}

	images := make([]types.Image, 0, len(byName))
	for _, img := range byName {
		// With no existing entry to keep from, "*" keeps nothing.
		if img.NewName == preserve {
			img.NewName = ""
		}
		if img.NewTag == preserve {
			img.NewTag = ""
		}
		if img.Digest == preserve {
			img.Digest = ""
		}
		images = append(images, img)
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Name < images[j].Name })
	return WriteImages(path, images)
}

func parseImageArg(arg string) (types.Image, error) {
	key, value, hasKey := strings.Cut(arg, "=")
	if !hasKey {
		value = arg
	}
	name, tag, digest := util.SplitImageName(value)
	if name == arg {
		return types.Image{}, fmt.Errorf("invalid image %q: want <name>=<ref>, <name>:<tag> or <name>@<digest>", arg)
	}
	img := types.Image{NewTag: tag, Digest: digest}
	if hasKey {
		img.Name, img.NewName = key, name
	} else {
		img.Name = name
	}
	return img, nil
}

// AddResource appends resource to the resources: list of the kustomization
// file at path, as `kustomize edit add resource` does, unless it is already
// listed. The rest of the file, comments included, is left as it was.
func AddResource(path, resource string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var k types.Kustomization
	if err := k.Unmarshal(data); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if slices.Contains(k.Resources, resource) {
		return nil
	}
	return setYQ(path, data, ".resources = (.resources // []) + [$params.resource]", "resource="+resource)
}

// WriteImages replaces the images: block of the kustomization file at path
// with the set-yq function, which leaves the rest of the file, comments
// included, as it was. No images leaves the file unchanged.
func WriteImages(path string, images []types.Image) error {
	if len(images) == 0 {
		return nil
	}
	list, err := json.Marshal(images)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return setYQ(path, data, ".images = "+string(list))
}

// setYQ writes data, the contents of the file at path, back to it after
// running the set-yq function with args. A kustomization has no
// metadata.name, which Kubernetes/YAML functions require, so it is edited
// as AppConfig/YAML.
func setYQ(path string, data []byte, args ...string) error {
	chain := &api.FunctionChain{Spec: api.FunctionChainSpec{Groups: []api.FunctionGroup{{
		Toolchain:   "AppConfig/YAML",
		Invocations: []api.FunctionInvocation{{Name: "set-yq", Args: args}},
	}}}}
	out, err := chainexec.RunChain(context.Background(), chain, data)
	if err != nil {
		return fmt.Errorf("edit %s: %w", path, err)
	}
	return os.WriteFile(path, out, 0o644)
}
