// Package selection resolves a user's raw component picks against the package
// manifest: closes the set under Requires, rejects Conflicts, validates
// against the chosen Base via ValidForBases, and picks a Base when the user
// did not specify one. Output is a Selection ready to drive render.
package selection

import (
	"fmt"
	"sort"

	"github.com/confighubai/installer/pkg/api"
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

	// Closure under Requires (BFS from picks).
	selected := map[string]struct{}{}
	queue := append([]string(nil), picks...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if _, ok := selected[name]; ok {
			continue
		}
		c, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown component %q (declared in %s)", name, pkg.Metadata.Name)
		}
		selected[name] = struct{}{}
		queue = append(queue, c.Requires...)
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
			PackageVersion: pkg.Metadata.Version,
			Base:           base,
			Components:     out,
		},
	}, nil
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
