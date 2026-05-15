package cli

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestTransformer_ChainSetsNamespace runs a ConfigHubTransformers with one
// set-namespace invocation against a Deployment whose namespace is default,
// and verifies the output is rewritten to "demo".
func TestTransformer_ChainSetsNamespace(t *testing.T) {
	input := `apiVersion: config.kubernetes.io/v1
kind: ResourceList
items:
  - apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: smoke
      namespace: default
    spec:
      replicas: 1
      selector: { matchLabels: { app: smoke } }
      template:
        metadata: { labels: { app: smoke } }
        spec:
          containers:
            - name: app
              image: nginx:1.27
functionConfig:
  apiVersion: installer.confighub.com/v1alpha1
  kind: ConfigHubTransformers
  metadata:
    name: chain
  spec:
    groups:
      - toolchain: Kubernetes/YAML
        invocations:
          - name: set-namespace
            args: ["demo"]
`
	out, err := runTransformer(context.Background(), []byte(input))
	if err != nil {
		t.Fatalf("runTransformer: %v", err)
	}
	var got resourceList
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatalf("parse output: %v\n----\n%s\n----", err, out)
	}
	if got.Kind != "ResourceList" {
		t.Errorf("output kind: want ResourceList, got %q", got.Kind)
	}
	if len(got.Items) != 1 {
		t.Fatalf("output items: want 1, got %d", len(got.Items))
	}
	var item struct {
		Metadata struct {
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
	}
	if err := got.Items[0].Decode(&item); err != nil {
		t.Fatalf("decode item: %v", err)
	}
	if item.Metadata.Namespace != "demo" {
		t.Errorf("namespace: want demo, got %q\n----\n%s\n----", item.Metadata.Namespace, out)
	}
}

// TestTransformer_ValidatorsEmitResults runs a ConfigHubValidators against a
// Deployment that violates vet-merge-keys (two containers named "app"). The
// items should round-trip unchanged; results should carry severity=error.
func TestTransformer_ValidatorsEmitResults(t *testing.T) {
	input := `apiVersion: config.kubernetes.io/v1
kind: ResourceList
items:
  - apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: smoke
      namespace: default
    spec:
      replicas: 1
      selector: { matchLabels: { app: smoke } }
      template:
        metadata: { labels: { app: smoke } }
        spec:
          containers:
            - name: app
              image: nginx:1.27
            - name: app
              image: busybox:1
functionConfig:
  apiVersion: installer.confighub.com/v1alpha1
  kind: ConfigHubValidators
  metadata:
    name: validators
  spec:
    groups:
      - toolchain: Kubernetes/YAML
        invocations:
          - name: vet-merge-keys
`
	out, err := runTransformer(context.Background(), []byte(input))
	if err != nil {
		t.Fatalf("runTransformer: %v", err)
	}
	var got resourceList
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatalf("parse output: %v\n----\n%s\n----", err, out)
	}
	if len(got.Results) == 0 {
		t.Fatalf("expected validator results, got none. Output:\n%s", out)
	}
	sawError := false
	for _, r := range got.Results {
		if r.Severity == "error" && strings.Contains(r.Message, "vet-merge-keys") {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("expected an error result mentioning vet-merge-keys; got:\n%v", got.Results)
	}
}

// TestTransformer_RejectsUnknownKind makes sure we fail fast on functionConfig
// kinds we don't recognize, rather than silently passing items through.
func TestTransformer_RejectsUnknownKind(t *testing.T) {
	input := `apiVersion: config.kubernetes.io/v1
kind: ResourceList
items: []
functionConfig:
  apiVersion: installer.confighub.com/v1alpha1
  kind: NotAThing
  metadata:
    name: oops
  spec:
    groups: []
`
	_, err := runTransformer(context.Background(), []byte(input))
	if err == nil {
		t.Fatal("expected error for unknown functionConfig kind")
	}
	if !strings.Contains(err.Error(), "unsupported functionConfig kind") {
		t.Errorf("error should explain why: %v", err)
	}
}

// TestTransformer_RejectsMissingFunctionConfig — without a functionConfig, the
// transformer has nothing to do; surface that as an error rather than a
// silent no-op so misconfigured kustomizations fail loudly.
func TestTransformer_RejectsMissingFunctionConfig(t *testing.T) {
	input := `apiVersion: config.kubernetes.io/v1
kind: ResourceList
items: []
`
	_, err := runTransformer(context.Background(), []byte(input))
	if err == nil {
		t.Fatal("expected error when functionConfig is missing")
	}
	if !strings.Contains(err.Error(), "no functionConfig") {
		t.Errorf("error should mention missing functionConfig: %v", err)
	}
}
