// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// decodeDocs splits a multi-document YAML stream into generic maps.
func decodeDocs(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	var docs []map[string]any
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("decode: %v", err)
		}
		docs = append(docs, doc)
	}
	return docs
}

// lookup walks a path of map keys through a decoded document.
func lookup(t *testing.T, doc map[string]any, path ...string) any {
	t.Helper()
	var cur any = doc
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%s: %v is not a map", strings.Join(path, "."), cur)
		}
		cur = m[key]
	}
	return cur
}

const nameLabel = "app.kubernetes.io/name"

func TestCustomizeManifestRenamesRecommendedLabels(t *testing.T) {
	body := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: hello-app
  namespace: kubernetes-resources
  labels:
    app.kubernetes.io/name: hello-app
  annotations:
    confighub.com/ResourceMergeID: abc
spec:
  replicas: 2
  selector:
    matchLabels:
      app.kubernetes.io/name: hello-app
  template:
    metadata:
      labels:
        app.kubernetes.io/name: hello-app
    spec:
      serviceAccountName: hello-app
      containers:
        - name: main
          image: ghcr.io/acme/hello-app:v0.1.0
          ports:
            - containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: hello-app
  namespace: kubernetes-resources
  labels:
    app.kubernetes.io/name: hello-app
spec:
  selector:
    app.kubernetes.io/name: hello-app
  ports:
    - port: 80
      targetPort: 8080
`)
	out, err := customizeManifest(body, customizeOpts{
		origName: "hello-app",
		newName:  "statusboard",
		image:    "nginxdemos/hello:plain-text",
		port:     80,
		replicas: 3,
	})
	if err != nil {
		t.Fatalf("customizeManifest: %v", err)
	}
	docs := decodeDocs(t, out)
	if len(docs) != 2 {
		t.Fatalf("got %d documents, want 2:\n%s", len(docs), out)
	}
	deploy, svc := docs[0], docs[1]

	for _, c := range []struct {
		doc  map[string]any
		path []string
	}{
		{deploy, []string{"metadata", "labels", nameLabel}},
		{deploy, []string{"spec", "selector", "matchLabels", nameLabel}},
		{deploy, []string{"spec", "template", "metadata", "labels", nameLabel}},
		{deploy, []string{"spec", "template", "spec", "serviceAccountName"}},
		{deploy, []string{"metadata", "name"}},
		{svc, []string{"metadata", "name"}},
		{svc, []string{"metadata", "labels", nameLabel}},
		{svc, []string{"spec", "selector", nameLabel}},
	} {
		if got := lookup(t, c.doc, c.path...); got != "statusboard" {
			t.Errorf("%s %s = %v, want statusboard", c.doc["kind"], strings.Join(c.path, "."), got)
		}
	}
	for _, doc := range docs {
		if got := lookup(t, doc, "metadata", "namespace"); got != "confighubplaceholder" {
			t.Errorf("%s metadata.namespace = %v, want confighubplaceholder", doc["kind"], got)
		}
	}
	if got := lookup(t, deploy, "metadata", "annotations"); got != nil {
		t.Errorf("confighub.com annotations not stripped: %v", got)
	}
	if got := lookup(t, deploy, "spec", "replicas"); got != 3 {
		t.Errorf("replicas = %v, want 3", got)
	}
	containers := lookup(t, deploy, "spec", "template", "spec", "containers").([]any)
	container := containers[0].(map[string]any)
	if container["image"] != "nginxdemos/hello:plain-text" {
		t.Errorf("image = %v", container["image"])
	}
	if port := container["ports"].([]any)[0].(map[string]any)["containerPort"]; port != 80 {
		t.Errorf("containerPort = %v, want 80", port)
	}
	if port := lookup(t, svc, "spec", "ports").([]any)[0].(map[string]any)["port"]; port != 80 {
		t.Errorf("service port = %v, want 80", port)
	}
}

func TestCustomizeManifestRenamesLegacyAppLabel(t *testing.T) {
	// A kubernetes-resources Space uploaded from an earlier version of
	// the package still carries the bare app label.
	body := []byte(`apiVersion: v1
kind: Service
metadata:
  name: hello-app
  labels:
    app: hello-app
spec:
  selector:
    app: hello-app
  ports:
    - port: 80
`)
	out, err := customizeManifest(body, customizeOpts{origName: "hello-app", newName: "web"})
	if err != nil {
		t.Fatalf("customizeManifest: %v", err)
	}
	doc := decodeDocs(t, out)[0]
	if got := lookup(t, doc, "metadata", "labels", "app"); got != "web" {
		t.Errorf("metadata.labels.app = %v, want web", got)
	}
	if got := lookup(t, doc, "spec", "selector", "app"); got != "web" {
		t.Errorf("spec.selector.app = %v, want web", got)
	}
}

func TestCustomizeManifestRenamesNetworkPolicyPodSelector(t *testing.T) {
	body := []byte(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-hello-app
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: allow-hello-app
`)
	out, err := customizeManifest(body, customizeOpts{origName: "allow-hello-app", newName: "allow-web"})
	if err != nil {
		t.Fatalf("customizeManifest: %v", err)
	}
	doc := decodeDocs(t, out)[0]
	if got := lookup(t, doc, "spec", "podSelector", "matchLabels", nameLabel); got != "allow-web" {
		t.Errorf("podSelector = %v, want allow-web", got)
	}
}

func TestCustomizeManifestLeavesOtherLabelsAlone(t *testing.T) {
	body := []byte(`apiVersion: v1
kind: Service
metadata:
  name: hello-app
spec:
  selector:
    app.kubernetes.io/name: hello-db
    app.kubernetes.io/component: database
`)
	out, err := customizeManifest(body, customizeOpts{origName: "hello-app", newName: "web"})
	if err != nil {
		t.Fatalf("customizeManifest: %v", err)
	}
	doc := decodeDocs(t, out)[0]
	if got := lookup(t, doc, "spec", "selector", nameLabel); got != "hello-db" {
		t.Errorf("selector name label = %v, want hello-db unchanged", got)
	}
	if got := lookup(t, doc, "spec", "selector", "app.kubernetes.io/component"); got != "database" {
		t.Errorf("component label = %v, want database unchanged", got)
	}
}
