// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package render

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func dirContents(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		contents[e.Name()] = string(body)
	}
	return contents
}

// entryNames lists dir's entries, to show that replaceDir leaves no
// temporary directories behind.
func entryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestReplaceDir_RemovesStaleFilesAndKeepsKptfile(t *testing.T) {
	outDir := t.TempDir()
	manifests := filepath.Join(outDir, "manifests")
	writeTestFile(t, filepath.Join(manifests, "configmap-app-env-1111111111.yaml"), "old")
	writeTestFile(t, filepath.Join(manifests, "deployment-app.yaml"), "old deployment")
	writeTestFile(t, filepath.Join(manifests, "Kptfile"), "kpt")

	files := []File{
		{Filename: "configmap-app-env-2222222222.yaml", Body: []byte("new")},
		{Filename: "deployment-app.yaml", Body: []byte("new deployment")},
	}
	if err := replaceDir(manifests, files, 0o755, 0o644, keptInManifests); err != nil {
		t.Fatalf("replaceDir: %v", err)
	}

	want := map[string]string{
		"configmap-app-env-2222222222.yaml": "new",
		"deployment-app.yaml":               "new deployment",
		"Kptfile":                           "kpt",
	}
	got := dirContents(t, manifests)
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for name, body := range want {
		if got[name] != body {
			t.Errorf("%s: want %q, got %q", name, body, got[name])
		}
	}
	if names := entryNames(t, outDir); len(names) != 1 || names[0] != "manifests" {
		t.Errorf("want only manifests/ in out/, got %v", names)
	}
}

func TestReplaceDir_CreatesDirWithPermissions(t *testing.T) {
	secrets := filepath.Join(t.TempDir(), "secrets")
	if err := replaceDir(secrets, []File{{Filename: "secret-key.yaml", Body: []byte("s")}}, 0o700, 0o600, nil); err != nil {
		t.Fatalf("replaceDir: %v", err)
	}
	info, err := os.Stat(secrets)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("want secrets/ mode 0700, got %o", perm)
	}
	info, err = os.Stat(filepath.Join(secrets, "secret-key.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("want secret-key.yaml mode 0600, got %o", perm)
	}
}

func TestReplaceDir_NothingToHoldRemovesDir(t *testing.T) {
	outDir := t.TempDir()
	secrets := filepath.Join(outDir, "secrets")
	writeTestFile(t, filepath.Join(secrets, "secret-old.yaml"), "old")

	if err := replaceDir(secrets, nil, 0o700, 0o600, nil); err != nil {
		t.Fatalf("replaceDir: %v", err)
	}
	if _, err := os.Stat(secrets); !os.IsNotExist(err) {
		t.Errorf("want secrets/ removed when the render produced none, got %v", err)
	}
	// And again, with nothing there to remove.
	if err := replaceDir(secrets, nil, 0o700, 0o600, nil); err != nil {
		t.Fatalf("replaceDir with no previous output: %v", err)
	}
}

func TestReplaceDir_FailedWriteLeavesPreviousOutput(t *testing.T) {
	outDir := t.TempDir()
	manifests := filepath.Join(outDir, "manifests")
	writeTestFile(t, filepath.Join(manifests, "deployment-app.yaml"), "old deployment")

	files := []File{
		{Filename: "deployment-app.yaml", Body: []byte("new deployment")},
		{Filename: "no-such-dir/service-app.yaml", Body: []byte("new service")},
	}
	if err := replaceDir(manifests, files, 0o755, 0o644, keptInManifests); err == nil {
		t.Fatal("want an error writing into a directory that doesn't exist")
	}
	got := dirContents(t, manifests)
	if len(got) != 1 || got["deployment-app.yaml"] != "old deployment" {
		t.Errorf("want the previous output untouched, got %v", got)
	}
	if names := entryNames(t, outDir); len(names) != 1 || names[0] != "manifests" {
		t.Errorf("want only manifests/ in out/, got %v", names)
	}
}
