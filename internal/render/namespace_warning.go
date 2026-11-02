// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package render

import (
	"fmt"
	"sort"
	"strings"
)

// UnusedNamespaceWarning returns a message when an install namespace was set
// and no namespaced object landed in it. A package only moves its objects when
// its chain runs set-namespace, so without that function the namespace guarantee
// creates an empty Namespace and every object stays where the package put it.
// It returns "" when there is nothing to report.
func (r *Result) UnusedNamespaceWarning(install string) string {
	if install == "" {
		return ""
	}
	counts := map[string]int{}
	for _, files := range [][]File{r.Manifests, r.Secrets} {
		for _, f := range files {
			if f.Namespace == "" {
				continue
			}
			if f.Namespace == install {
				return ""
			}
			counts[f.Namespace]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s (%d)", name, counts[name]))
	}
	return fmt.Sprintf("Warning: no object is in namespace %q, which holds only the Namespace object. This package does not move its objects, and they stay in: %s. Use --namespace %s, or render the source again for the namespace you want.",
		install, strings.Join(parts, ", "), names[0])
}
