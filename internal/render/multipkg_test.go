package render_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/confighub/installer/internal/render"
	"github.com/confighub/installer/pkg/api"
)

const depPackageYAML = `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata: {name: dep, version: 0.1.0}
spec:
  bases:
    - {name: default, path: bases/default, default: true}
  transformers:
    - toolchain: Kubernetes/YAML
      whereResource: ""
      invocations:
        - {name: set-namespace, args: ["{{ .Namespace }}"]}
`

const depBaseKust = `resources:
  - cm.yaml
`

const depCM = `apiVersion: v1
kind: ConfigMap
metadata:
  name: dep-cm
`

const parentPackageYAML = `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata: {name: parent, version: 0.1.0}
spec:
  bases:
    - {name: default, path: bases/default, default: true}
  dependencies:
    - {name: dep-one, package: oci://test/dep, version: "^0.1.0"}
  transformers:
    - toolchain: Kubernetes/YAML
      whereResource: ""
      invocations:
        - {name: set-namespace, args: ["{{ .Namespace }}"]}
`

const parentBaseKust = `resources:
  - cm.yaml
`

const parentCM = `apiVersion: v1
kind: ConfigMap
metadata:
  name: parent-cm
`

// writeFile creates parent directories as needed.
func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeDepPackage writes a self-contained dep package under destDir/package/.
// Mirrors the layout produced by ipkg.Pull for a native installer artifact.
func writeDepPackage(t *testing.T, destDir string) string {
	t.Helper()
	root := filepath.Join(destDir, "package")
	writeFile(t, filepath.Join(root, "installer.yaml"), depPackageYAML)
	writeFile(t, filepath.Join(root, "bases/default/kustomization.yaml"), depBaseKust)
	writeFile(t, filepath.Join(root, "bases/default/cm.yaml"), depCM)
	return root
}

func TestRenderDependencies(t *testing.T) {
	if _, err := exec.LookPath("kustomize"); err != nil {
		t.Skip("kustomize not on PATH")
	}
	ctx := context.Background()
	work := t.TempDir()

	// Parent source.
	pkgDir := filepath.Join(work, "package")
	writeFile(t, filepath.Join(pkgDir, "installer.yaml"), parentPackageYAML)
	writeFile(t, filepath.Join(pkgDir, "bases/default/kustomization.yaml"), parentBaseKust)
	writeFile(t, filepath.Join(pkgDir, "bases/default/cm.yaml"), parentCM)

	parentInputs := &api.Inputs{
		APIVersion: api.APIVersion,
		Kind:       api.KindInputs,
		Spec:       api.InputsSpec{Package: "parent", Namespace: "demo"},
	}

	lock := &api.Lock{
		APIVersion: api.APIVersion,
		Kind:       api.KindLock,
		Metadata:   api.Metadata{Name: "parent"},
		Spec: api.LockSpec{
			Package: api.LockedPackage{Name: "parent", Version: "0.1.0"},
			Resolved: []api.LockedDependency{{
				Name:        "dep-one",
				Ref:         "oci://test/dep:0.1.0",
				Digest:      "sha256:" + strings.Repeat("a", 64),
				Version:     "0.1.0",
				RequestedBy: []string{"root"},
				Inputs:      map[string]any{"namespace": "dep-ns"},
			}},
		},
	}

	fakeFetcher := func(_ context.Context, _, _, destDir string) (string, error) {
		// Mimic the cache-hit semantics: only write once.
		root := filepath.Join(destDir, "package")
		if _, err := os.Stat(filepath.Join(root, "installer.yaml")); err == nil {
			return root, nil
		}
		return writeDepPackage(t, destDir), nil
	}

	results, err := render.RenderDependencies(ctx, render.DepsOptions{TransformerBinary: installerBin,
		Lock:         lock,
		ParentInputs: parentInputs,
		WorkDir:      work,
		Fetcher:      fakeFetcher,
	})
	if err != nil {
		t.Fatalf("RenderDependencies: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d dep results, want 1", len(results))
	}
	r := results[0]
	if r.Name != "dep-one" {
		t.Errorf("name = %q", r.Name)
	}
	wantOut := filepath.Join(work, "out", "dep-one")
	if r.OutDir != wantOut {
		t.Errorf("outDir = %q want %q", r.OutDir, wantOut)
	}
	if len(r.Manifests) != 1 {
		t.Fatalf("manifests = %d, want 1", len(r.Manifests))
	}

	// The dep's namespace from its lock entry (dep-ns) drove its
	// set-namespace function. Verify the rendered ConfigMap landed there.
	cmPath := filepath.Join(wantOut, "manifests", "configmap-dep-ns-dep-cm.yaml")
	body, err := os.ReadFile(cmPath)
	if err != nil {
		t.Fatalf("read %s: %v", cmPath, err)
	}
	if !strings.Contains(string(body), "namespace: dep-ns") {
		t.Errorf("dep namespace not applied:\n%s", body)
	}

	// The dep's spec dir should carry its own selection/inputs/function-chain.
	for _, name := range []string{"selection.yaml", "inputs.yaml", "function-chain.yaml", "manifest-index.yaml"} {
		if _, err := os.Stat(filepath.Join(wantOut, "spec", name)); err != nil {
			t.Errorf("missing dep spec doc %s: %v", name, err)
		}
	}
}

