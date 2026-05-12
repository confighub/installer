package render_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ipkg "github.com/confighubai/installer/internal/pkg"
	"github.com/confighubai/installer/internal/render"
	"github.com/confighubai/installer/internal/selection"
	"github.com/confighubai/installer/internal/wizard"
)

// TestEndToEnd_HelloApp drives the example package through wizard → render
// and checks the rendered output looks correct.
//
// Requires the kustomize binary on PATH; skipped if missing.
func TestEndToEnd_HelloApp(t *testing.T) {
	if _, err := exec.LookPath("kustomize"); err != nil {
		t.Skip("kustomize not on PATH")
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(repoRoot, "examples/hello-app")

	loaded, err := ipkg.Load(pkgDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tmp := t.TempDir()
	outDir := filepath.Join(tmp, "out")

	sel, inputs, err := wizard.Run(loaded.Package, wizard.RawAnswers{
		Inputs: map[string]string{
			"namespace": "demo",
			"image":     "nginx:latest",
		},
		SelectedComponents: []string{"monitoring"},
	}, outDir)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}

	// Reuse the selection solver assertion just to confirm the wiring.
	if got, _ := selection.Resolve(loaded.Package, "", []string{"monitoring"}); got.Spec.Base != sel.Spec.Base {
		t.Errorf("solver and wizard disagree on base")
	}

	result, err := render.Render(context.Background(), render.Options{
		Loaded:    loaded,
		Selection: sel,
		Inputs:    inputs,
	}, outDir)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// Expect 4 resources: Namespace + Deployment + Service + ServiceMonitor.
	if len(result.Files) != 4 {
		t.Fatalf("got %d files, want 4: %v", len(result.Files), filenames(result.Files))
	}

	manifestsDir := filepath.Join(outDir, "manifests")
	// Slug includes namespace because set-namespace populates metadata.namespace.
	deploymentBytes, err := os.ReadFile(filepath.Join(manifestsDir, "deployment-demo-hello-app.yaml"))
	if err != nil {
		t.Fatalf("read deployment: %v", err)
	}
	got := string(deploymentBytes)
	// set-container-image should have replaced the literal default image.
	if !strings.Contains(got, "image: nginx:latest") {
		t.Errorf("set-container-image did not apply:\n%s", got)
	}
	// set-namespace should have populated metadata.namespace.
	if !strings.Contains(got, "namespace: demo") {
		t.Errorf("set-namespace did not apply:\n%s", got)
	}

	// set-name should have renamed the Namespace resource.
	nsBytes, err := os.ReadFile(filepath.Join(manifestsDir, "namespace-demo.yaml"))
	if err != nil {
		t.Fatalf("read namespace: %v", err)
	}
	if !strings.Contains(string(nsBytes), "name: demo") {
		t.Errorf("set-name did not rename Namespace:\n%s", nsBytes)
	}

	// Spec docs persisted.
	for _, name := range []string{"selection.yaml", "inputs.yaml", "function-chain.yaml", "manifest-index.yaml"} {
		if _, err := os.Stat(filepath.Join(outDir, "spec", name)); err != nil {
			t.Errorf("missing spec doc %s: %v", name, err)
		}
	}
}

func filenames(files []render.File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Filename
	}
	return out
}

// TestEndToEnd_KubeRay drives the kuberay package through wizard → render
// and verifies the trickier bits a real package exercises: CRDs stay
// cluster-scoped, RBAC subjects pick up the target namespace, and the
// operator image is rewritten.
//
// Requires the kustomize binary on PATH; skipped if missing.
func TestEndToEnd_KubeRay(t *testing.T) {
	if _, err := exec.LookPath("kustomize"); err != nil {
		t.Skip("kustomize not on PATH")
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(repoRoot, "examples/kuberay")

	loaded, err := ipkg.Load(pkgDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tmp := t.TempDir()
	outDir := filepath.Join(tmp, "out")

	sel, inputs, err := wizard.Run(loaded.Package, wizard.RawAnswers{
		Inputs: map[string]string{
			"namespace":      "raysystem",
			"operator_image": "quay.io/kuberay/operator:v1.6.1",
		},
		SelectedComponents: []string{"sample-cluster"},
	}, outDir)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}

	result, err := render.Render(context.Background(), render.Options{
		Loaded:    loaded,
		Selection: sel,
		Inputs:    inputs,
	}, outDir)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// 4 CRDs + Namespace + ServiceAccount + ClusterRole + ClusterRoleBinding
	// + Role + RoleBinding + Deployment + Service + RayCluster = 13.
	if len(result.Files) != 13 {
		t.Fatalf("got %d files, want 13: %v", len(result.Files), filenames(result.Files))
	}

	manifests := filepath.Join(outDir, "manifests")

	// Cluster-scoped resources should NOT have a namespace prefix in their slug.
	for _, want := range []string{
		"customresourcedefinition-rayclusters-ray-io.yaml",
		"clusterrole-kuberay-operator.yaml",
		"clusterrolebinding-kuberay-operator.yaml",
		"namespace-raysystem.yaml",
	} {
		if _, err := os.Stat(filepath.Join(manifests, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}

	// ClusterRoleBinding subject namespace was rewritten.
	crb, err := os.ReadFile(filepath.Join(manifests, "clusterrolebinding-kuberay-operator.yaml"))
	if err != nil {
		t.Fatalf("read crb: %v", err)
	}
	if !strings.Contains(string(crb), "namespace: raysystem") {
		t.Errorf("ClusterRoleBinding subject namespace not rewritten:\n%s", crb)
	}

	// Operator deployment has the right image and namespace.
	dep, err := os.ReadFile(filepath.Join(manifests, "deployment-raysystem-kuberay-operator.yaml"))
	if err != nil {
		t.Fatalf("read deployment: %v", err)
	}
	got := string(dep)
	if !strings.Contains(got, "image: quay.io/kuberay/operator:v1.6.1") {
		t.Errorf("set-container-image did not apply:\n%s", got)
	}
	if !strings.Contains(got, "namespace: raysystem") {
		t.Errorf("deployment namespace not set:\n%s", got)
	}

	// Sample RayCluster picked up the namespace.
	rc, err := os.ReadFile(filepath.Join(manifests, "raycluster-raysystem-raycluster-kuberay.yaml"))
	if err != nil {
		t.Fatalf("read raycluster: %v", err)
	}
	if !strings.Contains(string(rc), "namespace: raysystem") {
		t.Errorf("RayCluster namespace not set:\n%s", rc)
	}

	// Selection solver and wizard agree (sanity).
	if got, _ := selection.Resolve(loaded.Package, "", []string{"sample-cluster"}); got.Spec.Base != sel.Spec.Base {
		t.Errorf("solver and wizard disagree on base")
	}
}
