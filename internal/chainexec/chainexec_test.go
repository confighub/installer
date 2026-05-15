package chainexec

import (
	"context"
	"strings"
	"testing"

	"github.com/confighub/installer/pkg/api"
)

// passingResource is a minimal valid Deployment manifest. Used as the
// happy path against the default validator chain (vet-schemas,
// vet-merge-keys, vet-format).
const passingResource = `apiVersion: apps/v1
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
`

// failingResource has two containers with the same name — a
// vet-merge-keys violation. vet-schemas would also flag the bool
// annotation value.
const failingResource = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: smoke
  namespace: default
  annotations:
    foo: true   # bool annotation — vet-schemas
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
`

func TestRunValidators_NoValidatorsIsNoOp(t *testing.T) {
	failures, err := RunValidators(context.Background(), nil, []byte(passingResource))
	if err != nil {
		t.Fatalf("nil groups should be a no-op, got %v", err)
	}
	if len(failures) != 0 {
		t.Errorf("expected no failures, got %v", failures)
	}
}

func TestRunValidators_PassingResource(t *testing.T) {
	groups := []api.FunctionGroup{{
		Toolchain: "Kubernetes/YAML",
		Invocations: []api.FunctionInvocation{
			{Name: "vet-schemas"},
			{Name: "vet-merge-keys"},
			{Name: "vet-format"},
		},
	}}
	failures, err := RunValidators(context.Background(), groups, []byte(passingResource))
	if err != nil {
		t.Fatalf("RunValidators: %v", err)
	}
	if len(failures) != 0 {
		t.Errorf("expected no failures, got: %s", FormatValidatorFailures(failures))
	}
}

func TestRunValidators_FailingResource(t *testing.T) {
	groups := []api.FunctionGroup{{
		Toolchain: "Kubernetes/YAML",
		Invocations: []api.FunctionInvocation{
			{Name: "vet-schemas"},
			{Name: "vet-merge-keys"},
		},
	}}
	failures, err := RunValidators(context.Background(), groups, []byte(failingResource))
	if err != nil {
		t.Fatalf("RunValidators: %v", err)
	}
	if len(failures) == 0 {
		t.Fatal("expected failures, got none")
	}
	out := FormatValidatorFailures(failures)
	for _, want := range []string{"vet-schemas", "vet-merge-keys"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected failure mention of %q in:\n%s", want, out)
		}
	}
}

func TestRunValidators_RejectsMutatingFunctions(t *testing.T) {
	// set-namespace is a mutating function; declaring it under
	// validators is a programming error in the package author's
	// installer.yaml. RunValidators must reject before invoking.
	groups := []api.FunctionGroup{{
		Toolchain: "Kubernetes/YAML",
		Invocations: []api.FunctionInvocation{
			{Name: "set-namespace", Args: []string{"foo"}},
		},
	}}
	_, err := RunValidators(context.Background(), groups, []byte(passingResource))
	if err == nil {
		t.Fatal("expected error for mutating function in validators list")
	}
	if !strings.Contains(err.Error(), "not a validating function") {
		t.Errorf("error should explain why: %v", err)
	}
}

func TestRunValidators_UnknownFunction(t *testing.T) {
	groups := []api.FunctionGroup{{
		Toolchain: "Kubernetes/YAML",
		Invocations: []api.FunctionInvocation{
			{Name: "vet-bogus-does-not-exist"},
		},
	}}
	_, err := RunValidators(context.Background(), groups, []byte(passingResource))
	if err == nil {
		t.Fatal("expected error for unknown function")
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Errorf("error should explain unregistered: %v", err)
	}
}
