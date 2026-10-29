// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package kustimport

import (
	"sort"
	"strings"

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
		for _, ref := range containerImages(obj) {
			img := parseImage(ref)
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

// containerImages returns the image of every container and init container
// anywhere in obj, the fields kustomize's images transformer rewrites.
func containerImages(obj any) []string {
	var out []string
	switch v := obj.(type) {
	case map[string]any:
		for key, val := range v {
			if key == "containers" || key == "initContainers" {
				if list, ok := val.([]any); ok {
					for _, c := range list {
						if cm, ok := c.(map[string]any); ok {
							if image, ok := cm["image"].(string); ok && image != "" {
								out = append(out, image)
							}
						}
					}
				}
			}
			out = append(out, containerImages(val)...)
		}
	case []any:
		for _, val := range v {
			out = append(out, containerImages(val)...)
		}
	}
	return out
}

// parseImage splits an image reference into the images: entry that restates
// it: the name, plus the tag and digest when present.
func parseImage(ref string) types.Image {
	var img types.Image
	if i := strings.Index(ref, "@"); i >= 0 {
		ref, img.Digest = ref[:i], ref[i+1:]
	}
	// A colon after the last slash starts the tag; one before it is a
	// registry port.
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref, img.NewTag = ref[:i], ref[i+1:]
	}
	img.Name = ref
	return img
}
