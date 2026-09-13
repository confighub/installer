// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package upload

import (
	"path/filepath"
	"strings"
	"testing"

	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/confighub/installer/pkg/api"
)

// renderedPackage lays out one package's work-dir the way render leaves it and
// returns the Package that Discover would.
func renderedPackage(t *testing.T, name string, isParent bool) Package {
	t.Helper()
	work := t.TempDir()
	pkgDir := filepath.Join(work, "package")
	recordDir := filepath.Join(work, "out", api.RecordDir)
	manifestsDir := filepath.Join(work, "out", "manifests")
	writeFile(t, filepath.Join(pkgDir, "installer.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata: {name: `+name+`}
installerMetadata: {version: 1.2.3}
spec:
  bases:
    - {name: default, path: bases/default, default: true}
`)
	for file, doc := range map[string]any{
		"selection.yaml": api.Selection{APIVersion: api.APIVersion, Kind: api.KindSelection,
			Spec: api.SelectionSpec{Package: name, PackageVersion: "1.2.3", Base: "default", Components: []string{"core"}}},
		"inputs.yaml": api.Inputs{APIVersion: api.APIVersion, Kind: api.KindInputs,
			Spec: api.InputsSpec{Package: name, PackageVersion: "1.2.3", Namespace: "web", Values: map[string]any{"replicas": 2}}},
		"facts.yaml": api.Facts{APIVersion: api.APIVersion, Kind: api.KindFacts,
			Spec: api.FactsSpec{Package: name, PackageVersion: "1.2.3", Values: map[string]any{"clusterName": "dev"}}},
	} {
		data, err := api.MarshalYAML(doc)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(recordDir, file), string(data))
	}
	if isParent {
		data, err := api.MarshalYAML(api.Lock{APIVersion: api.APIVersion, Kind: api.KindLock,
			Spec: api.LockSpec{Package: api.LockedPackage{Name: name, Version: "1.2.3"},
				Resolved: []api.LockedDependency{{Name: "db", Ref: "oci://reg/db:1.0.0", Digest: "sha256:abc"}}}})
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(recordDir, "lock.yaml"), string(data))
	}
	writeFile(t, filepath.Join(manifestsDir, "deployment-web.yaml"), "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n")
	writeFile(t, filepath.Join(manifestsDir, "notes.txt"), "not a manifest")
	return Package{
		Name: name, Version: "1.2.3", PackageDir: pkgDir, ManifestsDir: manifestsDir,
		RecordDir: recordDir, SpaceSlug: name + "-space", IsParent: isParent,
	}
}

// The record is one AppConfig/YAML document: the configHub schema paths, not KRM
// apiVersion/kind/metadata, and the package name and version said once.
func TestBuildRecord(t *testing.T) {
	pkg := renderedPackage(t, "web", true)
	data, err := BuildRecord(pkg)
	if err != nil {
		t.Fatalf("BuildRecord: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("record is not one YAML document: %v\n%s", err, data)
	}
	if strings.Contains(string(data), "\n---") {
		t.Errorf("record must be a single document:\n%s", data)
	}
	for _, key := range []string{"apiVersion", "kind", "metadata"} {
		if _, ok := doc[key]; ok {
			t.Errorf("record has a top-level %s; it should use the configHub schema paths:\n%s", key, data)
		}
	}
	meta, _ := doc["configHub"].(map[string]any)
	if meta["configSchema"] != RecordSchema || meta["configName"] != RecordConfigName {
		t.Errorf("configHub = %v", meta)
	}
	for _, section := range []string{"selection", "inputs", "facts"} {
		spec, _ := doc[section].(map[string]any)
		if spec == nil {
			t.Errorf("record has no %s:\n%s", section, data)
			continue
		}
		if _, ok := spec["package"]; ok {
			t.Errorf("%s repeats the package name:\n%s", section, data)
		}
	}
	if _, ok := doc["lock"]; !ok {
		t.Errorf("the parent's record carries its lock:\n%s", data)
	}

	prior, err := ParseRecord(data)
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if prior.Package.Metadata.Name != "web" || prior.Package.InstallerMetadata.Version != "1.2.3" {
		t.Errorf("package = %+v", prior.Package)
	}
	if prior.Inputs.Spec.Namespace != "web" || prior.Inputs.Spec.Package != "web" || prior.Inputs.Spec.PackageVersion != "1.2.3" {
		t.Errorf("inputs = %+v", prior.Inputs.Spec)
	}
	if prior.Selection.Spec.Base != "default" || prior.Facts.Spec.Values["clusterName"] != "dev" {
		t.Errorf("selection = %+v, facts = %+v", prior.Selection.Spec, prior.Facts.Spec)
	}
	if prior.Lock == nil || len(prior.Lock.Spec.Resolved) != 1 {
		t.Errorf("lock = %+v", prior.Lock)
	}
}

func TestParseRecordRejectsOtherSchemas(t *testing.T) {
	if _, err := ParseRecord([]byte("configHub:\n  configSchema: Other\n")); err == nil {
		t.Fatal("expected an error for a different configSchema")
	}
}

// One request per package: its manifests and record, in its Space, with the
// install namespace as the release namespace. Only the parent names its
// dependencies, by their component names.
func TestBuildRequest(t *testing.T) {
	parent := renderedPackage(t, "web", true)
	dep := renderedPackage(t, "db", false)
	packages := []Package{parent, dep}
	target := goclientnew.UUID(uuid.New())
	opts := RequestOptions{
		Component:   "storefront",
		Variant:     "base",
		SpaceLabels: map[string]string{"Environment": "Prod"},
		TargetIDs:   map[string]goclientnew.UUID{"web-space": target},
		SourceRef:   "/work",
	}

	req, err := BuildRequest(parent, packages, opts)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	var paths []string
	for _, f := range req.Files {
		paths = append(paths, f.Path)
	}
	if strings.Join(paths, ",") != "manifests/deployment-web.yaml,"+RecordFileName {
		t.Errorf("files = %v", paths)
	}
	c := req.Components[0]
	if c.Name != "storefront" || c.Space != "web-space" || c.Namespace != "web" {
		t.Errorf("component = %+v", c)
	}
	if c.SpaceLabels["Component"] != "storefront" || c.SpaceLabels["Variant"] != "base" || c.SpaceLabels["Environment"] != "Prod" {
		t.Errorf("space labels = %v", c.SpaceLabels)
	}
	if c.TargetID == nil || *c.TargetID != target {
		t.Errorf("target = %v", c.TargetID)
	}
	if strings.Join(c.DependsOn, ",") != "db" {
		t.Errorf("the parent depends on its dependency's component: %v", c.DependsOn)
	}
	if req.Source == nil || req.Source.Client != "installer" || req.Source.Ref != "/work" {
		t.Errorf("source = %+v", req.Source)
	}

	depReq, err := BuildRequest(dep, packages, opts)
	if err != nil {
		t.Fatalf("BuildRequest(dep): %v", err)
	}
	d := depReq.Components[0]
	if d.Name != "db" || d.SpaceLabels["Component"] != "db" {
		t.Errorf("a dependency is its own component, not --component: %+v", d)
	}
	if len(d.DependsOn) != 0 || d.TargetID != nil {
		t.Errorf("dependency = %+v", d)
	}
}

func TestSummary(t *testing.T) {
	results := []*goclientnew.UploadResult{{Components: []goclientnew.UploadComponentResult{{Spaces: []goclientnew.UploadSpaceResult{{
		Units: []goclientnew.UploadUnitResult{
			{Action: "Create"}, {Action: "Update"}, {Action: "Unchanged"}, {Action: "Empty"},
			{Action: "Update", Error: &goclientnew.ResponseError{Message: "conflict"}},
		},
	}}}}}}
	if got := Summarize(results).Plan(); got != "Plan: 1 to create, 1 to update, 1 to empty, 0 to revive, 0 to adopt." {
		t.Errorf("got %q", got)
	}
	if got := Summarize(nil).Applied(); got != "No changes." {
		t.Errorf("got %q", got)
	}
}
