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

	"gopkg.in/yaml.v3"

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

// TestEndToEnd_HashedConfigMaps renders AppConfig carried in ConfigMaps
// from two configMapGenerators, one that hashes its ConfigMap's name and
// one with disableNameSuffixHash. kustomize runs the transformer before it
// appends the hash, so this is the only test that sees what the transformer
// is actually given. The hashed ConfigMap is immutable and records its
// name before the hash; the other is mutable and records nothing. The
// rendered output is kustomize's: the hashed name, and references to it
// rewritten to match.
func TestEndToEnd_HashedConfigMaps(t *testing.T) {
	pkgDir := t.TempDir()
	writeFile(t, filepath.Join(pkgDir, "installer.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata:
  name: hashed
installerMetadata:
  version: 0.0.1
spec:
  bases:
    - name: default
      path: bases/default
      default: true
`)
	writeFile(t, filepath.Join(pkgDir, "bases/default/kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
configMapGenerator:
  - name: app-env
    envs:
      - app.env
    options:
      annotations:
        confighub.com/toolchain: AppConfig/Env
  - name: app-props
    files:
      - app.properties
    options:
      disableNameSuffixHash: true
      annotations:
        confighub.com/toolchain: AppConfig/Properties
`)
	writeFile(t, filepath.Join(pkgDir, "bases/default/app.env"), "GREETING=hello\n")
	writeFile(t, filepath.Join(pkgDir, "bases/default/app.properties"), "port=5432\n")
	writeFile(t, filepath.Join(pkgDir, "bases/default/deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  selector:
    matchLabels:
      app: app
  template:
    metadata:
      labels:
        app: app
    spec:
      containers:
        - name: app
          image: registry.example.com/app:1.0.0
          envFrom:
            - configMapRef:
                name: app-env
          volumeMounts:
            - name: props
              mountPath: /etc/app
      volumes:
        - name: props
          configMap:
            name: app-props
`)

	loaded, err := ipkg.Load(pkgDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	outDir := filepath.Join(t.TempDir(), "out")
	wres, err := wizard.Run(context.Background(), loaded.Package, wizard.RawAnswers{Namespace: "demo"}, loaded.Root, outDir)
	if err != nil {
		t.Fatalf("wizard.Run: %v", err)
	}
	result, err := render.Render(context.Background(), render.Options{TransformerBinary: installerBin,
		Loaded:    loaded,
		Selection: wres.Selection,
		Inputs:    wres.Inputs,
	}, outDir)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	type configMap struct {
		Metadata struct {
			Name        string            `yaml:"name"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"metadata"`
	}
	var hashed, stable *configMap
	var deployment string
	for _, f := range result.Manifests {
		switch f.Kind {
		case "ConfigMap":
			var cm configMap
			if err := yaml.Unmarshal(f.Body, &cm); err != nil {
				t.Fatalf("parse %s: %v", f.Filename, err)
			}
			switch {
			case strings.HasPrefix(cm.Metadata.Name, "app-env-"):
				hashed = &cm
			case cm.Metadata.Name == "app-props":
				stable = &cm
			}
		case "Deployment":
			deployment = string(f.Body)
		}
	}
	if hashed == nil || stable == nil {
		t.Fatalf("want ConfigMaps app-env-<hash> and app-props, got %v", filenames(result.Manifests))
	}

	if got := hashed.Metadata.Annotations["confighub.com/appconfig-mutable"]; got != "false" {
		t.Errorf("a ConfigMap whose name kustomize hashes should be immutable, got appconfig-mutable %q", got)
	}
	if got := hashed.Metadata.Annotations["confighub.com/appconfig-name"]; got != "app-env" {
		t.Errorf("a hashed ConfigMap should record its name before the hash, got appconfig-name %q", got)
	}
	if got := stable.Metadata.Annotations["confighub.com/appconfig-mutable"]; got != "true" {
		t.Errorf("a ConfigMap under a stable name should be mutable, got appconfig-mutable %q", got)
	}
	if got, ok := stable.Metadata.Annotations["confighub.com/appconfig-name"]; ok {
		t.Errorf("a ConfigMap under a stable name should record no other name, got appconfig-name %q", got)
	}
	if _, ok := hashed.Metadata.Annotations["internal.config.kubernetes.io/needsHashSuffix"]; ok {
		t.Errorf("kustomize's build annotation should not reach the output")
	}
	if !strings.Contains(deployment, "name: "+hashed.Metadata.Name) {
		t.Errorf("the Deployment should refer to the hashed name %s, as kustomize rewrote it:\n%s", hashed.Metadata.Name, deployment)
	}

	// A content change gives the ConfigMap a new hashed name. A re-render
	// replaces the file under the old name rather than leaving it beside the
	// new one, and keeps a Kptfile it didn't write.
	manifestsDir := filepath.Join(outDir, "manifests")
	writeFile(t, filepath.Join(manifestsDir, "Kptfile"), "apiVersion: kpt.dev/v1\nkind: Kptfile\n")
	writeFile(t, filepath.Join(pkgDir, "bases/default/app.env"), "GREETING=bye\n")
	if _, err := render.Render(context.Background(), render.Options{TransformerBinary: installerBin,
		Loaded:    loaded,
		Selection: wres.Selection,
		Inputs:    wres.Inputs,
	}, outDir); err != nil {
		t.Fatalf("Render after the content change: %v", err)
	}
	entries, err := os.ReadDir(manifestsDir)
	if err != nil {
		t.Fatal(err)
	}
	var hashedFiles []string
	kptfile := false
	for _, e := range entries {
		switch {
		case strings.Contains(e.Name(), "-app-env-"):
			hashedFiles = append(hashedFiles, e.Name())
		case e.Name() == "Kptfile":
			kptfile = true
		}
	}
	if len(hashedFiles) != 1 || strings.Contains(hashedFiles[0], hashed.Metadata.Name) {
		t.Errorf("want one app-env ConfigMap, under a new hashed name rather than %s, got %v", hashed.Metadata.Name, hashedFiles)
	}
	if !kptfile {
		t.Errorf("a re-render should keep the Kptfile in out/manifests/")
	}
}
