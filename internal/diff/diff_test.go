package diff

import (
	"bytes"
	"context"
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

func TestFilterBookkeepingMutations(t *testing.T) {
	// Single bookkeeping-only mutation (the convergence-bug case):
	// the only diff after a successful merge-external-source apply is
	// the confighub.com/ResourceMergeID annotation that cub injected.
	bookkeepingOnly := `New changes from update from /tmp/x.yaml:
Resource: apps/v1/Deployment demo/hello
  - [Delete] metadata.annotations  (#4)
    confighub.com/ResourceMergeID: 31ad11e2-4838-4722-bbaa-7d04dea842e6
`
	got := filterBookkeepingMutations(bookkeepingOnly)
	if !isNoChange(got) {
		t.Errorf("expected no-change after filter, got:\n%s", got)
	}

	// Real change + bookkeeping noise: keep the real change, drop the
	// noise.
	mixed := `New changes from update from /tmp/x.yaml:
Resource: apps/v1/Deployment demo/hello
  ~ [Update] spec.replicas  (#5)
    1 →     5
  - [Delete] metadata.annotations  (#4)
    confighub.com/ResourceMergeID: 31ad11e2-4838-4722-bbaa-7d04dea842e6
`
	got = filterBookkeepingMutations(mixed)
	if isNoChange(got) {
		t.Errorf("expected real change to survive filter, got no-change")
	}
	if !strings.Contains(got, "[Update] spec.replicas") {
		t.Errorf("real Update mutation got dropped:\n%s", got)
	}
	if strings.Contains(got, "ResourceMergeID") {
		t.Errorf("bookkeeping mutation should have been dropped:\n%s", got)
	}

	// Empty-body mutation: the "value" is encoded in the header path
	// itself (e.g., adding a single label). Must NOT be dropped.
	emptyBody := `New changes from update from /tmp/x.yaml:
Resource: apps/v1/Deployment demo/hello-app
  + [Add] metadata.labels.installer-e2e-marker  (#3)
`
	got = filterBookkeepingMutations(emptyBody)
	if isNoChange(got) {
		t.Errorf("empty-body mutation got dropped (it's a real change):\n%s", got)
	}
	if !strings.Contains(got, "installer-e2e-marker") {
		t.Errorf("empty-body mutation should survive filter:\n%s", got)
	}

	// Two resources, one with only bookkeeping, one with a real diff:
	// only the second resource's header should survive.
	twoResources := `New changes from update from /tmp/x.yaml:
Resource: v1/ConfigMap demo/cm
  - [Delete] metadata.annotations  (#4)
    confighub.com/ResourceMergeID: aaa
Resource: apps/v1/Deployment demo/hello
  ~ [Update] spec.replicas  (#5)
    1 →     5
`
	got = filterBookkeepingMutations(twoResources)
	if strings.Contains(got, "ConfigMap") {
		t.Errorf("ConfigMap header should be dropped (only bookkeeping):\n%s", got)
	}
	if !strings.Contains(got, "Deployment") {
		t.Errorf("Deployment header should survive (has real change):\n%s", got)
	}
}

func TestIsNoChange(t *testing.T) {
	cases := map[string]bool{
		"":                                       true,
		"  \n":                                   true,
		"No new changes":                         true,
		"... No new changes from update from x.": true,
		// Preamble alone, no Resource: blocks → no change. Happens
		// after filterBookkeepingMutations strips a confighub.com-only
		// diff, leaving the header line behind.
		"New changes from update from x.yaml:": true,
		"New changes from update from x.yaml:\nResource: apps/v1/Deployment x/y\n  ~ [Update] spec.replicas (#3)": false,
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

func TestDescriptionOrDefault(t *testing.T) {
	cases := []struct {
		d, pkg, ver, want string
	}{
		{"", "hello", "0.1.0", "installer update from hello@0.1.0"},
		{"", "hello", "", "installer update from hello"},
		{"manual desc", "hello", "0.1.0", "manual desc"},
	}
	for _, tc := range cases {
		if got := descriptionOrDefault(tc.d, tc.pkg, tc.ver); got != tc.want {
			t.Errorf("(%q,%q,%q) = %q, want %q", tc.d, tc.pkg, tc.ver, got, tc.want)
		}
	}
}

func TestBaseName(t *testing.T) {
	cases := map[string]string{
		"/tmp/x/foo.yaml":            "foo.yaml",
		"foo.yaml":                   "foo.yaml",
		`C:\path\to\thing.yaml`:      "thing.yaml",
		"":                           "",
		"/trailing/":                 "",
	}
	for in, want := range cases {
		if got := baseName(in); got != want {
			t.Errorf("baseName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApplyNoChangesNoOp(t *testing.T) {
	// Apply on a plan with no changes must not invoke cub at all
	// (no ChangeSets opened, no commands run).
	var stdout, stderr bytes.Buffer
	res, err := Apply(context.Background(), Plan{Spaces: []SpacePlan{{SpaceSlug: "x"}}}, ApplyOptions{
		Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Created != 0 || res.Updated != 0 || res.Deleted != 0 {
		t.Errorf("expected zero counts, got %+v", res)
	}
	if len(res.ChangeSetsOpened) != 0 {
		t.Errorf("expected no ChangeSets, got %v", res.ChangeSetsOpened)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("expected no output, got stdout=%q stderr=%q", stdout.String(), stderr.String())
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
