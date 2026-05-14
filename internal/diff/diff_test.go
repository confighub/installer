package diff

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintNoChanges(t *testing.T) {
	var buf bytes.Buffer
	Print(&buf, Plan{Spaces: []SpacePlan{{SpaceSlug: "hello-app"}}})
	got := buf.String()
	if !strings.Contains(got, "No changes.") {
		t.Errorf("expected No changes header, got %q", got)
	}
}

func TestPrintWithChanges(t *testing.T) {
	plan := Plan{
		Spaces: []SpacePlan{
			{
				Package:   "hello-app",
				SpaceSlug: "hello-app",
				Adds: []SlugDiff{
					{Slug: "ingress-tls-cert", Path: "/tmp/x.yaml"},
				},
				Updates: []SlugDiff{
					{
						Slug:     "deployment-demo-hello",
						Path:     "/tmp/d.yaml",
						DiffText: "Resource: apps/v1/Deployment demo/hello\n  ~ [Update] spec.replicas\n    →     3",
					},
				},
				Deletes: []SlugDiff{
					{Slug: "configmap-old-thing"},
				},
				Images: []WorkloadImage{
					{Kind: "Deployment", Name: "hello", Container: "app", Image: "hello:v2"},
				},
			},
		},
	}
	var buf bytes.Buffer
	Print(&buf, plan)
	got := buf.String()

	wantSubstrs := []string{
		"Plan: 1 to add, 1 to change, 1 to delete.",
		"Space hello-app:",
		"+ ingress-tls-cert",
		"~ deployment-demo-hello",
		"      Resource: apps/v1/Deployment demo/hello",
		"      ~ [Update] spec.replicas",
		"- configmap-old-thing",
		"Images in hello-app (post-render):",
		"Deployment/hello [app] hello:v2",
	}
	for _, s := range wantSubstrs {
		if !strings.Contains(got, s) {
			t.Errorf("output missing %q\n--- got ---\n%s", s, got)
		}
	}
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[94mhello\x1b[0m \x1b[32mworld\x1b[0m"
	if got := stripANSI(in); got != "hello world" {
		t.Errorf("got %q", got)
	}
}

func TestIsNoChange(t *testing.T) {
	cases := map[string]bool{
		"":                                       true,
		"  \n":                                   true,
		"No new changes":                         true,
		"... No new changes from update from x.": true,
		"New changes from update from x.yaml:":   false,
	}
	for in, want := range cases {
		if got := isNoChange(in); got != want {
			t.Errorf("isNoChange(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestExtractImages(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "deployment.yaml"), `apiVersion: apps/v1
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
	mustWrite(t, filepath.Join(dir, "cronjob.yaml"), `apiVersion: batch/v1
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
	mustWrite(t, filepath.Join(dir, "configmap.yaml"), `apiVersion: v1
kind: ConfigMap
metadata: {name: cm}
data: {x: y}
`)
	mustWrite(t, filepath.Join(dir, "ignored.txt"), "not yaml")

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

func TestPlanCounts(t *testing.T) {
	p := Plan{
		Spaces: []SpacePlan{
			{Adds: []SlugDiff{{Slug: "a"}, {Slug: "b"}}, Updates: []SlugDiff{{Slug: "c"}}},
			{Deletes: []SlugDiff{{Slug: "d"}}},
		},
	}
	if !p.HasChanges() {
		t.Errorf("HasChanges should be true")
	}
	a, u, d := p.Counts()
	if a != 2 || u != 1 || d != 1 {
		t.Errorf("counts = (%d, %d, %d), want (2, 1, 1)", a, u, d)
	}
}

func TestEmptyPlanHasNoChanges(t *testing.T) {
	var p Plan
	if p.HasChanges() {
		t.Errorf("empty plan should have no changes")
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
