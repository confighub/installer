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

const validPackageWithDeps = `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata:
  name: stack
  version: 0.3.0
  kubeVersion: ">= 1.28"
  installerVersion: ">= 0.2.0"
spec:
  bases:
    - {name: default, path: bases/default, default: true}
  components:
    - {name: ingress, path: components/ingress}
  dependencies:
    - name: gateway-api
      package: oci://ghcr.io/confighubai/gateway-api
      version: "^1.2.0"
      selection:
        base: default
        components: [crds, kgateway]
      inputs:
        namespace: gateway-system
      whenComponent: ingress
      satisfies:
        - {kind: CRD, name: gateway.networking.k8s.io}
        - {kind: GatewayClass, capability: ext-proc}
    - name: cert-manager
      package: oci://ghcr.io/confighubai/cert-manager
      version: ">= 1.15.0"
      optional: true
  conflicts:
    - {package: oci://ghcr.io/foo/old-gateway, version: "*"}
  replaces:
    - {package: oci://ghcr.io/foo/old-name, version: "< 2.0.0"}
  bundleExamples: false
  functionChainTemplate:
    - toolchain: Kubernetes/YAML
      whereResource: ""
      invocations:
        - name: set-namespace
          args: ["test"]
`

func TestParsePackageWithDeps(t *testing.T) {
	p, err := api.ParsePackage([]byte(validPackageWithDeps))
	if err != nil {
		t.Fatalf("ParsePackage: %v", err)
	}
	if p.Metadata.KubeVersion != ">= 1.28" {
		t.Errorf("kubeVersion = %q", p.Metadata.KubeVersion)
	}
	if p.Metadata.InstallerVersion != ">= 0.2.0" {
		t.Errorf("installerVersion = %q", p.Metadata.InstallerVersion)
	}
	if len(p.Spec.Dependencies) != 2 {
		t.Fatalf("dependencies = %d, want 2", len(p.Spec.Dependencies))
	}
	d := p.Spec.Dependencies[0]
	if d.Name != "gateway-api" || d.Version != "^1.2.0" {
		t.Errorf("dep[0] mismatch: %+v", d)
	}
	if d.Selection == nil || d.Selection.Base != "default" || len(d.Selection.Components) != 2 {
		t.Errorf("dep[0].selection mismatch: %+v", d.Selection)
	}
	if d.Inputs["namespace"] != "gateway-system" {
		t.Errorf("dep[0].inputs mismatch: %+v", d.Inputs)
	}
	if d.WhenComponent != "ingress" {
		t.Errorf("dep[0].whenComponent = %q", d.WhenComponent)
	}
	if len(d.Satisfies) != 2 {
		t.Errorf("dep[0].satisfies count = %d", len(d.Satisfies))
	}
	if !p.Spec.Dependencies[1].Optional {
		t.Errorf("dep[1].optional should be true")
	}
	if len(p.Spec.Conflicts) != 1 || p.Spec.Conflicts[0].Package == "" {
		t.Errorf("conflicts mismatch: %+v", p.Spec.Conflicts)
	}
	if len(p.Spec.Replaces) != 1 || p.Spec.Replaces[0].Version != "< 2.0.0" {
		t.Errorf("replaces mismatch: %+v", p.Spec.Replaces)
	}
	if p.Spec.BundleExamples == nil || *p.Spec.BundleExamples != false {
		t.Errorf("bundleExamples should be false")
	}
}

func TestParsePackageRejectsDuplicateDepName(t *testing.T) {
	bad := strings.Replace(validPackageWithDeps, "    - name: cert-manager", "    - name: gateway-api", 1)
	_, err := api.ParsePackage([]byte(bad))
	if err == nil || !strings.Contains(err.Error(), "duplicate dependency name") {
		t.Fatalf("expected duplicate-name error, got %v", err)
	}
}

func TestParsePackageRejectsDepMissingFields(t *testing.T) {
	missingPackage := `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata: {name: t}
spec:
  bases: [{name: default, path: bases/default, default: true}]
  dependencies:
    - name: foo
`
	_, err := api.ParsePackage([]byte(missingPackage))
	if err == nil || !strings.Contains(err.Error(), "package is required") {
		t.Fatalf("expected package-required error, got %v", err)
	}
}

func TestParsePackageRejectsBadWhenComponent(t *testing.T) {
	bad := strings.Replace(validPackageWithDeps, "whenComponent: ingress", "whenComponent: nope", 1)
	_, err := api.ParsePackage([]byte(bad))
	if err == nil || !strings.Contains(err.Error(), "whenComponent") {
		t.Fatalf("expected whenComponent error, got %v", err)
	}
}

func TestParseLock(t *testing.T) {
	lockYAML := `apiVersion: installer.confighub.com/v1alpha1
kind: Lock
metadata:
  name: stack
spec:
  package:
    name: stack
    version: 0.3.0
  resolved:
    - name: gateway-api
      ref: oci://ghcr.io/confighubai/gateway-api:1.4.2
      digest: sha256:bbbbbbbb
      version: 1.4.2
      requestedBy: [root]
      selection:
        base: default
        components: [crds]
`
	l, err := api.ParseLock([]byte(lockYAML))
	if err != nil {
		t.Fatalf("ParseLock: %v", err)
	}
	if l.Spec.Package.Name != "stack" {
		t.Errorf("package.name = %q", l.Spec.Package.Name)
	}
	if len(l.Spec.Resolved) != 1 || l.Spec.Resolved[0].Digest != "sha256:bbbbbbbb" {
		t.Errorf("resolved mismatch: %+v", l.Spec.Resolved)
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
