// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package render

import (
	"strings"
	"testing"
)

func TestUnusedNamespaceWarning(t *testing.T) {
	cases := []struct {
		name    string
		install string
		result  Result
		want    string
	}{
		{name: "no install namespace", install: "", result: Result{Manifests: []File{{Namespace: "redis"}}}},
		{name: "objects in the install namespace", install: "redis", result: Result{Manifests: []File{{Namespace: "redis"}}}},
		{name: "one object in the install namespace is enough", install: "app", result: Result{Manifests: []File{{Namespace: "app"}, {Namespace: "kube-system"}}}},
		{name: "cluster-scoped objects only", install: "other", result: Result{Manifests: []File{{Kind: "ClusterRole"}}}},
		{name: "objects stay elsewhere", install: "other", result: Result{Manifests: []File{{Kind: "Namespace"}, {Namespace: "redis"}, {Namespace: "redis"}}}, want: "redis (2)"},
		{name: "secrets count", install: "other", result: Result{Secrets: []File{{Namespace: "redis"}}}, want: "redis (1)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.result.UnusedNamespaceWarning(tc.install)
			if tc.want == "" && got != "" {
				t.Fatalf("want no warning, got %q", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("want a warning naming %q, got %q", tc.want, got)
			}
		})
	}
}
