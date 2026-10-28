// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package selection_test

import (
	"strings"
	"testing"

	"github.com/confighub/installer/internal/selection"
	"github.com/confighub/installer/pkg/api"
)

func mkPkg() *api.Package {
	return &api.Package{
		APIVersion: api.APIVersion,
		Kind:       api.KindPackage,
		Metadata:   api.Metadata{Name: "p"},
		Spec: api.PackageSpec{
			Bases: []api.Base{
				{Name: "raw", Path: "bases/raw", Default: true},
				{Name: "knative", Path: "bases/knative"},
			},
			Components: []api.Component{
				{Name: "monitoring", Path: "components/monitoring"},
				{Name: "router", Path: "components/router"},
				{Name: "router-hpa", Path: "components/router-hpa", Requires: []string{"router"}},
				{Name: "router-tls", Path: "components/router-tls", Requires: []string{"router"}, Conflicts: []string{"router-plain"}},
				{Name: "router-plain", Path: "components/router-plain"},
				{Name: "knative-only", Path: "components/knative-only", ValidForBases: []string{"knative"}},
			},
		},
	}
}

func TestResolveDefaultBase(t *testing.T) {
	sel, err := selection.Resolve(mkPkg(), "", []string{"monitoring"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if sel.Spec.Base != "raw" {
		t.Errorf("Base = %q, want raw (default)", sel.Spec.Base)
	}
}

func TestResolveExplicitBase(t *testing.T) {
	sel, err := selection.Resolve(mkPkg(), "knative", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if sel.Spec.Base != "knative" {
		t.Errorf("Base = %q, want knative", sel.Spec.Base)
	}
}

func TestResolveUnknownBase(t *testing.T) {
	if _, err := selection.Resolve(mkPkg(), "made-up", nil); err == nil {
		t.Fatal("expected error for unknown base")
	}
}

func TestResolveRequiresClosure(t *testing.T) {
	// router-hpa requires router; selecting only router-hpa should pull router.
	sel, err := selection.Resolve(mkPkg(), "", []string{"router-hpa"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := map[string]bool{"router": true, "router-hpa": true}
	for _, c := range sel.Spec.Components {
		if !want[c] {
			t.Errorf("unexpected component %q", c)
		}
		delete(want, c)
	}
	if len(want) > 0 {
		t.Errorf("missing components: %v", want)
	}
}

func TestResolveConflicts(t *testing.T) {
	_, err := selection.Resolve(mkPkg(), "", []string{"router-tls", "router-plain"})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestResolveValidForBases(t *testing.T) {
	if _, err := selection.Resolve(mkPkg(), "raw", []string{"knative-only"}); err == nil {
		t.Fatal("expected validForBases error")
	}
	if _, err := selection.Resolve(mkPkg(), "knative", []string{"knative-only"}); err != nil {
		t.Fatalf("knative-only on knative base failed: %v", err)
	}
}

func TestResolveUnknownComponent(t *testing.T) {
	if _, err := selection.Resolve(mkPkg(), "", []string{"made-up"}); err == nil {
		t.Fatal("expected error for unknown component")
	}
}

func TestResolveDeterministicOrder(t *testing.T) {
	a, _ := selection.Resolve(mkPkg(), "", []string{"router-hpa", "monitoring"})
	b, _ := selection.Resolve(mkPkg(), "", []string{"monitoring", "router-hpa"})
	if strings.Join(a.Spec.Components, ",") != strings.Join(b.Spec.Components, ",") {
		t.Errorf("order should be deterministic regardless of pick order: %v vs %v",
			a.Spec.Components, b.Spec.Components)
	}
}

// mkGroupPkg declares a required group of controllers, argocd its default,
// and an optional group of storage backends with no default.
func mkGroupPkg() *api.Package {
	return &api.Package{
		Metadata: api.Metadata{Name: "p"},
		Spec: api.PackageSpec{
			Bases: []api.Base{{Name: "default", Path: "bases/default", Default: true}},
			Components: []api.Component{
				{Name: "argocd", Path: "components/argocd", Default: true},
				{Name: "flux", Path: "components/flux"},
				{Name: "flux-notifications", Path: "components/flux-notifications", Requires: []string{"flux"}},
				{Name: "s3", Path: "components/s3"},
				{Name: "gcs", Path: "components/gcs"},
				{Name: "monitoring", Path: "components/monitoring"},
			},
			ComponentGroups: []api.ComponentGroup{
				{Name: "controller", Components: []string{"argocd", "flux"}, Required: true},
				{Name: "storage", Components: []string{"s3", "gcs"}},
			},
		},
	}
}

func TestResolveComponentGroups(t *testing.T) {
	cases := []struct {
		name  string
		picks []string
		want  string // the resolved components, or the error's substring
		err   bool
	}{
		{"nothing picked selects the required group's default", nil, "argocd", false},
		{"another pick still gets the default", []string{"monitoring"}, "argocd,monitoring", false},
		{"picking a member replaces the default", []string{"flux"}, "flux", false},
		{"a member reached through requires counts", []string{"flux-notifications"}, "flux,flux-notifications", false},
		{"an optional group may stay empty", []string{"s3"}, "argocd,s3", false},
		{"two members of a group", []string{"argocd", "flux"}, `"argocd" and "flux" are alternatives in group "controller"`, true},
		{"two members of an optional group", []string{"s3", "gcs"}, `group "storage"`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sel, err := selection.Resolve(mkGroupPkg(), "", tc.picks)
			if tc.err {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error = %v, want one containing %q", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got := strings.Join(sel.Spec.Components, ","); got != tc.want {
				t.Errorf("components = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestResolveRequiredGroupWithoutDefault(t *testing.T) {
	pkg := mkGroupPkg()
	pkg.Spec.Components[0].Default = false
	_, err := selection.Resolve(pkg, "", nil)
	if err == nil || !strings.Contains(err.Error(), `group "controller" needs one of components "argocd", "flux"`) {
		t.Fatalf("error = %v, want one naming the group's components", err)
	}
	if _, err := selection.Resolve(pkg, "", []string{"flux"}); err != nil {
		t.Fatalf("an explicit member should satisfy the group: %v", err)
	}
}
