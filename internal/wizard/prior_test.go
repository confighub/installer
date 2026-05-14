package wizard

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/confighub/installer/internal/upload"
)

func TestLoadPriorStateNone(t *testing.T) {
	work := t.TempDir()
	state, src, err := LoadPriorState(context.Background(), work, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != nil {
		t.Errorf("state should be nil for empty work-dir, got %+v", state)
	}
	if src != SourceNone {
		t.Errorf("source = %q, want %q", src, SourceNone)
	}
}

func TestLoadPriorStateLocal(t *testing.T) {
	work := t.TempDir()
	specDir := filepath.Join(work, "out", "spec")
	pkgDir := filepath.Join(work, "package")
	mustWrite(t, filepath.Join(specDir, "selection.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Selection
metadata: {name: hello-selection}
spec:
  package: hello
  base: default
  components: [foo]
`)
	mustWrite(t, filepath.Join(specDir, "inputs.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Inputs
metadata: {name: hello-inputs}
spec:
  package: hello
  namespace: demo
  values: {greeting: hi}
`)
	mustWrite(t, filepath.Join(pkgDir, "installer.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata: {name: hello, version: 0.1.0}
spec:
  bases:
    - {name: default, path: bases/default, default: true}
`)

	state, src, err := LoadPriorState(context.Background(), work, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src != SourceLocal {
		t.Errorf("source = %q, want %q", src, SourceLocal)
	}
	if state == nil {
		t.Fatal("state should not be nil")
	}
	if state.Selection == nil || state.Selection.Spec.Base != "default" {
		t.Errorf("Selection mismatch: %+v", state.Selection)
	}
	if state.Inputs == nil || state.Inputs.Spec.Namespace != "demo" {
		t.Errorf("Inputs mismatch: %+v", state.Inputs)
	}
	if state.PriorPackage == nil || state.PriorPackage.Metadata.Name != "hello" {
		t.Errorf("PriorPackage mismatch: %+v", state.PriorPackage)
	}
	if state.Upload != nil {
		t.Errorf("Upload should be nil (no upload.yaml written): %+v", state.Upload)
	}
}

func TestLoadPriorStateUploadYAMLOnly(t *testing.T) {
	// upload.yaml present but cub fetch will fail (no cub on PATH in
	// `go test` env, or the recorded Space doesn't exist). The loader
	// must fall back to local spec without erroring.
	work := t.TempDir()
	specDir := filepath.Join(work, "out", "spec")
	mustWrite(t, filepath.Join(specDir, "selection.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Selection
metadata: {name: x}
spec: {package: hello, base: default}
`)
	mustWrite(t, filepath.Join(specDir, upload.UploadDocFilename), `apiVersion: installer.confighub.com/v1alpha1
kind: Upload
metadata: {name: hello-upload}
spec:
  package: hello
  packageVersion: 0.1.0
  spaces:
    - {package: hello, version: 0.1.0, slug: nonexistent-test-space, isParent: true}
`)
	warned := 0
	state, src, err := LoadPriorState(context.Background(), work, func(string) { warned++ })
	if err != nil {
		t.Fatalf("loader should fall back, got %v", err)
	}
	if src != SourceLocal {
		t.Errorf("expected fallback to local source, got %q", src)
	}
	if state == nil || state.Selection == nil {
		t.Fatal("expected local state to be loaded")
	}
	if warned == 0 {
		t.Errorf("expected at least one warn callback for the fetch failure")
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
