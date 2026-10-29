// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package render_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/confighub/installer/internal/kustomize"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/render"
	"github.com/confighub/installer/internal/selection"
	"github.com/confighub/installer/internal/wizard"
	"github.com/confighub/installer/pkg/api"
)

// TestEndToEnd_HelloApp drives the example package through wizard → render
// and checks the rendered output looks correct.
func TestEndToEnd_HelloApp(t *testing.T) {
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

	result, err := render.Render(context.Background(), render.Options{TransformerBinary: installerBin,
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
		if _, err := os.Stat(filepath.Join(outDir, api.RecordDir, name)); err != nil {
			t.Errorf("missing spec doc %s: %v", name, err)
		}
	}

	// out/.gitignore keeps rendered secrets out of version control.
	gi, err := os.ReadFile(filepath.Join(outDir, ".gitignore"))
	if err != nil {
		t.Fatalf("read out/.gitignore: %v", err)
	}
	if !strings.Contains(string(gi), "secrets/") {
		t.Errorf("out/.gitignore should ignore the secrets/ subdirectory, got:\n%s", gi)
	}
}

// TestComposeBuildsWithKustomizeBinary checks that out/compose/ still
// reproduces the render with a standalone kustomize, which runs the
// installer transformer as an exec plugin through the wrapper script. Render
// itself builds with kustomize's Go API, so only this test exercises the
// binary. Skipped when kustomize is not on PATH.
func TestComposeBuildsWithKustomizeBinary(t *testing.T) {
	if _, err := exec.LookPath("kustomize"); err != nil {
		t.Skip("kustomize not on PATH")
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := ipkg.Load(filepath.Join(repoRoot, "examples/hello-app"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	outDir := filepath.Join(t.TempDir(), "out")
	wres, err := wizard.Run(context.Background(), loaded.Package, wizard.RawAnswers{
		Namespace:          "demo",
		SelectedComponents: []string{"monitoring"},
	}, loaded.Root, outDir)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}
	if _, err := render.Render(context.Background(), render.Options{TransformerBinary: installerBin,
		Loaded:    loaded,
		Selection: wres.Selection,
		Inputs:    wres.Inputs,
	}, outDir); err != nil {
		t.Fatalf("Render: %v", err)
	}

	composeDir := filepath.Join(outDir, "compose")
	cmd := exec.Command("kustomize", "build", "--enable-exec", "--enable-alpha-plugins", composeDir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("kustomize build %s: %v\n%s", composeDir, err, stderr.String())
	}
	got := string(out)
	// hello-app's base has no namespace: fields and a Namespace named
	// hello-app; only the transformer's set-namespace changes either.
	if !strings.Contains(got, "kind: Deployment") || !strings.Contains(got, "namespace: demo") {
		t.Errorf("the transformer's set-namespace did not apply:\n%s", got)
	}
	if !strings.Contains(got, "kind: Namespace\nmetadata:\n  name: demo\n") {
		t.Errorf("the transformer did not rename the Namespace to demo:\n%s", got)
	}

	// A kustomize release other than the one built in may build
	// differently, so a mismatch is reported but does not fail the test.
	builtIn, err := kustomize.BuildYAML(composeDir)
	if err != nil {
		t.Fatalf("built-in kustomize build: %v", err)
	}
	if !bytes.Equal(out, builtIn) {
		version, _ := exec.Command("kustomize", "version").Output()
		t.Logf("kustomize %s builds out/compose/ differently from the built-in kustomize", strings.TrimSpace(string(version)))
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
func TestEndToEnd_KubeRay(t *testing.T) {
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

	result, err := render.Render(context.Background(), render.Options{TransformerBinary: installerBin,
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
func TestEndToEnd_GAIE(t *testing.T) {
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

	result, err := render.Render(context.Background(), render.Options{TransformerBinary: installerBin,
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
