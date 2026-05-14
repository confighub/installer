package userconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileReturnsEmptyState(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "doesnotexist.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if s == nil {
		t.Fatal("expected empty state, got nil")
	}
	if s.FindInstall("anything", "") != nil {
		t.Errorf("empty state should have no installs")
	}
}

func TestUpsertReplacesExisting(t *testing.T) {
	s := &State{}
	s.UpsertInstall(InstallRecord{
		Package: "kubernetes-resources", OrganizationID: "org_a", SpaceSlug: "k8s-res-a",
	})
	s.UpsertInstall(InstallRecord{
		Package: "kubernetes-resources", OrganizationID: "org_b", SpaceSlug: "k8s-res-b",
	})
	// Replace the first record.
	s.UpsertInstall(InstallRecord{
		Package: "kubernetes-resources", OrganizationID: "org_a", SpaceSlug: "k8s-res-a-renamed",
	})
	if len(s.Spec.Installs) != 2 {
		t.Fatalf("expected 2 installs after replace, got %d", len(s.Spec.Installs))
	}
	if r := s.FindInstall("kubernetes-resources", "org_a"); r == nil || r.SpaceSlug != "k8s-res-a-renamed" {
		t.Errorf("FindInstall org_a wrong: %+v", r)
	}
	if r := s.FindInstall("kubernetes-resources", "org_b"); r == nil || r.SpaceSlug != "k8s-res-b" {
		t.Errorf("FindInstall org_b wrong: %+v", r)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.yaml")
	s := &State{}
	s.UpsertInstall(InstallRecord{
		Package: "kubernetes-resources", OrganizationID: "org_x",
		Server: "https://hub.example.com", SpaceSlug: "k8s-res",
	})
	if err := Save(path, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Permissions check — file should be 0600.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file perm = %o, want 0600", info.Mode().Perm())
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r := loaded.FindInstall("kubernetes-resources", "org_x"); r == nil {
		t.Fatal("install record lost on round-trip")
	} else if r.Server != "https://hub.example.com" {
		t.Errorf("server lost: %+v", r)
	}
}

func TestFindInstallEmptyOrgMatchesAny(t *testing.T) {
	s := &State{}
	s.UpsertInstall(InstallRecord{
		Package: "kubernetes-resources", OrganizationID: "org_x", SpaceSlug: "k8s-res-x",
	})
	if r := s.FindInstall("kubernetes-resources", ""); r == nil {
		t.Errorf("empty org should match any")
	}
}
