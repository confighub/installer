// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package upload

import (
	"path/filepath"
	"testing"
)

func TestExtractImages(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata: {name: hello}
spec:
  template:
    spec:
      initContainers:
        - name: setup
          image: busybox:1.36
      containers:
        - name: app
          image: hello:v1
        - name: sidecar
          image: sidecar:1.4
`)
	writeFile(t, filepath.Join(dir, "cronjob.yaml"), `apiVersion: batch/v1
kind: CronJob
metadata: {name: nightly}
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: runner
              image: runner:2
`)
	writeFile(t, filepath.Join(dir, "configmap.yaml"), `apiVersion: v1
kind: ConfigMap
metadata: {name: cm}
data: {x: y}
`)
	writeFile(t, filepath.Join(dir, "ignored.txt"), "not yaml")

	imgs, err := ExtractImages(dir)
	if err != nil {
		t.Fatalf("ExtractImages: %v", err)
	}
	// Sorted: CronJob/nightly[runner], Deployment/hello init[setup],
	// Deployment/hello[app], Deployment/hello[sidecar].
	want := []WorkloadImage{
		{Kind: "CronJob", Name: "nightly", Container: "runner", Image: "runner:2"},
		{Kind: "Deployment", Name: "hello", Container: "setup", Image: "busybox:1.36", Init: true},
		{Kind: "Deployment", Name: "hello", Container: "app", Image: "hello:v1"},
		{Kind: "Deployment", Name: "hello", Container: "sidecar", Image: "sidecar:1.4"},
	}
	if len(imgs) != len(want) {
		t.Fatalf("got %d images, want %d: %+v", len(imgs), len(want), imgs)
	}
	for i := range want {
		if imgs[i] != want[i] {
			t.Errorf("imgs[%d] = %+v\nwant      %+v", i, imgs[i], want[i])
		}
	}
}

func TestExtractImagesMissingDir(t *testing.T) {
	imgs, err := ExtractImages(filepath.Join(t.TempDir(), "doesnotexist"))
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(imgs) != 0 {
		t.Errorf("expected empty, got %v", imgs)
	}
}
