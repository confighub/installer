// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

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

func TestDeclaredImagesMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kustomization.yaml")
	mustWrite(t, path, "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - x.yaml\n")
	_, err := declaredImages(path)
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

func TestDeclaredImagesMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := declaredImages(filepath.Join(dir, "doesnotexist.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error message should mention missing file, got: %v", err)
	}
}

func TestDeclaredImagesPresent(t *testing.T) {
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
	declared, err := declaredImages(path)
	if err != nil {
		t.Errorf("expected no error for present images block, got: %v", err)
	}
	if !declared["hello"] || len(declared) != 1 {
		t.Errorf("want the one declared image hello, got %v", declared)
	}
}

// A name the base doesn't declare would match no container, and kustomize
// would ignore it without a word, so it is refused, naming the ones declared.
func TestValidateImageOverrides(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "bases/default/kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
images:
  - name: nginxdemos/hello
    newTag: plain-text
  - name: busybox
`)
	pkg := &api.Package{Metadata: api.Metadata{Name: "demo"},
		Spec: api.PackageSpec{Bases: []api.Base{{Name: "default", Path: "bases/default", Default: true}}}}

	if err := ValidateImageOverrides(pkg, root, "default", map[string]string{"nginxdemos/hello": "nginxdemos/hello:latest"}); err != nil {
		t.Errorf("a declared name should be accepted, got: %v", err)
	}
	if err := ValidateImageOverrides(pkg, root, "default", nil); err != nil {
		t.Errorf("no overrides should be accepted, got: %v", err)
	}
	err := ValidateImageOverrides(pkg, root, "default", map[string]string{"hello": "nginxdemos/hello:latest", "nginxdemos/hello": "x"})
	if err == nil {
		t.Fatal("want an error for a name the base doesn't declare")
	}
	for _, want := range []string{"names hello,", "declares busybox, nginxdemos/hello"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q, got: %v", want, err)
		}
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
