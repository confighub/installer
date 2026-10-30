// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package wizard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/confighub/installer/pkg/api"
)

// A --set-image name the chosen base doesn't declare is refused before the
// wizard writes anything, so the mistake doesn't reach the record.
func TestRunRefusesUndeclaredImage(t *testing.T) {
	pkgDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pkgDir, "bases/default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "bases/default/kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
images:
  - name: nginxdemos/hello
    newTag: plain-text
`), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := &api.Package{Metadata: api.Metadata{Name: "demo"},
		Spec: api.PackageSpec{Bases: []api.Base{{Name: "default", Path: "bases/default", Default: true}}}}
	outDir := filepath.Join(t.TempDir(), "out")

	_, err := Run(context.Background(), pkg, RawAnswers{Namespace: "demo",
		ImageOverrides: map[string]string{"hello": "nginxdemos/hello:latest"}}, pkgDir, outDir)
	if err == nil || !strings.Contains(err.Error(), "names hello,") {
		t.Fatalf("want an error naming the undeclared image hello, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, api.RecordDir)); !os.IsNotExist(err) {
		t.Errorf("nothing should be written to out/record/ when an image name is refused, got %v", err)
	}

	res, err := Run(context.Background(), pkg, RawAnswers{Namespace: "demo",
		ImageOverrides: map[string]string{"nginxdemos/hello": "nginxdemos/hello:latest"}}, pkgDir, outDir)
	if err != nil {
		t.Fatalf("a declared image name should be accepted: %v", err)
	}
	if got := res.Inputs.Spec.ImageOverrides["nginxdemos/hello"]; got != "nginxdemos/hello:latest" {
		t.Errorf("want the override recorded, got %q", got)
	}
}
