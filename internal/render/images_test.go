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
	// Empty overrides must not even look for a kustomization file.
	dir := t.TempDir()
	loaded := &ipkg.Loaded{
		Root: dir,
		Package: &api.Package{Spec: api.PackageSpec{
			Bases: []api.Base{{Name: "default", Path: "bases/default"}},
		}},
	}
	sel := &api.Selection{Spec: api.SelectionSpec{Base: "default"}}
	for _, overrides := range []map[string]string{nil, {}} {
		images, err := applyImageOverrides(loaded, sel, overrides)
		if err != nil || images != nil {
			t.Fatalf("no overrides should be a no-op, got %v, %v", images, err)
		}
	}
}

func TestDeclaredImages(t *testing.T) {
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

	mustWrite(t, path, "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - x.yaml\n")
	declared, err = declaredImages(path)
	if err != nil || len(declared) != 0 {
		t.Errorf("want nothing declared without an images: block, got %v, %v", declared, err)
	}
}

// A name is accepted when the base or a selected component declares it in
// an images: block, or when a container they include runs it. Any other
// name would match no container, and kustomize would ignore it without a
// word, so it is refused, naming the ones that would match.
func TestValidateImageOverrides(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "bases/default/kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
images:
  - name: nginxdemos/hello
    newTag: plain-text
`)
	mustWrite(t, filepath.Join(root, "bases/default/deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: hello
spec:
  template:
    spec:
      initContainers:
        - name: init
          image: busybox:1.36
      containers:
        - name: hello
          image: nginxdemos/hello
`)
	mustWrite(t, filepath.Join(root, "components/db/kustomization.yml"), `apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
resources:
  - statefulset.yaml
`)
	mustWrite(t, filepath.Join(root, "components/db/statefulset.yaml"), `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: db
spec:
  template:
    spec:
      containers:
        - name: postgres
          image: postgres:17.6-alpine@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`)
	pkg := &api.Package{Metadata: api.Metadata{Name: "demo"},
		Spec: api.PackageSpec{
			Bases:      []api.Base{{Name: "default", Path: "bases/default", Default: true}},
			Components: []api.Component{{Name: "db", Path: "components/db"}},
		}}

	for _, tc := range []struct {
		name       string
		components []string
		overrides  map[string]string
		wantErr    []string
	}{
		{name: "none"},
		{name: "declared", overrides: map[string]string{"nginxdemos/hello": "nginxdemos/hello:latest"}},
		{name: "init container", overrides: map[string]string{"busybox": "busybox:1.37"}},
		{name: "component container", components: []string{"db"},
			overrides: map[string]string{"postgres": "mirror.example.com/postgres:17.6-alpine"}},
		{name: "component not selected",
			overrides: map[string]string{"postgres": "mirror.example.com/postgres:17.6-alpine"},
			wantErr:   []string{"names postgres,", `the base "default" uses`, "are busybox, nginxdemos/hello"}},
		{name: "with tag", components: []string{"db"},
			overrides: map[string]string{"postgres:17.6-alpine": "mirror.example.com/postgres:17.6-alpine", "hello": "x", "busybox": "busybox:1.37"},
			wantErr: []string{"names hello, postgres:17.6-alpine,", `the base "default" and components db uses`,
				"are busybox, nginxdemos/hello, postgres"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateImageOverrides(pkg, root, "default", tc.components, tc.overrides)
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("want accepted, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want an error")
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error should contain %q, got: %v", want, err)
				}
			}
		})
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
