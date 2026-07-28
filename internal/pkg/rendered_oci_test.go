// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"oras.land/oras-go/v2/content/memory"

	"github.com/confighub/installer/pkg/api"
)

func TestStageRenderedOCIRoundTrip(t *testing.T) {
	opts := testRenderedOCIOptions(t)
	ctx := context.Background()
	target := memory.New()

	result, err := stageAndCopyRendered(ctx, opts, "v1", target)
	if err != nil {
		t.Fatalf("stageAndCopyRendered: %v", err)
	}
	if !result.Verified {
		t.Fatal("rendered OCI was not verified after write")
	}
	if result.ManifestCount != 2 {
		t.Fatalf("manifest count: got %d want 2", result.ManifestCount)
	}
	wantFiles := []string{
		"kustomization.yaml",
		"dependency/manifests/service.yaml",
		"manifests/deployment.yaml",
	}
	if !slices.Equal(result.Files, wantFiles) {
		t.Fatalf("files: got %v want %v", result.Files, wantFiles)
	}
	for _, file := range result.Files {
		if strings.Contains(file, "secret") {
			t.Fatalf("rendered Secret leaked into OCI layer: %s", file)
		}
	}

	inspection, err := inspectRenderedFromTarget(ctx, target, "v1")
	if err != nil {
		t.Fatal(err)
	}
	config := inspection.config
	if config.Source.Reference != opts.SourceReference || config.Source.ManifestDigest != opts.SourceDigest {
		t.Fatalf("source provenance mismatch: %+v", config.Source)
	}
	if config.Render.Base != "default" || config.Render.Namespace != "demo" {
		t.Fatalf("render context mismatch: %+v", config.Render)
	}
	if len(config.Checks) != 2 || config.Checks[0].Name != "render" || config.Checks[1].Name != "vet-schemas" {
		t.Fatalf("checks mismatch: %+v", config.Checks)
	}
	if config.Output.ObjectSetDigest != result.ObjectSetDigest {
		t.Fatalf("object-set digest mismatch: config=%s result=%s", config.Output.ObjectSetDigest, result.ObjectSetDigest)
	}
}

func TestStageRenderedOCIIsDeterministic(t *testing.T) {
	opts := testRenderedOCIOptions(t)
	ctx := context.Background()
	first, err := stageAndCopyRendered(ctx, opts, "v1", memory.New())
	if err != nil {
		t.Fatal(err)
	}
	second, err := stageAndCopyRendered(ctx, opts, "v1", memory.New())
	if err != nil {
		t.Fatal(err)
	}
	if first.ManifestDigest != second.ManifestDigest {
		t.Fatalf("manifest digest is not deterministic: %s vs %s", first.ManifestDigest, second.ManifestDigest)
	}
	if first.ObjectSetDigest != second.ObjectSetDigest {
		t.Fatalf("object-set digest is not deterministic: %s vs %s", first.ObjectSetDigest, second.ObjectSetDigest)
	}
}

func TestPublishRenderedOCILocalLayout(t *testing.T) {
	opts := testRenderedOCIOptions(t)
	opts.Destination = filepath.Join(t.TempDir(), "rendered.oci")

	result, err := PublishRenderedOCI(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified {
		t.Fatal("local OCI layout did not pass pull-back verification")
	}
	for _, name := range []string{"oci-layout", "index.json"} {
		if _, err := os.Stat(filepath.Join(opts.Destination, name)); err != nil {
			t.Fatalf("local OCI layout missing %s: %v", name, err)
		}
	}
}

func testRenderedOCIOptions(t *testing.T) RenderedOCIOptions {
	t.Helper()
	workDir := t.TempDir()
	writeTestFile := func(rel, body string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(workDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile("out/manifests/deployment.yaml", "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app\n", 0o644)
	writeTestFile("out/dependency/manifests/service.yaml", "apiVersion: v1\nkind: Service\nmetadata:\n  name: dependency\n", 0o644)
	writeTestFile("out/secrets/secret.yaml", "apiVersion: v1\nkind: Secret\nmetadata:\n  name: private\n", 0o600)
	writeTestFile("out/spec/selection.yaml", "kind: Selection\nspec:\n  base: default\n", 0o644)
	writeTestFile("out/spec/inputs.yaml", "kind: Inputs\nspec:\n  namespace: demo\n  values:\n    token: do-not-publish\n", 0o644)
	writeTestFile("out/spec/function-chain.yaml", "kind: FunctionChain\nspec:\n  groups: []\n", 0o644)

	return RenderedOCIOptions{
		WorkDir:         workDir,
		Destination:     filepath.Join(t.TempDir(), "unused"),
		SourceReference: "oci://registry.example/test/demo:1.2.3",
		SourceDigest:    "sha256:" + strings.Repeat("a", 64),
		Package: &api.Package{
			Metadata:          api.Metadata{Name: "demo"},
			InstallerMetadata: api.InstallerMetadata{Version: "1.2.3"},
			Spec: api.PackageSpec{
				Validators: []api.FunctionGroup{{
					Toolchain: "Kubernetes/YAML",
					Invocations: []api.FunctionInvocation{{
						Name: "vet-schemas",
					}},
				}},
			},
		},
		Selection: &api.Selection{Spec: api.SelectionSpec{
			Base:       "default",
			Components: []string{"dependency"},
		}},
		Inputs: &api.Inputs{Spec: api.InputsSpec{
			Namespace: "demo",
			Values:    map[string]any{"token": "do-not-publish"},
		}},
	}
}
