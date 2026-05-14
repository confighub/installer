package upload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/confighubai/installer/pkg/api"
)

func TestRenderSpaceSlug(t *testing.T) {
	cases := []struct {
		pattern string
		vars    Vars
		want    string
		wantErr bool
	}{
		{"{{.PackageName}}", Vars{PackageName: "foo"}, "foo", false},
		{"{{.PackageName}}-{{.PackageVersion}}", Vars{PackageName: "foo", PackageVersion: "0.3.0"}, "foo-0.3.0", false},
		{"prod-{{.PackageName}}", Vars{PackageName: "x"}, "prod-x", false},
		{"", Vars{PackageName: "x"}, "", true},
		{"{{.Bogus}}", Vars{PackageName: "x"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			got, err := RenderSpaceSlug(tc.pattern, tc.vars)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestDiscoverParentOnly(t *testing.T) {
	work := t.TempDir()
	writeFile(t, filepath.Join(work, "package", "installer.yaml"), minimalParentYAML)
	writeFile(t, filepath.Join(work, "package", "bases/default/kustomization.yaml"), "resources: []")
	writeFile(t, filepath.Join(work, "out", "manifests", "x.yaml"), "kind: ConfigMap")
	writeFile(t, filepath.Join(work, "out", "spec", "selection.yaml"), "kind: Selection")

	parent, err := api.ParsePackage([]byte(minimalParentYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pkgs, err := Discover(DiscoverInput{
		WorkDir:       work,
		SpacePattern:  "{{.PackageName}}",
		ParentPackage: parent,
		Lock:          nil,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("got %d packages, want 1", len(pkgs))
	}
	if !pkgs[0].IsParent || pkgs[0].SpaceSlug != "parent" {
		t.Fatalf("parent mismatch: %+v", pkgs[0])
	}
}

func TestDiscoverWithLock(t *testing.T) {
	work := t.TempDir()
	writeFile(t, filepath.Join(work, "package", "installer.yaml"), parentWithDepYAML)
	writeFile(t, filepath.Join(work, "package", "bases/default/kustomization.yaml"), "resources: []")
	// Vendor cache is keyed by the lock's local handle (matches what
	// render.RenderDependencies writes), not by the dep's package name.
	writeFile(t, filepath.Join(work, "out", "vendor", "dep-a@0.2.0", "package", "installer.yaml"), depPackageYAML)
	writeFile(t, filepath.Join(work, "out", "vendor", "dep-a@0.2.0", "package", "bases/default/kustomization.yaml"), "resources: []")

	parent, err := api.ParsePackage([]byte(parentWithDepYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	lock := &api.Lock{
		Spec: api.LockSpec{
			Package: api.LockedPackage{Name: "parent", Version: "0.1.0"},
			Resolved: []api.LockedDependency{{
				Name:    "dep-a",
				Ref:     "oci://reg/dep-pkg:0.2.0",
				Version: "0.2.0",
			}},
		},
	}
	pkgs, err := Discover(DiscoverInput{
		WorkDir:       work,
		SpacePattern:  "{{.PackageName}}-{{.PackageVersion}}",
		ParentPackage: parent,
		Lock:          lock,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("got %d packages, want 2", len(pkgs))
	}
	if pkgs[0].SpaceSlug != "parent-0.1.0" || !pkgs[0].IsParent {
		t.Fatalf("parent mismatch: %+v", pkgs[0])
	}
	dep := pkgs[1]
	if dep.SpaceSlug != "dep-pkg-0.2.0" || dep.IsParent {
		t.Fatalf("dep mismatch: %+v", dep)
	}
	if dep.LocalHandle != "dep-a" {
		t.Fatalf("LocalHandle = %q want dep-a", dep.LocalHandle)
	}
	wantManifests := filepath.Join(work, "out", "dep-a", "manifests")
	if dep.ManifestsDir != wantManifests {
		t.Fatalf("ManifestsDir = %q want %q", dep.ManifestsDir, wantManifests)
	}
}

func TestDiscoverErrorOnMissingVendor(t *testing.T) {
	work := t.TempDir()
	writeFile(t, filepath.Join(work, "package", "installer.yaml"), parentWithDepYAML)
	writeFile(t, filepath.Join(work, "package", "bases/default/kustomization.yaml"), "resources: []")
	parent, _ := api.ParsePackage([]byte(parentWithDepYAML))
	lock := &api.Lock{
		Spec: api.LockSpec{
			Package: api.LockedPackage{Name: "parent"},
			Resolved: []api.LockedDependency{{Name: "dep-a", Ref: "oci://reg/dep-pkg:0.2.0", Version: "0.2.0"}},
		},
	}
	_, err := Discover(DiscoverInput{WorkDir: work, ParentPackage: parent, Lock: lock})
	if err == nil || !strings.Contains(err.Error(), "vendor missing") {
		t.Fatalf("expected vendor-missing error, got %v", err)
	}
}

func TestBuildInstallerRecord(t *testing.T) {
	work := t.TempDir()
	pkgDir := filepath.Join(work, "package")
	specDir := filepath.Join(work, "out", "spec")
	writeFile(t, filepath.Join(pkgDir, "installer.yaml"), minimalParentYAML)
	writeFile(t, filepath.Join(specDir, "selection.yaml"), "kind: Selection\nmetadata: {name: x}\n")
	writeFile(t, filepath.Join(specDir, "inputs.yaml"), "kind: Inputs\nmetadata: {name: y}\n")
	writeFile(t, filepath.Join(specDir, "notes.txt"), "ignored")

	body, err := BuildInstallerRecord(Package{
		PackageDir: pkgDir,
		SpecDir:    specDir,
	})
	if err != nil {
		t.Fatalf("BuildInstallerRecord: %v", err)
	}
	docs := strings.Split(string(body), "\n---\n")
	if len(docs) != 3 {
		t.Fatalf("got %d docs, want 3 (installer.yaml + 2 spec files): \n%s", len(docs), body)
	}
	if !strings.Contains(docs[0], "kind: Package") {
		t.Errorf("first doc should be Package: %s", docs[0])
	}
	// Sorted by filename within spec/: inputs.yaml before selection.yaml.
	if !strings.Contains(docs[1], "kind: Inputs") {
		t.Errorf("second doc should be Inputs (sorted): %s", docs[1])
	}
	if !strings.Contains(docs[2], "kind: Selection") {
		t.Errorf("third doc should be Selection: %s", docs[2])
	}
	// Non-YAML files are ignored.
	if strings.Contains(string(body), "ignored") {
		t.Errorf("notes.txt should have been filtered out")
	}
}

func TestSplitInstallerRecord(t *testing.T) {
	work := t.TempDir()
	pkgDir := filepath.Join(work, "package")
	specDir := filepath.Join(work, "out", "spec")
	writeFile(t, filepath.Join(pkgDir, "installer.yaml"), minimalParentYAML)
	writeFile(t, filepath.Join(specDir, "selection.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Selection
metadata: {name: parent-selection}
spec:
  package: parent
  base: default
  components: [a, b]
`)
	writeFile(t, filepath.Join(specDir, "inputs.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Inputs
metadata: {name: parent-inputs}
spec:
  package: parent
  namespace: demo
  values: {greeting: hi}
`)
	writeFile(t, filepath.Join(specDir, "upload.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Upload
metadata: {name: parent-upload}
spec:
  package: parent
  packageVersion: 0.1.0
  spaces:
    - {package: parent, version: 0.1.0, slug: parent, isParent: true}
  organizationID: org_test
  server: https://hub.example.com
`)

	body, err := BuildInstallerRecord(Package{PackageDir: pkgDir, SpecDir: specDir})
	if err != nil {
		t.Fatalf("BuildInstallerRecord: %v", err)
	}
	got, err := SplitInstallerRecord(body)
	if err != nil {
		t.Fatalf("SplitInstallerRecord: %v", err)
	}
	if got.Package == nil || got.Package.Metadata.Name != "parent" {
		t.Errorf("Package missing or wrong: %+v", got.Package)
	}
	if got.Selection == nil || got.Selection.Spec.Base != "default" {
		t.Errorf("Selection missing or wrong: %+v", got.Selection)
	}
	if got.Inputs == nil || got.Inputs.Spec.Namespace != "demo" {
		t.Errorf("Inputs missing or wrong: %+v", got.Inputs)
	}
	if got.Upload == nil || got.Upload.Spec.OrganizationID != "org_test" {
		t.Errorf("Upload missing or wrong: %+v", got.Upload)
	}
}

func TestPlanCrossSpaceLinks(t *testing.T) {
	packages := []Package{
		{Name: "parent", SpaceSlug: "parent-space", IsParent: true},
		{Name: "dep-pkg-a", LocalHandle: "dep-a", SpaceSlug: "dep-pkg-a-space"},
		{Name: "dep-pkg-b", LocalHandle: "dep-b", SpaceSlug: "dep-pkg-b-space"},
	}
	links := PlanCrossSpaceLinks(packages)
	if len(links) != 2 {
		t.Fatalf("got %d links, want 2", len(links))
	}
	if links[0].Slug != "dep-dep-a" {
		t.Errorf("link[0].Slug = %q", links[0].Slug)
	}
	if links[0].FromSpace != "parent-space" || links[0].ToSpace != "dep-pkg-a-space" {
		t.Errorf("link[0] spaces wrong: %+v", links[0])
	}
	if links[0].FromUnit != InstallerRecordSlug || links[0].ToUnit != InstallerRecordSlug {
		t.Errorf("link[0] units should be the record slug: %+v", links[0])
	}
	if links[0].Component != "parent" {
		t.Errorf("link[0].Component = %q want %q", links[0].Component, "parent")
	}
}

func TestPlanCrossSpaceLinksEmpty(t *testing.T) {
	if got := PlanCrossSpaceLinks([]Package{{Name: "p", IsParent: true}}); got != nil {
		t.Fatalf("parent-only should produce no links, got %v", got)
	}
	if got := PlanCrossSpaceLinks(nil); got != nil {
		t.Fatalf("nil should produce no links")
	}
}

// --- fixtures + helpers --------------------------------------------------

const minimalParentYAML = `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata: {name: parent, version: 0.1.0}
spec:
  bases:
    - {name: default, path: bases/default, default: true}
`

const parentWithDepYAML = `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata: {name: parent, version: 0.1.0}
spec:
  bases:
    - {name: default, path: bases/default, default: true}
  dependencies:
    - name: dep-a
      package: oci://reg/dep-pkg
      version: "^0.2.0"
`

const depPackageYAML = `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata: {name: dep-pkg, version: 0.2.0}
spec:
  bases:
    - {name: default, path: bases/default, default: true}
`

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
