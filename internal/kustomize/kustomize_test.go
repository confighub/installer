// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package kustomize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/kustomize/api/types"
)

func writeKustomization(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kustomization.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readImages(t *testing.T, path string) []types.Image {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var k types.Kustomization
	if err := k.Unmarshal(data); err != nil {
		t.Fatal(err)
	}
	return k.Images
}

// The CLI's default legacy order puts Namespaces before the resources in
// them; the library's own default keeps input order.
func TestBuildUsesLegacyOrder(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"kustomization.yaml": "resources:\n- deploy.yaml\n- ns.yaml\n",
		"deploy.yaml":        "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n",
		"ns.yaml":            "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: demo\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := BuildYAML(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ns, dep := strings.Index(string(out), "kind: Namespace"), strings.Index(string(out), "kind: Deployment"); ns < 0 || dep < 0 || ns > dep {
		t.Errorf("want the Namespace first:\n%s", out)
	}
}

func TestSetImages(t *testing.T) {
	path := writeKustomization(t, `# keep this comment
resources:
- deploy.yaml # and this one
images:
- name: zeta
  newTag: "1"
- name: app
  newName: registry.example.com/app
  newTag: v1
  digest: sha256:old
`)
	if err := SetImages(path, []string{
		"app=*:v2", // keeps newName, sets the tag, drops the digest
		"new=mirror.example.com/new@sha256:abc",
	}); err != nil {
		t.Fatal(err)
	}
	want := []types.Image{
		{Name: "app", NewName: "registry.example.com/app", NewTag: "v2"},
		{Name: "new", NewName: "mirror.example.com/new", Digest: "sha256:abc"},
		{Name: "zeta", NewTag: "1"},
	}
	got := readImages(t, path)
	if len(got) != len(want) {
		t.Fatalf("images = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("images[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	data, _ := os.ReadFile(path)
	for _, s := range []string{"# keep this comment", "# and this one"} {
		if !strings.Contains(string(data), s) {
			t.Errorf("lost %q:\n%s", s, data)
		}
	}
}

func TestSetImagesPreserveWithoutExistingEntry(t *testing.T) {
	path := writeKustomization(t, "images: []\n")
	if err := SetImages(path, []string{"app=*:v2"}); err != nil {
		t.Fatal(err)
	}
	if got := readImages(t, path); len(got) != 1 || got[0] != (types.Image{Name: "app", NewTag: "v2"}) {
		t.Errorf("images = %+v, want app with only newTag v2", got)
	}
}

func TestSetImagesRejectsBareName(t *testing.T) {
	path := writeKustomization(t, "images: []\n")
	if err := SetImages(path, []string{"app"}); err == nil {
		t.Fatal("want an error for an image with no new name, tag or digest")
	}
}

func TestAddResource(t *testing.T) {
	path := writeKustomization(t, "# images: comes later\nnamespace: demo\n")
	for range 2 {
		if err := AddResource(path, "deployment-web.yaml"); err != nil {
			t.Fatal(err)
		}
	}
	if err := AddResource(path, "service-web.yaml"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var k types.Kustomization
	if err := k.Unmarshal(data); err != nil {
		t.Fatal(err)
	}
	if strings.Join(k.Resources, ",") != "deployment-web.yaml,service-web.yaml" {
		t.Errorf("resources = %v", k.Resources)
	}
	if !strings.Contains(string(data), "# images: comes later") || k.Namespace != "demo" {
		t.Errorf("the rest of the file changed:\n%s", data)
	}
}
