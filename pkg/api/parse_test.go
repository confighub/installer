package api_test

import (
	"strings"
	"testing"

	"github.com/confighubai/installer/pkg/api"
)

const validPackage = `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata:
  name: test
  version: 1.0.0
spec:
  bases:
    - name: default
      path: bases/default
      default: true
  components:
    - name: foo
      path: components/foo
      requires: [bar]
    - name: bar
      path: components/bar
  inputs:
    - name: namespace
      type: string
      default: test
  functionChainTemplate:
    - toolchain: Kubernetes/YAML
      whereResource: ""
      invocations:
        - name: set-namespace
          args: ["test"]
`

func TestParsePackage(t *testing.T) {
	p, err := api.ParsePackage([]byte(validPackage))
	if err != nil {
		t.Fatalf("ParsePackage: %v", err)
	}
	if p.Metadata.Name != "test" {
		t.Errorf("name = %q, want test", p.Metadata.Name)
	}
	if len(p.Spec.Bases) != 1 || p.Spec.Bases[0].Name != "default" {
		t.Errorf("bases mismatch: %+v", p.Spec.Bases)
	}
	if len(p.Spec.Components) != 2 {
		t.Errorf("components = %d, want 2", len(p.Spec.Components))
	}
	if len(p.Spec.FunctionChainTemplate) != 1 ||
		p.Spec.FunctionChainTemplate[0].Toolchain != "Kubernetes/YAML" {
		t.Errorf("function chain mismatch: %+v", p.Spec.FunctionChainTemplate)
	}
}

func TestParsePackageRejectsBadKind(t *testing.T) {
	bad := strings.Replace(validPackage, "kind: Package", "kind: NotAPackage", 1)
	if _, err := api.ParsePackage([]byte(bad)); err == nil {
		t.Fatal("expected error for wrong kind")
	}
}

func TestParsePackageRejectsMissingBases(t *testing.T) {
	bad := `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata: {name: x}
spec: {}
`
	if _, err := api.ParsePackage([]byte(bad)); err == nil {
		t.Fatal("expected error for missing bases")
	}
}

func TestParsePackageRejectsMultipleDefaults(t *testing.T) {
	bad := strings.Replace(validPackage,
		"  components:",
		`    - name: alt
      path: bases/alt
      default: true
  components:`, 1)
	if _, err := api.ParsePackage([]byte(bad)); err == nil {
		t.Fatal("expected error for multiple defaults")
	}
}

func TestSplitMultiDoc(t *testing.T) {
	stream := []byte(`apiVersion: v1
kind: Namespace
metadata:
  name: foo
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: bar
`)
	docs, err := api.SplitMultiDoc(stream)
	if err != nil {
		t.Fatalf("SplitMultiDoc: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(docs))
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	sel := &api.Selection{
		APIVersion: api.APIVersion,
		Kind:       api.KindSelection,
		Metadata:   api.Metadata{Name: "round-trip"},
		Spec: api.SelectionSpec{
			Package:    "test",
			Base:       "default",
			Components: []string{"a", "b"},
		},
	}
	data, err := api.MarshalYAML(sel)
	if err != nil {
		t.Fatalf("MarshalYAML: %v", err)
	}
	got, err := api.ParseSelection(data)
	if err != nil {
		t.Fatalf("ParseSelection: %v", err)
	}
	if got.Spec.Base != "default" || len(got.Spec.Components) != 2 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}
