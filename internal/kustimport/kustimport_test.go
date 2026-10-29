// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package kustimport

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/kustomize/api/types"

	"github.com/confighub/installer/internal/kustomize"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func deployment(name, image string) string {
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: ` + name + `
spec:
  selector:
    matchLabels: {app: ` + name + `}
  template:
    metadata:
      labels: {app: ` + name + `}
    spec:
      initContainers:
      - name: init
        image: busybox
      containers:
      - name: app
        image: ` + image + `
`
}

// requireSameBuild checks the imported base against the source independently
// of the comparison Import itself makes.
func requireSameBuild(t *testing.T, source, dest string) {
	t.Helper()
	want, err := kustomize.Build(source)
	if err != nil {
		t.Fatal(err)
	}
	got, err := kustomize.Build(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := sameOutput(want, got); err != nil {
		t.Fatal(err)
	}
}

func TestImportSelfContainedSeedsImages(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	writeFiles(t, src, map[string]string{
		"kustomization.yml": "resources:\n- deploy.yaml\n",
		"deploy.yaml":       deployment("web", "nginx:1.27"),
	})
	dest := filepath.Join(tmp, "pkg", "bases", "default")

	res, err := Import(src, "", dest)
	if err != nil {
		t.Fatal(err)
	}
	if res.Target != "." {
		t.Errorf("Target = %q, want .", res.Target)
	}
	want := []types.Image{{Name: "busybox"}, {Name: "nginx", NewTag: "1.27"}}
	if !reflect.DeepEqual(res.Images, want) {
		t.Errorf("Images = %+v, want %+v", res.Images, want)
	}
	if _, err := os.Stat(filepath.Join(dest, "kustomization.yml")); !os.IsNotExist(err) {
		t.Errorf("kustomization.yml was not renamed to kustomization.yaml")
	}
	kust := readFile(t, filepath.Join(dest, "kustomization.yaml"))
	for _, s := range []string{"- deploy.yaml", "name: nginx", `newTag: "1.27"`, "name: busybox"} {
		if !strings.Contains(kust, s) {
			t.Errorf("kustomization.yaml lacks %q:\n%s", s, kust)
		}
	}
	requireSameBuild(t, src, dest)
	if entries, _ := os.ReadDir(filepath.Dir(dest)); len(entries) != 1 {
		t.Errorf("expected only default/ in %s, found %d entries", filepath.Dir(dest), len(entries))
	}
}

func TestImportKeepsDeclaredImages(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	writeFiles(t, src, map[string]string{
		"kustomization.yaml": "resources:\n- deploy.yaml\nimages:\n- name: nginx\n  newName: mirror/nginx\n  newTag: \"1.28\"\n",
		"deploy.yaml":        deployment("web", "nginx:1.27"),
	})
	dest := filepath.Join(tmp, "dest")

	res, err := Import(src, "", dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 0 {
		t.Errorf("Images = %+v, want none", res.Images)
	}
	kust := readFile(t, filepath.Join(dest, "kustomization.yaml"))
	if strings.Contains(kust, "busybox") || !strings.Contains(kust, "newName: mirror/nginx") {
		t.Errorf("images: block changed:\n%s", kust)
	}
}

func overlayTree() map[string]string {
	return map[string]string{
		"app/base/kustomization.yaml":          "resources:\n- deploy.yaml\n",
		"app/base/deploy.yaml":                 deployment("web", "nginx:1.27"),
		"app/overlays/prod/kustomization.yaml": "namespace: prod\nresources:\n- ../../base\npatches:\n- path: replicas.yaml\n",
		"app/overlays/prod/replicas.yaml":      "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\nspec:\n  replicas: 3\n",
		"other/unreferenced.yaml":              "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: other\n",
	}
}

func TestImportOverlayInGitRepository(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	writeFiles(t, repo, overlayTree())
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(repo, "app", "overlays", "prod")
	dest := filepath.Join(tmp, "dest")

	res, err := Import(src, "", dest)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(repo); res.Scope != want {
		t.Errorf("Scope = %q, want %q", res.Scope, want)
	}
	if res.Target != filepath.Join("overlays", "prod") {
		t.Errorf("Target = %q, want overlays/prod", res.Target)
	}
	for _, f := range []string{"base/deploy.yaml", "overlays/prod/replicas.yaml"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "other")); !os.IsNotExist(err) {
		t.Errorf("unreferenced files were copied")
	}
	kust := readFile(t, filepath.Join(dest, "kustomization.yaml"))
	for _, s := range []string{"kind: Kustomization", "- overlays/prod", "name: nginx"} {
		if !strings.Contains(kust, s) {
			t.Errorf("kustomization.yaml lacks %q:\n%s", s, kust)
		}
	}
	requireSameBuild(t, src, dest)
}

func TestImportOverlayOutsideDefaultScope(t *testing.T) {
	tmp := t.TempDir()
	writeFiles(t, tmp, overlayTree())
	dest := filepath.Join(tmp, "dest")

	_, err := Import(filepath.Join(tmp, "app", "overlays", "prod"), "", dest)
	if err == nil || !strings.Contains(err.Error(), "--source-scope") {
		t.Fatalf("err = %v, want a --source-scope hint", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest was created by a failed import")
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 2 {
		t.Errorf("the temporary directory was left behind: %v", entries)
	}
}

func TestImportOverlayWithExplicitScope(t *testing.T) {
	tmp := t.TempDir()
	writeFiles(t, tmp, overlayTree())
	src := filepath.Join(tmp, "app", "overlays", "prod")
	dest := filepath.Join(tmp, "dest")

	res, err := Import(src, filepath.Join(tmp, "app"), dest)
	if err != nil {
		t.Fatal(err)
	}
	if res.Target != filepath.Join("overlays", "prod") {
		t.Errorf("Target = %q, want overlays/prod", res.Target)
	}
	requireSameBuild(t, src, dest)
}

func TestImportConflictingTagsGetBareEntry(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	writeFiles(t, src, map[string]string{
		"kustomization.yaml": "resources:\n- a.yaml\n- b.yaml\n",
		"a.yaml":             deployment("a", "nginx:1.25"),
		"b.yaml":             deployment("b", "nginx:1.27"),
	})
	res, err := Import(src, "", filepath.Join(tmp, "dest"))
	if err != nil {
		t.Fatal(err)
	}
	want := []types.Image{{Name: "busybox"}, {Name: "nginx"}}
	if !reflect.DeepEqual(res.Images, want) {
		t.Errorf("Images = %+v, want %+v", res.Images, want)
	}
}

func TestImportRejectsHelm(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	writeFiles(t, src, map[string]string{
		"kustomization.yaml": "helmCharts:\n- name: podinfo\n  repo: https://stefanprodan.github.io/podinfo\n  version: 6.7.0\n  releaseName: p\n",
	})
	_, err := Import(src, "", filepath.Join(tmp, "dest"))
	if err == nil || !strings.Contains(err.Error(), "Helm") {
		t.Fatalf("err = %v, want a Helm refusal", err)
	}
}

func TestImportRejectsSecretData(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	writeFiles(t, src, map[string]string{
		"kustomization.yaml": "secretGenerator:\n- name: creds\n  envs:\n  - creds.env\n",
		"creds.env":          "PASSWORD=hunter2\n",
	})
	_, err := Import(src, "", filepath.Join(tmp, "dest"))
	if err == nil || !strings.Contains(err.Error(), "Secrets with data") {
		t.Fatalf("err = %v, want a Secret refusal", err)
	}
}

func TestImportRefusesNonEmptyDest(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	writeFiles(t, src, map[string]string{
		"kustomization.yaml": "resources:\n- deploy.yaml\n",
		"deploy.yaml":        deployment("web", "nginx:1.27"),
	})
	dest := filepath.Join(tmp, "dest")
	writeFiles(t, dest, map[string]string{"keep.yaml": "x: 1\n"})

	if _, err := Import(src, "", dest); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err = %v, want a non-empty refusal", err)
	}
	if got := readFile(t, filepath.Join(dest, "keep.yaml")); got != "x: 1\n" {
		t.Errorf("dest was modified: %q", got)
	}
}
