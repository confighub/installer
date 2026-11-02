// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package kustimport

import (
	"sort"

	"github.com/confighub/installer/internal/kustomize"
	"sigs.k8s.io/kustomize/api/pkg/util"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/types"
)

// seedImages returns one images: entry per image name in the built output,
// each restating the tag and digest the output already has, so the entries
// change nothing until an operator overrides one with --set-image. An image
// name used with more than one tag or digest gets a bare entry: kustomize
// matches entries by name, so restating either reference would rewrite the
// other.
func seedImages(m resmap.ResMap) []types.Image {
	byName := map[string]*types.Image{}
	conflicting := map[string]bool{}
	for _, r := range m.Resources() {
		obj, err := r.Map()
		if err != nil {
			continue
		}
		for _, ref := range kustomize.ContainerImages(obj) {
			name, tag, digest := util.SplitImageName(ref)
			img := types.Image{Name: name, NewTag: tag, Digest: digest}
			prev, ok := byName[img.Name]
			if !ok {
				byName[img.Name] = &img
				continue
			}
			if *prev != img {
				conflicting[img.Name] = true
			}
		}
	}
	out := make([]types.Image, 0, len(byName))
	for name, img := range byName {
		if conflicting[name] {
			out = append(out, types.Image{Name: name})
			continue
		}
		out = append(out, *img)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
