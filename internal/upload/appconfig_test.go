package upload

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestDetectAppConfigManifest_FileMode verifies the detector picks up a
// rendered ConfigMap with the file-mode annotations the transformer's
// pre-pass injects, and extracts the raw file body verbatim.
func TestDetectAppConfigManifest_FileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cm.yaml")
	writeFile(t, path, `apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: demo
  annotations:
    installer.confighub.com/toolchain: AppConfig/Properties
    installer.confighub.com/appconfig-mode: file
    installer.confighub.com/appconfig-source-key: application.properties
data:
  application.properties: |
    a=1
    b=2
`)
	got, err := DetectAppConfigManifest(path)
	if err != nil {
		t.Fatalf("DetectAppConfigManifest: %v", err)
	}
	if got == nil {
		t.Fatal("expected detection, got nil")
	}
	if got.Toolchain != "AppConfig/Properties" {
		t.Errorf("Toolchain: want AppConfig/Properties, got %q", got.Toolchain)
	}
	if got.Mode != "file" {
		t.Errorf("Mode: want file, got %q", got.Mode)
	}
	if got.SourceKey != "application.properties" {
		t.Errorf("SourceKey: want application.properties, got %q", got.SourceKey)
	}
	wantContent := "a=1\nb=2\n"
	if string(got.Content) != wantContent {
		t.Errorf("Content: want %q, got %q", wantContent, string(got.Content))
	}
	if got.UnitSlug() != "app-config-appconfig" {
		t.Errorf("UnitSlug: want app-config-appconfig, got %q", got.UnitSlug())
	}
	if got.TargetSlug() != "app-config-renderer" {
		t.Errorf("TargetSlug: want app-config-renderer, got %q", got.TargetSlug())
	}
	if len(got.RendererOptions()) != 0 {
		t.Errorf("file-mode non-Env toolchain should not set AsKeyValue, got %v", got.RendererOptions())
	}
}

// TestDetectAppConfigManifest_MutableTriggersRevisionHistoryLimit
// verifies that appconfig-mutable=true on the rendered ConfigMap maps
// to RevisionHistoryLimit=0 on the renderer Target.
func TestDetectAppConfigManifest_MutableTriggersRevisionHistoryLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cm.yaml")
	writeFile(t, path, `apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  annotations:
    installer.confighub.com/toolchain: AppConfig/Properties
    installer.confighub.com/appconfig-mode: file
    installer.confighub.com/appconfig-source-key: application.properties
    installer.confighub.com/appconfig-mutable: "true"
data:
  application.properties: x=1
`)
	got, err := DetectAppConfigManifest(path)
	if err != nil {
		t.Fatalf("DetectAppConfigManifest: %v", err)
	}
	if !got.Mutable {
		t.Errorf("Mutable: want true, got false")
	}
	opts := got.RendererOptions()
	hasRev := false
	for _, o := range opts {
		if o == "RevisionHistoryLimit=0" {
			hasRev = true
		}
	}
	if !hasRev {
		t.Errorf("mutable=true should include RevisionHistoryLimit=0; got %v", opts)
	}
}

// TestDetectAppConfigManifest_ImmutableSkipsRevisionHistoryLimit
// verifies that the immutable case (the kustomize default) leaves
// RevisionHistoryLimit unset so the bridge default applies.
func TestDetectAppConfigManifest_ImmutableSkipsRevisionHistoryLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cm.yaml")
	writeFile(t, path, `apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config-798k5k7g9f
  annotations:
    installer.confighub.com/toolchain: AppConfig/Properties
    installer.confighub.com/appconfig-mode: file
    installer.confighub.com/appconfig-source-key: application.properties
    installer.confighub.com/appconfig-mutable: "false"
data:
  application.properties: x=1
`)
	got, err := DetectAppConfigManifest(path)
	if err != nil {
		t.Fatalf("DetectAppConfigManifest: %v", err)
	}
	if got.Mutable {
		t.Errorf("Mutable: want false, got true")
	}
	for _, o := range got.RendererOptions() {
		if strings.HasPrefix(o, "RevisionHistoryLimit") {
			t.Errorf("immutable case must not set RevisionHistoryLimit; got %v", got.RendererOptions())
		}
	}
}

