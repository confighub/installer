// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package pkg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/confighub/installer/pkg/api"
)

func TestCleanRepoURL(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:o/r.git":             "https://github.com/o/r",
		"ssh://git@github.com:22/o/r.git":    "https://github.com/o/r",
		"https://token@github.com/o/r.git":   "https://github.com/o/r",
		"https://u:p@github.com/o/r?x=1#top": "https://github.com/o/r",
		"git://github.com/o/r":               "https://github.com/o/r",
		"file:///tmp/r":                      "",
		"/tmp/r":                             "",
		`C:\src\r`:                           "",
		"../r":                               "",
	} {
		if got := CleanRepoURL(in); got != want {
			t.Errorf("CleanRepoURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectGitSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		args = append([]string{"-C", root, "-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	dir := filepath.Join(root, "pkgs", "app")
	run("init", "-q")
	run("remote", "add", "origin", "git@github.com:o/r.git")
	write("pkgs/app/installer.yaml", "a: 1\n")

	if got := detectGitSource(ctx, dir); got != nil {
		t.Errorf("untracked package: got %+v, want nil", got)
	}

	run("add", ".")
	run("commit", "-q", "-m", "add package")
	got := detectGitSource(ctx, dir)
	if got == nil {
		t.Fatal("committed package: got nil")
	}
	if got.Repository != "https://github.com/o/r" || got.Path != "pkgs/app" || got.Revision == "" || got.Dirty {
		t.Errorf("committed package: got %+v", got)
	}

	write("pkgs/app/installer.yaml", "a: 2\n")
	if got := detectGitSource(ctx, dir); got == nil || !got.Dirty {
		t.Errorf("changed package: got %+v, want dirty", got)
	}
}

func TestGitSourceFromAnnotations(t *testing.T) {
	if got := gitSourceFromAnnotations(nil); got != nil {
		t.Errorf("no annotations: got %+v, want nil", got)
	}
	got := gitSourceFromAnnotations(map[string]string{
		"org.opencontainers.image.source":   "https://github.com/o/r",
		"org.opencontainers.image.revision": "abc",
		api.AnnotationGitPath:               "pkgs/app",
		api.AnnotationGitDirty:              "true",
	})
	want := api.GitSource{Repository: "https://github.com/o/r", Revision: "abc", Path: "pkgs/app", Dirty: true}
	if got == nil || *got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
