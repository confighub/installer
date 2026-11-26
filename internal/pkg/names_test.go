// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package pkg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/confighub/installer/internal/testregistry"
	"github.com/confighub/installer/pkg/api"
)

func TestIsPath(t *testing.T) {
	for ref, want := range map[string]bool{
		"./k8sbot":                   true,
		"../k8sbot":                  true,
		".":                          true,
		"/abs/k8sbot":                true,
		"packages/k8sbot":            true,
		"k8sbot.tgz":                 true,
		"k8sbot.tar.gz":              true,
		"file:///abs/k8sbot":         true,
		"k8sbot":                     false,
		"k8sbot:0.2.12":              false,
		"ghcr.io/confighub/k8sbot":   true,
		"oci://ghcr.io/x/k8sbot:1.0": true,
	} {
		if got := IsPath(ref); got != want {
			t.Errorf("IsPath(%q) = %v, want %v", ref, got, want)
		}
	}
}

// pushTags pushes the test package to repo once per tag.
func pushTags(t *testing.T, reg Registry, repo string, tags ...string) {
	t.Helper()
	src := testPackage(t)
	for _, tag := range tags {
		if _, err := reg.Push(context.Background(), src, "oci://"+repo+":"+tag); err != nil {
			t.Fatalf("push %s:%s: %v", repo, tag, err)
		}
	}
}

func TestResolveRef(t *testing.T) {
	ctx := context.Background()
	srv := testregistry.New()
	defer srv.Close()
	reg := Registry{PackageRepository: srv.Host + "/packages/"}
	repo := srv.Host + "/packages/ocitest"
	// Releases are compared as versions, not strings; a prerelease or a
	// moving tag is not a release.
	pushTags(t, reg, repo, "0.2.0", "v0.10.0", "0.9.1", "1.0.0-rc.1", "latest")

	for ref, want := range map[string]string{
		"ocitest":                 "oci://" + repo + ":v0.10.0",
		"ocitest:0.2.0":           "oci://" + repo + ":0.2.0",
		"./ocitest":               "./ocitest",
		"oci://example.com/x:1.0": "oci://example.com/x:1.0",
		"":                        "",
	} {
		got, err := reg.ResolveRef(ctx, ref)
		if err != nil || got != want {
			t.Errorf("ResolveRef(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}

	if _, err := reg.ResolveRef(ctx, "Not_A_Package"); err == nil || !strings.Contains(err.Error(), "is not a package") {
		t.Errorf("an invalid name: err = %v, want one saying it is not a package", err)
	}

	tags, err := reg.List(ctx, "ocitest")
	if err != nil || len(tags) != 5 {
		t.Errorf("List of a bare name = %v, %v; want the package's 5 tags", tags, err)
	}
	res, err := reg.Inspect(ctx, "ocitest:0.2.0")
	if err != nil || res.Config.Manifest.Metadata.Name != "ocitest" {
		t.Errorf("Inspect of a bare name = %+v, %v; want the package", res, err)
	}
}

// A package pulled by name is recorded as the release the name resolved to,
// so that installing from the record again gets the same one.
func TestPullByNameRecordsRelease(t *testing.T) {
	srv := testregistry.New()
	defer srv.Close()
	reg := Registry{PackageRepository: srv.Host + "/packages"}
	repo := srv.Host + "/packages/ocitest"
	pushTags(t, reg, repo, "1.2.3")

	workDir := t.TempDir()
	if _, err := reg.PullToWorkDir(context.Background(), "ocitest", workDir); err != nil {
		t.Fatalf("PullToWorkDir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(workDir, "out", api.RecordDir, SourceFile))
	if err != nil {
		t.Fatal(err)
	}
	source, err := api.ParseSource(data)
	if err != nil {
		t.Fatal(err)
	}
	if source.Spec.Ref != "oci://"+repo+":1.2.3" || source.Spec.Digest == "" {
		t.Errorf("source = %+v, want the resolved release, pinned to its digest", source.Spec)
	}
}

// A bare name that fails to resolve, beside a local package of that name,
// says how to use the local one.
func TestBareNameLocalHint(t *testing.T) {
	srv := testregistry.New()
	defer srv.Close()
	reg := Registry{PackageRepository: srv.Host + "/packages"}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "mypkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if _, err := reg.Pull(context.Background(), "mypkg", t.TempDir()); err == nil || !strings.Contains(err.Error(), "write it as a path: ./mypkg") {
		t.Errorf("Pull beside a directory of that name: err = %v, want the ./ hint", err)
	}
	if _, err := reg.Pull(context.Background(), "otherpkg", t.TempDir()); err == nil || strings.Contains(err.Error(), "write it as a path") {
		t.Errorf("Pull of a name with no local package: err = %v, want no hint", err)
	}
}

// failingStore is a credential store that refuses to be asked.
type failingStore struct{}

func (failingStore) Get(context.Context, string) (auth.Credential, error) {
	return auth.EmptyCredential, errors.New("credential store asked")
}
func (failingStore) Put(context.Context, string, auth.Credential) error { return nil }
func (failingStore) Delete(context.Context, string) error               { return nil }

// ConfigHub's packages are read without credentials; everything else, and
// any write, with the Registry's.
func TestReadRepoCredentials(t *testing.T) {
	reg := Registry{Credentials: failingStore{}}
	for _, tc := range []struct {
		name      string
		repo      func(string) (*remote.Repository, error)
		repoRef   string
		anonymous bool
	}{
		{"read a ConfigHub package", reg.readRepo, api.PackageRepository + "/k8sbot", true},
		{"read another repository", reg.readRepo, "ghcr.io/someone/k8sbot", false},
		{"write a ConfigHub package", reg.newRepo, api.PackageRepository + "/k8sbot", false},
	} {
		repo, err := tc.repo(tc.repoRef)
		if err != nil {
			t.Fatal(err)
		}
		client, ok := repo.Client.(*auth.Client)
		if !ok {
			t.Fatalf("%s: client is %T", tc.name, repo.Client)
		}
		_, err = client.Credential(context.Background(), repo.Reference.Registry)
		if asked := err != nil; asked == tc.anonymous {
			t.Errorf("%s: asked the credential store = %v, want %v", tc.name, asked, !tc.anonymous)
		}
	}
}

// A registry reference typed without oci:// is a path that doesn't exist; the
// error says how to write it.
func TestRegistryHint(t *testing.T) {
	t.Chdir(t.TempDir())
	for ref, hint := range map[string]bool{
		"ghcr.io/example/app:1.0.0": true,
		"localhost:5000/app:1.0.0":  true,
		"localhost/app:1.0.0":       true,
		"./ghcr.io/app":             false,
		"examples/app":              false,
		"/no/such/app":              false,
	} {
		_, err := (Registry{}).Pull(context.Background(), ref, t.TempDir())
		if err == nil || !os.IsNotExist(errors.Unwrap(err)) {
			t.Errorf("Pull(%q): err = %v, want the path not found", ref, err)
			continue
		}
		if got := strings.Contains(err.Error(), "written oci://"+ref); got != hint {
			t.Errorf("Pull(%q): err = %v; hint %v, want %v", ref, err, got, hint)
		}
	}
}
