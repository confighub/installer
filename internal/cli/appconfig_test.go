package cli

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestTransformer_AppConfigAnnotationInjection_FileMode verifies the
// pre-pass infers appconfig-mode=file and the unique source-key for a
// ConfigMap with one data: file under installer.confighub.com/toolchain.
// No function group is declared; the transformer's only job here is the
// canonical-annotation pass.
func TestTransformer_AppConfigAnnotationInjection_FileMode(t *testing.T) {
	input := `apiVersion: config.kubernetes.io/v1
kind: ResourceList
items:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: app-config
      namespace: demo
      annotations:
        installer.confighub.com/toolchain: AppConfig/Properties
    data:
      application.properties: |
        a=1
        b=2
functionConfig:
  apiVersion: installer.confighub.com/v1alpha1
  kind: ConfigHubTransformers
  metadata: {name: noop}
  spec: {groups: []}
`
	out, err := runTransformer(context.Background(), []byte(input))
	if err != nil {
		t.Fatalf("runTransformer: %v", err)
	}
	cm := firstConfigMap(t, out)
	if cm.Metadata.Annotations[annoMode] != appConfigModeFile {
		t.Errorf("appconfig-mode: want %q, got %q", appConfigModeFile, cm.Metadata.Annotations[annoMode])
	}
	if cm.Metadata.Annotations[annoSourceKey] != "application.properties" {
		t.Errorf("appconfig-source-key: want application.properties, got %q",
			cm.Metadata.Annotations[annoSourceKey])
	}
}

