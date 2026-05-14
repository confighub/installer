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
