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

	wres, err := wizard.Run(context.Background(), loaded.Package, wizard.RawAnswers{
		Namespace:          "demo",
		SelectedComponents: []string{"monitoring"},
	}, loaded.Root, outDir)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}
	sel, inputs := wres.Selection, wres.Inputs

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
	if len(result.Manifests) != 4 {
		t.Fatalf("got %d files, want 4: %v", len(result.Manifests), filenames(result.Manifests))
	}

	manifestsDir := filepath.Join(outDir, "manifests")
	// Slug includes namespace because set-namespace populates metadata.namespace.
	deploymentBytes, err := os.ReadFile(filepath.Join(manifestsDir, "deployment-demo-hello-app.yaml"))
	if err != nil {
		t.Fatalf("read deployment: %v", err)
	}
	got := string(deploymentBytes)
	// hello-app's chain only does set-namespace; the image is whatever the
	// base declares. Verify the base image survives and the namespace is set.
	if !strings.Contains(got, "image: nginxdemos/hello:plain-text") {
		t.Errorf("base image not preserved:\n%s", got)
	}
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

	wres, err := wizard.Run(context.Background(), loaded.Package, wizard.RawAnswers{
		Namespace:          "raysystem",
		SelectedComponents: []string{"sample-cluster"},
	}, loaded.Root, outDir)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}
	sel, inputs := wres.Selection, wres.Inputs

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
	if len(result.Manifests) != 13 {
		t.Fatalf("got %d files, want 13: %v", len(result.Manifests), filenames(result.Manifests))
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

// TestEndToEnd_GAIE drives the gaie package through wizard → render with the
// sample-pool component selected. Verifies the four CRDs stay cluster-scoped
// and that the sample custom resources land in the chosen namespace.
//
// Requires the kustomize binary on PATH; skipped if missing.
func TestEndToEnd_GAIE(t *testing.T) {
	if _, err := exec.LookPath("kustomize"); err != nil {
		t.Skip("kustomize not on PATH")
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(repoRoot, "examples/gaie")

	loaded, err := ipkg.Load(pkgDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tmp := t.TempDir()
	outDir := filepath.Join(tmp, "out")

	wres, err := wizard.Run(context.Background(), loaded.Package, wizard.RawAnswers{
		Namespace:          "inferdemo",
		SelectedComponents: []string{"sample-pool"},
	}, loaded.Root, outDir)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}
	sel, inputs := wres.Selection, wres.Inputs

	result, err := render.Render(context.Background(), render.Options{
		Loaded:    loaded,
		Selection: sel,
		Inputs:    inputs,
	}, outDir)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// 4 CRDs + Namespace + InferencePool + InferenceObjective = 7.
	if len(result.Manifests) != 7 {
		t.Fatalf("got %d files, want 7: %v", len(result.Manifests), filenames(result.Manifests))
	}

	manifests := filepath.Join(outDir, "manifests")

	// All four CRDs present and unprefixed (cluster-scoped).
	for _, want := range []string{
		"customresourcedefinition-inferencepools-inference-networking-k8s-io.yaml",
		"customresourcedefinition-inferenceobjectives-inference-networking-x-k8s-io.yaml",
		"customresourcedefinition-inferencemodelrewrites-inference-networking-x-k8s-io.yaml",
		"customresourcedefinition-inferencepoolimports-inference-networking-x-k8s-io.yaml",
		"namespace-inferdemo.yaml",
	} {
		if _, err := os.Stat(filepath.Join(manifests, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}

	// set-name renamed the Namespace resource.
	nsBytes, err := os.ReadFile(filepath.Join(manifests, "namespace-inferdemo.yaml"))
	if err != nil {
		t.Fatalf("read namespace: %v", err)
	}
	if !strings.Contains(string(nsBytes), "name: inferdemo") {
		t.Errorf("set-name did not rename Namespace:\n%s", nsBytes)
	}

	// InferencePool picked up the namespace.
	poolBytes, err := os.ReadFile(filepath.Join(manifests, "inferencepool-inferdemo-sample-pool.yaml"))
	if err != nil {
		t.Fatalf("read inferencepool: %v", err)
	}
	if !strings.Contains(string(poolBytes), "namespace: inferdemo") {
		t.Errorf("InferencePool namespace not set:\n%s", poolBytes)
	}
}