// TestDetectAppConfigManifest_EnvKeyValueOption verifies env-mode +
// AppConfig/Env triggers the AsKeyValue=true Target option, and the
// content is reconstructed as a deterministic .env-shaped doc.
func TestDetectAppConfigManifest_EnvKeyValueOption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cm.yaml")
	writeFile(t, path, `apiVersion: v1
kind: ConfigMap
metadata:
  name: app-env
  annotations:
    installer.confighub.com/toolchain: AppConfig/Env
    installer.confighub.com/appconfig-mode: env
data:
  FOO: bar
  BAZ: qux
`)
	got, err := DetectAppConfigManifest(path)
	if err != nil {
		t.Fatalf("DetectAppConfigManifest: %v", err)
	}
	if got == nil {
		t.Fatal("expected detection, got nil")
	}
	if got.Mode != "env" {
		t.Errorf("Mode: want env, got %q", got.Mode)
	}
	opts := got.RendererOptions()
	if len(opts) != 1 || opts[0] != "AsKeyValue=true" {
		t.Errorf("RendererOptions: want [AsKeyValue=true], got %v", opts)
	}
	// Env content is rendered with sorted keys for determinism: BAZ before FOO.
	wantContent := "BAZ=qux\nFOO=bar\n"
	if string(got.Content) != wantContent {
		t.Errorf("Content: want %q, got %q", wantContent, string(got.Content))
	}
}

// TestDetectAppConfigManifest_SkipsUnannotated returns nil for ConfigMaps
// that don't carry the toolchain annotation. The normal Kubernetes/YAML
// upload path handles those.
func TestDetectAppConfigManifest_SkipsUnannotated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cm.yaml")
	writeFile(t, path, `apiVersion: v1
kind: ConfigMap
metadata:
  name: plain
data:
  foo: bar
`)
	got, err := DetectAppConfigManifest(path)
	if err != nil {
		t.Fatalf("DetectAppConfigManifest: %v", err)
	}
	if got != nil {
		t.Errorf("expected no detection for un-annotated ConfigMap, got %+v", got)
	}
}

// TestDetectAppConfigManifest_SkipsNonConfigMap returns nil for non-ConfigMap
// resources even if they happen to carry an installer annotation (shouldn't
// happen in practice, but we don't want to fail loudly on it).
func TestDetectAppConfigManifest_SkipsNonConfigMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deployment.yaml")
	writeFile(t, path, `apiVersion: apps/v1
kind: Deployment
metadata:
  name: smoke
  annotations:
    installer.confighub.com/toolchain: AppConfig/Properties
spec:
  replicas: 1
`)
	got, err := DetectAppConfigManifest(path)
	if err != nil {
		t.Fatalf("DetectAppConfigManifest: %v", err)
	}
	if got != nil {
		t.Errorf("expected no detection for Deployment, got %+v", got)
	}
}

// TestDetectAppConfigManifest_RejectsMissingMode catches the case where
// the upload step runs against output from an older installer that didn't
// inject the mode annotation. Error message should point at re-rendering.
func TestDetectAppConfigManifest_RejectsMissingMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cm.yaml")
	writeFile(t, path, `apiVersion: v1
kind: ConfigMap
metadata:
  name: app
  annotations:
    installer.confighub.com/toolchain: AppConfig/Properties
data:
  app.properties: x=1
`)
	_, err := DetectAppConfigManifest(path)
	if err == nil {
		t.Fatal("expected error for missing appconfig-mode annotation")
	}
	if !strings.Contains(err.Error(), "appconfig-mode") {
		t.Errorf("error should name the missing annotation: %v", err)
	}
	if !strings.Contains(err.Error(), "installer render") {
		t.Errorf("error should suggest re-rendering: %v", err)
	}
}

// writeFile (defined in upload_test.go, same package) is reused here.
