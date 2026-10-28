// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Package selection resolves a user's raw component picks against the package
// manifest: closes the set under Requires, selects the default member of each
// required ComponentGroup left empty, rejects Conflicts and two members of one
// group, validates against the chosen Base via ValidForBases, and picks a Base
// when the user did not specify one. Output is a Selection ready to drive
// render.
package selection

import (
	"fmt"
	"sort"
	"strings"

	"github.com/confighub/installer/pkg/api"
)

// Resolve takes the package manifest, the user's chosen base name (may be ""),
// and the user's raw selected component names. Returns a Selection whose
// Components is the closure under Requires, with Conflicts and ValidForBases
// validated.
//
// If baseName is "", the package's default base is chosen; if no default is
// declared and there is exactly one base, that one is used; otherwise an error
// is returned.
func Resolve(pkg *api.Package, baseName string, picks []string) (*api.Selection, error) {
	base, err := chooseBase(pkg, baseName)
	if err != nil {
		return nil, err
	}

	byName := indexComponents(pkg)

	selected := map[string]struct{}{}
	if err := closeUnderRequires(pkg, byName, selected, picks); err != nil {
		return nil, err
	}
	// A required group nothing selected a member of gets its default
	// member, and what that requires. Each pass adds a member to a group
	// that had none, so this ends.
	for {
		added, err := selectGroupDefaults(pkg, byName, selected)
		if err != nil {
			return nil, err
		}
		if len(added) == 0 {
			break
		}
		if err := closeUnderRequires(pkg, byName, selected, added); err != nil {
			return nil, err
		}
	}
	for _, g := range pkg.Spec.ComponentGroups {
		var members []string
		for _, m := range g.Components {
			if _, ok := selected[m]; ok {
				members = append(members, m)
			}
		}
		if len(members) > 1 {
			return nil, fmt.Errorf("components %s are alternatives in group %q; select one",
				strings.Join(quoteAll(members), " and "), g.Name)
		}
	}

	// ValidForBases check.
	for name := range selected {
		c := byName[name]
		if len(c.ValidForBases) == 0 {
			continue
		}
		ok := false
		for _, b := range c.ValidForBases {
			if b == base {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("component %q is not valid for base %q (valid for: %v)",
				name, base, c.ValidForBases)
		}
	}

	// Conflicts check.
	for name := range selected {
		c := byName[name]
		for _, other := range c.Conflicts {
			if _, both := selected[other]; both {
				return nil, fmt.Errorf("components %q and %q conflict", name, other)
			}
		}
	}

	// Deterministic order.
	out := make([]string, 0, len(selected))
	for name := range selected {
		out = append(out, name)
	}
	sort.Strings(out)

	return &api.Selection{
		APIVersion: api.APIVersion,
		Kind:       api.KindSelection,
		Metadata: api.Metadata{
			Name: pkg.Metadata.Name + "-selection",
		},
		Spec: api.SelectionSpec{
			Package:        pkg.Metadata.Name,
			PackageVersion: pkg.InstallerMetadata.Version,
			Base:           base,
			Components:     out,
		},
	}, nil
}

// closeUnderRequires adds names, and everything they require, to selected.
func closeUnderRequires(pkg *api.Package, byName map[string]*api.Component, selected map[string]struct{}, names []string) error {
	queue := append([]string(nil), names...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if _, ok := selected[name]; ok {
			continue
		}
		c, ok := byName[name]
		if !ok {
			return fmt.Errorf("unknown component %q (declared in %s)", name, pkg.Metadata.Name)
		}
		selected[name] = struct{}{}
		queue = append(queue, c.Requires...)
	}
	return nil
}

// selectGroupDefaults selects the default member of each required group
// that has no selected member, and returns the members it selected. A
// required group with no default member is an error naming the choices.
func selectGroupDefaults(pkg *api.Package, byName map[string]*api.Component, selected map[string]struct{}) ([]string, error) {
	var added []string
	for _, g := range pkg.Spec.ComponentGroups {
		if !g.Required {
			continue
		}
		def := ""
		have := false
		for _, m := range g.Components {
			if _, ok := selected[m]; ok {
				have = true
				break
			}
			if c := byName[m]; c != nil && c.Default {
				def = m
			}
		}
		if have {
			continue
		}
		if def == "" {
			return nil, fmt.Errorf("group %q needs one of components %s; select one",
				g.Name, strings.Join(quoteAll(g.Components), ", "))
		}
		selected[def] = struct{}{}
		added = append(added, def)
	}
	return added, nil
}

func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return out
}

func chooseBase(pkg *api.Package, requested string) (string, error) {
	if requested != "" {
		for _, b := range pkg.Spec.Bases {
			if b.Name == requested {
				return requested, nil
			}
		}
		return "", fmt.Errorf("unknown base %q", requested)
	}
	for _, b := range pkg.Spec.Bases {
		if b.Default {
			return b.Name, nil
		}
	}
	if len(pkg.Spec.Bases) == 1 {
		return pkg.Spec.Bases[0].Name, nil
	}
	names := make([]string, 0, len(pkg.Spec.Bases))
	for _, b := range pkg.Spec.Bases {
		names = append(names, b.Name)
	}
	return "", fmt.Errorf("package has %d bases (%v) and no default; specify --base", len(names), names)
}

func indexComponents(pkg *api.Package) map[string]*api.Component {
	out := make(map[string]*api.Component, len(pkg.Spec.Components))
	for i := range pkg.Spec.Components {
		c := &pkg.Spec.Components[i]
		out[c.Name] = c
	}
	return out
}
