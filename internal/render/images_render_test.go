// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package render_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/render"
	"github.com/confighub/installer/internal/selection"
)

// --set-image changes an image wherever the render uses it: in the base,
// whose images: block renames its image, and in a component that declares
// no images: block, which the base's block never reaches.
func TestRenderSetsImagesInBaseAndComponents(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "installer.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata: {name: images}
installerMetadata: {version: 0.1.0}
spec:
  bases:
    - {name: default, path: bases/default, default: true}
  components:
    - {name: db, path: components/db}
`)
	writeFile(t, filepath.Join(root, "bases/default/kustomization.yaml"), `resources:
  - deployment.yaml
images:
  - name: app
    newName: ghcr.io/example/app
    newTag: v1
`)
	writeFile(t, filepath.Join(root, "bases/default/deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
        - name: app
          image: app
`)
	writeFile(t, filepath.Join(root, "components/db/kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
resources:
  - statefulset.yaml
`)
	writeFile(t, filepath.Join(root, "components/db/statefulset.yaml"), `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: db
spec:
  template:
    spec:
      initContainers:
        - name: init
          image: app:v0
      containers:
        - name: postgres
          image: postgres:17.6-alpine
`)
	loaded, err := ipkg.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sel, err := selection.Resolve(loaded.Package, "", []string{"db"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	inputs := mixinInputs("demo")
	inputs.Spec.ImageOverrides = map[string]string{
		"app":      "mirror.example.com/app:v2",
		"postgres": "mirror.example.com/postgres",
	}

	res, err := render.Render(context.Background(), render.Options{
		Loaded:            loaded,
		Selection:         sel,
		Inputs:            inputs,
		TransformerBinary: installerBin,
	}, filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, tc := range []struct{ kind, name, want string }{
		{"Deployment", "app", "image: mirror.example.com/app:v2"},
		{"StatefulSet", "db", "image: mirror.example.com/app:v2"},
		// No tag in the override keeps the one the container has.
		{"StatefulSet", "db", "image: mirror.example.com/postgres:17.6-alpine"},
	} {
		body := readManifest(t, res, tc.kind, "", tc.name)
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s %s should contain %q:\n%s", tc.kind, tc.name, tc.want, body)
		}
	}
}