func TestRenderDependenciesNamespaceFallbackToParent(t *testing.T) {
	if _, err := exec.LookPath("kustomize"); err != nil {
		t.Skip("kustomize not on PATH")
	}
	ctx := context.Background()
	work := t.TempDir()

	parentInputs := &api.Inputs{
		APIVersion: api.APIVersion,
		Kind:       api.KindInputs,
		Spec:       api.InputsSpec{Package: "parent", Namespace: "parent-ns"},
	}

	lock := &api.Lock{
		Spec: api.LockSpec{
			Package: api.LockedPackage{Name: "parent"},
			Resolved: []api.LockedDependency{{
				Name:    "dep-one",
				Ref:     "oci://test/dep:0.1.0",
				Digest:  "sha256:" + strings.Repeat("b", 64),
				Version: "0.1.0",
				// No inputs.namespace — should fall back to parent's.
			}},
		},
	}

	results, err := render.RenderDependencies(ctx, render.DepsOptions{TransformerBinary: installerBin,
		Lock:         lock,
		ParentInputs: parentInputs,
		WorkDir:      work,
		Fetcher: func(_ context.Context, _, _, destDir string) (string, error) {
			return writeDepPackage(t, destDir), nil
		},
	})
	if err != nil {
		t.Fatalf("RenderDependencies: %v", err)
	}
	cmPath := filepath.Join(results[0].OutDir, "manifests", "configmap-parent-ns-dep-cm.yaml")
	body, err := os.ReadFile(cmPath)
	if err != nil {
		t.Fatalf("read %s: %v", cmPath, err)
	}
	if !strings.Contains(string(body), "namespace: parent-ns") {
		t.Errorf("parent namespace not used as fallback:\n%s", body)
	}
}

// TestRenderDependenciesDeterministic re-renders the same lock and verifies
// every output file is byte-identical across runs — the acceptance criterion
// for Phase 5.
func TestRenderDependenciesDeterministic(t *testing.T) {
	if _, err := exec.LookPath("kustomize"); err != nil {
		t.Skip("kustomize not on PATH")
	}
	ctx := context.Background()
	parentInputs := &api.Inputs{
		APIVersion: api.APIVersion,
		Kind:       api.KindInputs,
		Spec:       api.InputsSpec{Package: "parent", Namespace: "demo"},
	}
	lock := &api.Lock{
		Spec: api.LockSpec{
			Package: api.LockedPackage{Name: "parent"},
			Resolved: []api.LockedDependency{{
				Name:    "dep-one",
				Ref:     "oci://test/dep:0.1.0",
				Digest:  "sha256:" + strings.Repeat("c", 64),
				Version: "0.1.0",
			}},
		},
	}
	run := func() map[string]string {
		work := t.TempDir()
		_, err := render.RenderDependencies(ctx, render.DepsOptions{TransformerBinary: installerBin,
			Lock:         lock,
			ParentInputs: parentInputs,
			WorkDir:      work,
			Fetcher: func(_ context.Context, _, _, destDir string) (string, error) {
				return writeDepPackage(t, destDir), nil
			},
		})
		if err != nil {
			t.Fatalf("RenderDependencies: %v", err)
		}
		return collectHashes(t, filepath.Join(work, "out", "dep-one"))
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatalf("file count differs: %d vs %d", len(a), len(b))
	}
	for name, ha := range a {
		hb, ok := b[name]
		if !ok {
			t.Errorf("file %s present in run a, missing in run b", name)
			continue
		}
		if ha != hb {
			t.Errorf("digest mismatch for %s: %s vs %s", name, ha, hb)
		}
	}
}

// collectHashes returns rel-path → sha256 for every file under dir.
func collectHashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		sum := sha256.Sum256(data)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
