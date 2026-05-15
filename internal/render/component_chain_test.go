package render_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/render"
	"github.com/confighub/installer/internal/selection"
	"github.com/confighub/installer/pkg/api"
)

// TestComponentScopedTransformers verifies that a component's own
// spec.components[].transformers list only runs when the component is
// selected, and that its groups append to (don't replace) the package-wide
// transformers chain.
//
// The package below sets metadata.namespace via the package-wide chain. The
// `annotate` component then layers a yq-i call that adds a marker
// annotation. Selecting `annotate` should produce manifests with both the
// namespace AND the annotation; omitting it should produce only the
// namespace.
func TestComponentScopedTransformers(t *testing.T) {
	if _, err := exec.LookPath("kustomize"); err != nil {
		t.Skip("kustomize not on PATH")
	}

	for _, tc := range []struct {
		name              string
		selectAnnotate    bool
		expectAnnotation  bool
	}{
		{name: "annotate selected", selectAnnotate: true, expectAnnotation: true},
		{name: "annotate not selected", selectAnnotate: false, expectAnnotation: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkgDir := writeMixinPackage(t)
			loaded, err := ipkg.Load(pkgDir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			selected := []string{}
			if tc.selectAnnotate {
				selected = []string{"annotate"}
			}
			sel, err := selection.Resolve(loaded.Package, "", selected)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			inputs := mixinInputs("demo")

			work := t.TempDir()
			out := filepath.Join(work, "out")
			res, err := render.Render(context.Background(), render.Options{
				Loaded:            loaded,
				Selection:         sel,
				Inputs:            inputs,
				TransformerBinary: installerBin,
			}, out)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			cm := readManifest(t, res, "ConfigMap", "demo", "smoke")
			if !strings.Contains(cm, "namespace: demo") {
				t.Errorf("expected namespace: demo in:\n%s", cm)
			}
			hasAnnotation := strings.Contains(cm, "installer.test/marker: hello")
			if hasAnnotation != tc.expectAnnotation {
				t.Errorf("annotation present=%v, want %v.\n%s", hasAnnotation, tc.expectAnnotation, cm)
			}
		})
	}
}

func writeMixinPackage(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "installer.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata: {name: mixin, version: 0.1.0}
spec:
  bases:
    - {name: default, path: bases/default, default: true}
  components:
    - name: annotate
      path: components/annotate
      transformers:
        - toolchain: Kubernetes/YAML
          whereResource: "ConfigHub.ResourceType = 'v1/ConfigMap'"
          invocations:
            - name: yq-i
              args: ['.metadata.annotations."installer.test/marker" = "hello"']
  transformers:
    - toolchain: Kubernetes/YAML
      invocations:
        - {name: set-namespace, args: ["{{ .Namespace }}"]}
`)
	writeFile(t, filepath.Join(root, "bases/default/kustomization.yaml"), "resources:\n  - cm.yaml\n")
	writeFile(t, filepath.Join(root, "bases/default/cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: smoke
data:
  k: v
`)
	writeFile(t, filepath.Join(root, "components/annotate/kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
commonAnnotations:
  installer.test/component: annotate
`)
	return root
}

func mixinInputs(ns string) *api.Inputs {
	return &api.Inputs{
		APIVersion: api.APIVersion,
		Kind:       api.KindInputs,
		Spec: api.InputsSpec{
			Namespace: ns,
			Values:    map[string]any{},
		},
	}
}

// readManifest finds the rendered file matching kind/namespace/name and
// returns its body as a string.
func readManifest(t *testing.T, res *render.Result, kind, namespace, name string) string {
	t.Helper()
	for _, f := range res.Manifests {
		if f.Kind == kind && f.Namespace == namespace && f.Name == name {
			body, err := os.ReadFile(filepath.Join(res.OutDir, "manifests", f.Filename))
			if err != nil {
				t.Fatalf("read %s: %v", f.Filename, err)
			}
			return string(body)
		}
	}
	t.Fatalf("no manifest %s/%s/%s in result", kind, namespace, name)
	return ""
}