// TestTransformer_AppConfigAnnotationInjection_MutableInferredFromName
// verifies the mutability inference: a stable name (no kustomize hash
// suffix) yields mutable=true, a hashed name yields mutable=false.
func TestTransformer_AppConfigAnnotationInjection_MutableInferredFromName(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cmName      string
		wantMutable string
	}{
		{name: "stable name → mutable", cmName: "app-config", wantMutable: "true"},
		{name: "kustomize-hashed name → immutable", cmName: "app-config-798k5k7g9f", wantMutable: "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := `apiVersion: config.kubernetes.io/v1
kind: ResourceList
items:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: ` + tc.cmName + `
      annotations:
        installer.confighub.com/toolchain: AppConfig/Properties
    data:
      application.properties: a=1
functionConfig:
  apiVersion: installer.confighub.com/v1alpha1
  kind: ConfigHubTransformers
  metadata: {name: noop}
  spec: {groups: []}
`
			out, err := runTransformer(context.Background(), []byte(input))
			if err != nil {
				t.Fatalf("runTransformer: %v", err)
			}
			cm := firstConfigMap(t, out)
			if cm.Metadata.Annotations[annoMutable] != tc.wantMutable {
				t.Errorf("appconfig-mutable: want %q, got %q",
					tc.wantMutable, cm.Metadata.Annotations[annoMutable])
			}
		})
	}
}

// TestTransformer_AppConfigAnnotationInjection_AuthorMutableOverride
// verifies the inference doesn't clobber an author-set value. A hashed
// name with appconfig-mutable=true should stay mutable=true.
func TestTransformer_AppConfigAnnotationInjection_AuthorMutableOverride(t *testing.T) {
	input := `apiVersion: config.kubernetes.io/v1
kind: ResourceList
items:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: app-config-798k5k7g9f
      annotations:
        installer.confighub.com/toolchain: AppConfig/Properties
        installer.confighub.com/appconfig-mutable: "true"
    data:
      application.properties: a=1
functionConfig:
  apiVersion: installer.confighub.com/v1alpha1
  kind: ConfigHubTransformers
  metadata: {name: noop}
  spec: {groups: []}
`
	out, err := runTransformer(context.Background(), []byte(input))
	if err != nil {
		t.Fatalf("runTransformer: %v", err)
	}
	cm := firstConfigMap(t, out)
	if cm.Metadata.Annotations[annoMutable] != "true" {
		t.Errorf("expected author override to win, got %q", cm.Metadata.Annotations[annoMutable])
	}
}

// TestTransformer_AppConfigAnnotationInjection_EnvMode verifies the
// pre-pass picks env mode for an AppConfig/Env ConfigMap whose data: holds
// env-shaped key/value pairs.
func TestTransformer_AppConfigAnnotationInjection_EnvMode(t *testing.T) {
	input := `apiVersion: config.kubernetes.io/v1
kind: ResourceList
items:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: app-env
      annotations:
        installer.confighub.com/toolchain: AppConfig/Env
    data:
      FOO: bar
      BAZ: qux
functionConfig:
  apiVersion: installer.confighub.com/v1alpha1
  kind: ConfigHubTransformers
  metadata: {name: noop}
  spec: {groups: []}
`
	out, err := runTransformer(context.Background(), []byte(input))
	if err != nil {
		t.Fatalf("runTransformer: %v", err)
	}
	cm := firstConfigMap(t, out)
	if cm.Metadata.Annotations[annoMode] != appConfigModeEnv {
		t.Errorf("appconfig-mode: want %q, got %q", appConfigModeEnv, cm.Metadata.Annotations[annoMode])
	}
	if _, present := cm.Metadata.Annotations[annoSourceKey]; present {
		t.Errorf("env mode should not carry a source-key annotation, got %q",
			cm.Metadata.Annotations[annoSourceKey])
	}
}

// TestTransformer_AppConfigAnnotationInjection_RejectsAmbiguous fails the
// pre-pass when file mode has multiple data keys and no explicit
// source-key annotation — there's no safe automatic pick.
func TestTransformer_AppConfigAnnotationInjection_RejectsAmbiguous(t *testing.T) {
	input := `apiVersion: config.kubernetes.io/v1
kind: ResourceList
items:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: app-config
      annotations:
        installer.confighub.com/toolchain: AppConfig/Properties
    data:
      first.properties: a=1
      second.properties: b=2
functionConfig:
  apiVersion: installer.confighub.com/v1alpha1
  kind: ConfigHubTransformers
  metadata: {name: noop}
  spec: {groups: []}
`
	_, err := runTransformer(context.Background(), []byte(input))
	if err == nil {
		t.Fatal("expected error for ambiguous file-mode ConfigMap")
	}
	if !strings.Contains(err.Error(), annoSourceKey) {
		t.Errorf("error should point at the missing source-key annotation: %v", err)
	}
}

// TestTransformer_AppConfigChainRoundTrip runs an AppConfig/Properties
// function (set-string-path) against a ConfigMap-carried .properties
// file and verifies the mutation lands inside data:.
func TestTransformer_AppConfigChainRoundTrip(t *testing.T) {
	input := `apiVersion: config.kubernetes.io/v1
kind: ResourceList
items:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: app-config
      namespace: demo
      annotations:
        installer.confighub.com/toolchain: AppConfig/Properties
    data:
      application.properties: |
        configHub.configSchema=AppProperties
        server.port=8080
        server.host=localhost
functionConfig:
  apiVersion: installer.confighub.com/v1alpha1
  kind: ConfigHubTransformers
  metadata: {name: chain}
  spec:
    groups:
      - toolchain: AppConfig/Properties
        invocations:
          - name: set-int-path
            args: ["AppProperties", "server.port", "9090"]
`
	out, err := runTransformer(context.Background(), []byte(input))
	if err != nil {
		t.Fatalf("runTransformer: %v", err)
	}
	cm := firstConfigMap(t, out)
	got := cm.Data["application.properties"]
	if !strings.Contains(got, "server.port=9090") {
		t.Errorf("expected server.port=9090 in mutated content, got:\n%s", got)
	}
	if !strings.Contains(got, "server.host=localhost") {
		t.Errorf("expected server.host=localhost preserved in mutated content, got:\n%s", got)
	}
}

// firstConfigMap unmarshals the output ResourceList and returns the first
// ConfigMap-shaped item, decoded into configMapShape so tests can inspect
// metadata.annotations and data: directly.
func firstConfigMap(t *testing.T, out []byte) configMapShape {
	t.Helper()
	var list resourceList
	if err := yaml.Unmarshal(out, &list); err != nil {
		t.Fatalf("parse output ResourceList: %v\n----\n%s\n----", err, out)
	}
	for _, item := range list.Items {
		var shape configMapShape
		if err := item.Decode(&shape); err != nil {
			continue
		}
		if shape.Kind == "ConfigMap" {
			return shape
		}
	}
	t.Fatalf("no ConfigMap in output:\n%s", out)
	return configMapShape{}
}
