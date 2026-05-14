package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/pkg/api"
)

func TestApplyImageOverridesNoOpWhenEmpty(t *testing.T) {
	// Empty overrides must not even read the kustomization.yaml —
	// the function should return cleanly even on a package whose
	// base lacks an `images:` block.
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "bases", "default", "kustomization.yaml"),
		"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n")
	loaded := &ipkg.Loaded{
		Root: dir,
		Package: &api.Package{Spec: api.PackageSpec{
			Bases: []api.Base{{Name: "default", Path: "bases/default"}},
		}},
	}
	sel := &api.Selection{Spec: api.SelectionSpec{Base: "default"}}
	if err := applyImageOverrides(loaded, sel, nil); err != nil {
		t.Fatalf("nil overrides should be no-op, got %v", err)
	}
	if err := applyImageOverrides(loaded, sel, map[string]string{}); err != nil {
		t.Fatalf("empty overrides should be no-op, got %v", err)
	}
}

func TestRequireImagesBlockMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kustomization.yaml")
	mustWrite(t, path, "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - x.yaml\n")
	err := requireImagesBlock(path)
	if err == nil {
		t.Fatal("expected error for missing images: block")
	}
	if !strings.Contains(err.Error(), "no `images:` block") {
		t.Errorf("error message should explain the missing field, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Principle 5") {
		t.Errorf("error message should reference principles doc, got: %v", err)
	}
}

func TestRequireImagesBlockMissingFile(t *testing.T) {
	dir := t.TempDir()
	err := requireImagesBlock(filepath.Join(dir, "doesnotexist.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error message should mention missing file, got: %v", err)
	}
}

func TestRequireImagesBlockPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kustomization.yaml")
	mustWrite(t, path, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - x.yaml
images:
  - name: hello
    newTag: v1
`)
	if err := requireImagesBlock(path); err != nil {
		t.Errorf("expected no error for present images block, got: %v", err)
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
