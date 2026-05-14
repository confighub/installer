package pkg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	"oras.land/oras-go/v2/content/memory"

	"github.com/confighubai/installer/internal/bundle"
	"github.com/confighubai/installer/pkg/api"
)

const testInstallerYAML = `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata:
  name: ocitest
  version: 1.2.3
spec:
  bases:
    - {name: default, path: bases/default, default: true}
`

// testPackage creates a minimal but valid package source tree on disk and
// returns its root.
func testPackage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must := func(p, c string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("installer.yaml", testInstallerYAML)
	must("bases/default/kustomization.yaml", "resources:\n  - cm.yaml\n")
	must("bases/default/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n")
	return dir
}

// TestStageAndInspectRoundTrip pushes a package into an in-memory oras target,
// inspects it through the same target, and verifies the round-trip preserves
// the manifest, layer digest, and file list.
func TestStageAndInspectRoundTrip(t *testing.T) {
	src := testPackage(t)
	ctx := context.Background()
	mem := memory.New()

	pushed, err := stageAndCopy(ctx, src, "v1", mem)
	if err != nil {
		t.Fatalf("stageAndCopy: %v", err)
	}
	if !strings.HasPrefix(pushed.LayerDigest, "sha256:") {
		t.Fatalf("layer digest missing sha256 prefix: %s", pushed.LayerDigest)
	}
	if !strings.HasPrefix(pushed.ManifestDigest, "sha256:") {
		t.Fatalf("manifest digest missing sha256 prefix: %s", pushed.ManifestDigest)
	}
	wantFiles := []string{
		"bases/default/cm.yaml",
		"bases/default/kustomization.yaml",
		"installer.yaml",
	}
	if !equalStrings(pushed.Files, wantFiles) {
		t.Fatalf("file list mismatch: got %v want %v", pushed.Files, wantFiles)
	}

	got, err := inspectFromTarget(ctx, mem, "v1", "")
	if err != nil {
		t.Fatalf("inspectFromTarget: %v", err)
	}
	if got.ManifestDigest != pushed.ManifestDigest {
		t.Fatalf("manifest digest mismatch: inspect=%s pushed=%s", got.ManifestDigest, pushed.ManifestDigest)
	}
	cb := got.Config
	if cb.Manifest.Metadata.Name != "ocitest" || cb.Manifest.Metadata.Version != "1.2.3" {
		t.Fatalf("manifest metadata wrong: %+v", cb.Manifest.Metadata)
	}
	if cb.Bundle.LayerDigest != pushed.LayerDigest {
		t.Fatalf("layer digest in config blob (%s) differs from pushed (%s)",
			cb.Bundle.LayerDigest, pushed.LayerDigest)
	}
	if cb.Bundle.LayerSize != pushed.LayerSize {
		t.Fatalf("layer size mismatch: blob=%d pushed=%d", cb.Bundle.LayerSize, pushed.LayerSize)
	}
	if !equalStrings(cb.Bundle.Files, wantFiles) {
		t.Fatalf("config blob file list mismatch: got %v want %v", cb.Bundle.Files, wantFiles)
	}
}

// TestStageDeterministicManifest verifies that two pushes of the same source
// tree produce the same manifest digest — required for digest pinning and
// signing to be meaningful.
func TestStageDeterministicManifest(t *testing.T) {
	src := testPackage(t)
	ctx := context.Background()

	a, err := stageAndCopy(ctx, src, "v1", memory.New())
	if err != nil {
		t.Fatal(err)
	}
	b, err := stageAndCopy(ctx, src, "v1", memory.New())
	if err != nil {
		t.Fatal(err)
	}
	if a.ManifestDigest != b.ManifestDigest {
		t.Fatalf("manifest digest not deterministic: %s vs %s", a.ManifestDigest, b.ManifestDigest)
	}
	if a.LayerDigest != b.LayerDigest {
		t.Fatalf("layer digest not deterministic: %s vs %s", a.LayerDigest, b.LayerDigest)
	}
}

// TestInspectDigestPin enforces that a wrong @sha256:... pin fails inspect
// even when the tag resolves successfully.
func TestInspectDigestPin(t *testing.T) {
	src := testPackage(t)
	ctx := context.Background()
	mem := memory.New()
	pushed, err := stageAndCopy(ctx, src, "v1", mem)
	if err != nil {
		t.Fatal(err)
	}
	good, _ := digest.Parse(pushed.ManifestDigest)
	if _, err := inspectFromTarget(ctx, mem, "v1", good); err != nil {
		t.Fatalf("correct pin should succeed: %v", err)
	}
	bad := digest.Digest("sha256:" + strings.Repeat("0", 64))
	_, err = inspectFromTarget(ctx, mem, "v1", bad)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("wrong pin should fail with digest mismatch, got %v", err)
	}
}

func TestStageRejectsNonPackage(t *testing.T) {
	dir := t.TempDir()
	// no installer.yaml
	_, err := stageAndCopy(context.Background(), dir, "v1", memory.New())
	if err == nil {
		t.Fatalf("expected error for non-package dir")
	}
}

func TestParseRef(t *testing.T) {
	cases := []struct {
		in         string
		requireTag bool
		wantRepo   string
		wantTag    string
		wantDigest digest.Digest
		wantErr    bool
	}{
		{in: "oci://ghcr.io/o/r:v1", wantRepo: "ghcr.io/o/r", wantTag: "v1"},
		{in: "ghcr.io/o/r:v1", wantRepo: "ghcr.io/o/r", wantTag: "v1"},
		{in: "oci://ghcr.io/o/r:v1@sha256:" + strings.Repeat("a", 64), wantRepo: "ghcr.io/o/r", wantTag: "v1", wantDigest: digest.Digest("sha256:" + strings.Repeat("a", 64))},
		{in: "ghcr.io/o/r@sha256:" + strings.Repeat("b", 64), wantRepo: "ghcr.io/o/r", wantDigest: digest.Digest("sha256:" + strings.Repeat("b", 64))},
		{in: "ghcr.io/o/r", wantErr: true},
		{in: "ghcr.io/o/r@sha256:" + strings.Repeat("c", 64), requireTag: true, wantErr: true},
		{in: "ghcr.io/o/r:v1", requireTag: true, wantRepo: "ghcr.io/o/r", wantTag: "v1"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			repo, tag, dgst, err := parseRef(tc.in, tc.requireTag)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if repo != tc.wantRepo {
				t.Fatalf("repo: got %q want %q", repo, tc.wantRepo)
			}
			if tag != tc.wantTag {
				t.Fatalf("tag: got %q want %q", tag, tc.wantTag)
			}
			if dgst != tc.wantDigest {
				t.Fatalf("digest: got %q want %q", dgst, tc.wantDigest)
			}
		})
	}
}

// TestListTarFilesSorted ensures the file list extracted from a layer matches
// the bundler's deterministic order.
func TestListTarFilesSorted(t *testing.T) {
	src := testPackage(t)
	dst := filepath.Join(t.TempDir(), "p.tgz")
	if _, err := bundle.Bundle(src, dst); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	files, err := listTarFiles(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"bases/default/cm.yaml",
		"bases/default/kustomization.yaml",
		"installer.yaml",
	}
	if !equalStrings(files, want) {
		t.Fatalf("got %v want %v", files, want)
	}
}

// ensure api types are referenced so import isn't pruned.
var _ = api.ArtifactType

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
