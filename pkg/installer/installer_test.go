// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/confighub/sdk/core/cubapi"

	"github.com/confighub/installer/internal/fakehub"
	"github.com/confighub/installer/pkg/installer"
)

func TestMain(m *testing.M) {
	// Render re-executes this binary for kustomize's transform step.
	installer.RunTransformerIfRequested()
	home, err := os.MkdirTemp("", "installer-pkg-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Nothing reads the user's signing policy or cub configuration.
	os.Setenv("HOME", home)
	for _, k := range []string{"CUB_SERVER", "CUB_TOKEN", "CUB_CONFIG", "CUB_CONTEXT", "CUB_PLUGIN",
		"INSTALLER_SIGNING_POLICY", "INSTALLER_COSIGN_BIN", "ORAS_OCI_INSECURE"} {
		os.Unsetenv(k)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// fixture is an Installer talking to a fake ConfigHub, with its output.
type fixture struct {
	hub      *fakehub.Server
	inst     *installer.Installer
	out, err bytes.Buffer
	warnings []error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{hub: fakehub.New()}
	t.Cleanup(f.hub.Close)
	client, err := cubapi.NewClient(cubapi.ClientOptions{ServerURL: f.hub.URL, Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	f.inst, err = installer.New(installer.Options{
		ConfigHub: client,
		Out:       &f.out,
		ErrOut:    &f.err,
		Warn:      func(err error) { f.warnings = append(f.warnings, err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func example(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// A program sets up, plans and uploads an install, and re-enters it from
// ConfigHub, getting what each step chose and did back as values.
func TestSetupPlanUploadReenter(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	work := t.TempDir()

	res, err := f.inst.Setup(ctx, installer.SetupOptions{
		WorkDir:   work,
		Pull:      example(t, "hello-app"),
		Namespace: "demo",
		Select:    []string{"monitoring"},
		Inputs:    map[string]string{},
		Render:    true,
	})
	if err != nil {
		t.Fatalf("Setup: %v\n%s", err, f.err.String())
	}
	if res.PriorSource != installer.PriorNone {
		t.Errorf("PriorSource = %q, want none", res.PriorSource)
	}
	if res.Package.Metadata.Name != "hello-app" {
		t.Errorf("Package = %q", res.Package.Metadata.Name)
	}
	if got := res.Selection.Spec.Components; !slices.Equal(got, []string{"monitoring"}) {
		t.Errorf("components = %v", got)
	}
	if res.Inputs.Spec.Namespace != "demo" {
		t.Errorf("namespace = %q", res.Inputs.Spec.Namespace)
	}
	if res.Facts != nil || res.Lock != nil || res.RenderedOCI != nil {
		t.Errorf("Facts, Lock, RenderedOCI = %v, %v, %v; want nil", res.Facts, res.Lock, res.RenderedOCI)
	}
	if res.Render == nil || len(res.Render.Manifests) != 4 {
		t.Fatalf("Render = %+v, want 4 manifests", res.Render)
	}
	for _, m := range res.Render.Manifests {
		if _, err := os.Stat(m); err != nil {
			t.Errorf("rendered manifest: %v", err)
		}
	}
	if !strings.Contains(f.out.String(), "Rendered 4 manifest(s)") {
		t.Errorf("progress lacks the render:\n%s", f.out.String())
	}
	// What to run next is the CLI's to say.
	if strings.Contains(f.out.String(), "Next:") {
		t.Errorf("progress names a next command:\n%s", f.out.String())
	}

	plan, err := f.inst.PlanUpload(ctx, installer.UploadOptions{WorkDir: work, Space: "hello"})
	if err != nil {
		t.Fatalf("PlanUpload: %v", err)
	}
	if len(plan.Packages) != 1 || plan.Packages[0].Space != "hello" || plan.Packages[0].Preview == nil {
		t.Fatalf("plan = %+v", plan.Packages)
	}
	if emptied := plan.Emptied(); len(emptied) != 0 {
		t.Errorf("Emptied = %v", emptied)
	}
	if f.hub.Space("hello") != nil {
		t.Fatal("PlanUpload wrote to ConfigHub")
	}

	uploaded, err := f.inst.Upload(ctx, plan)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !uploaded.Changed() || uploaded.Failed() {
		t.Errorf("Changed, Failed = %v, %v; want true, false", uploaded.Changed(), uploaded.Failed())
	}
	pkg := uploaded.Packages[0]
	if !pkg.IsParent || pkg.Space != "hello" || len(pkg.ChangeSets) != 1 || pkg.ChangeSets[0].Space != "hello" {
		t.Errorf("uploaded package = %+v", pkg)
	}
	space := f.hub.Space("hello")
	if space == nil || space.Labels["Component"] != "hello-app" || space.Labels["Variant"] != "base" {
		t.Fatalf("Space hello = %+v", space)
	}
	for _, u := range f.hub.Units("hello") {
		if u.Slug == "installer" && u.HiddenReason == "" {
			t.Error("the installer record Unit is not hidden")
		}
	}

	// Uploading the same render again changes nothing.
	plan, err = f.inst.PrepareUpload(ctx, installer.UploadOptions{WorkDir: work, Space: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if again, err := f.inst.Upload(ctx, plan); err != nil || again.Changed() {
		t.Errorf("second Upload: changed=%v err=%v", again != nil && again.Changed(), err)
	}

	// Another work-dir re-enters the install from its installer record alone.
	res, err = f.inst.Setup(ctx, installer.SetupOptions{WorkDir: t.TempDir(), Space: "hello"})
	if err != nil {
		t.Fatalf("re-entering Setup: %v", err)
	}
	if res.PriorSource != installer.PriorConfigHub {
		t.Errorf("PriorSource = %q, want confighub", res.PriorSource)
	}
	if got := res.Selection.Spec.Components; !slices.Equal(got, []string{"monitoring"}) || res.Inputs.Spec.Namespace != "demo" {
		t.Errorf("re-entered components %v, namespace %q", got, res.Inputs.Spec.Namespace)
	}
	if res.Render != nil {
		t.Error("Setup rendered without Render")
	}
	if len(f.warnings) != 0 {
		t.Errorf("warnings: %v", f.warnings)
	}
}

// The failures a program remedies through its own options are sentinel errors.
func TestSetupSentinelErrors(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	hello := example(t, "hello-app")

	cases := []struct {
		name string
		opts installer.SetupOptions
		want error
	}{
		{"no package", installer.SetupOptions{}, installer.ErrNoPackage},
		{"no namespace", installer.SetupOptions{Pull: hello}, installer.ErrNamespaceRequired},
		{"nothing to reuse", installer.SetupOptions{Pull: hello, Reuse: true}, installer.ErrNoPriorInstall},
		{"preset and select", installer.SetupOptions{Pull: hello, Namespace: "x", Preset: "all", Select: []string{"monitoring"}}, installer.ErrPresetAndSelect},
		{"no record to re-enter", installer.SetupOptions{Space: "nowhere"}, installer.ErrNoInstallerRecord},
		{"rendered OCI without rendering", installer.SetupOptions{Pull: hello, Namespace: "x", OutputOCI: "rendered.oci"}, installer.ErrOutputOCIWithoutRender},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.WorkDir = t.TempDir()
			_, err := f.inst.Setup(ctx, tc.opts)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("upgrade adds a required input", func(t *testing.T) {
		work := t.TempDir()
		if _, err := f.inst.Setup(ctx, installer.SetupOptions{WorkDir: work, Pull: hello, Namespace: "x"}); err != nil {
			t.Fatal(err)
		}
		pkg := filepath.Join(work, "package", "installer.yaml")
		data, err := os.ReadFile(pkg)
		if err != nil {
			t.Fatal(err)
		}
		withInput := strings.Replace(string(data), "  components:\n", "  inputs:\n    - name: owner\n      type: string\n      required: true\n\n  components:\n", 1)
		if err := os.WriteFile(pkg, []byte(withInput), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = f.inst.Setup(ctx, installer.SetupOptions{WorkDir: work})
		var newRequired *installer.NewRequiredInputsError
		if !errors.Is(err, installer.ErrNewRequiredInputs) || !errors.As(err, &newRequired) {
			t.Fatalf("err = %v, want a NewRequiredInputsError", err)
		}
		if len(newRequired.Inputs) != 1 || newRequired.Inputs[0].Name != "owner" {
			t.Errorf("Inputs = %+v", newRequired.Inputs)
		}
		// Answering it lets the upgrade through.
		res, err := f.inst.Setup(ctx, installer.SetupOptions{WorkDir: work, Inputs: map[string]string{"owner": "me"}})
		if err != nil {
			t.Fatalf("Setup with the input answered: %v", err)
		}
		if got := res.Inputs.Spec.Values["owner"]; got != "me" {
			t.Errorf("owner = %v, want me", got)
		}
	})

	t.Run("upload needs a lock", func(t *testing.T) {
		work := t.TempDir()
		if _, err := f.inst.Setup(ctx, installer.SetupOptions{WorkDir: work, Pull: example(t, "example-stack"), Namespace: "x"}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.inst.PrepareUpload(ctx, installer.UploadOptions{WorkDir: work}); !errors.Is(err, installer.ErrNoLock) {
			t.Errorf("err = %v, want ErrNoLock", err)
		}
		if _, err := f.inst.Render(ctx, work); !errors.Is(err, installer.ErrNoLock) {
			t.Errorf("Render err = %v, want ErrNoLock", err)
		}
	})
}

// A problem that doesn't stop setup is a warning, which the program can
// match.
func TestSetupWarnsOfSeveralInstalls(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	work := t.TempDir()
	if _, err := f.inst.Setup(ctx, installer.SetupOptions{WorkDir: work, Pull: example(t, "hello-app"), Namespace: "x", Render: true}); err != nil {
		t.Fatal(err)
	}
	for _, space := range []string{"one", "two"} {
		plan, err := f.inst.PrepareUpload(ctx, installer.UploadOptions{WorkDir: work, Space: space})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.inst.Upload(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}

	res, err := f.inst.Setup(ctx, installer.SetupOptions{WorkDir: t.TempDir(), Pull: example(t, "hello-app"), Namespace: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if res.PriorSource != installer.PriorNone {
		t.Errorf("PriorSource = %q, want none", res.PriorSource)
	}
	if len(f.warnings) != 1 || !errors.Is(f.warnings[0], installer.ErrSeveralInstalls) {
		t.Errorf("warnings = %v, want one ErrSeveralInstalls", f.warnings)
	}
}

// Without Warn, warnings go to ErrOut; with neither, nothing is printed.
func TestDefaultOutput(t *testing.T) {
	ctx := context.Background()
	hub := fakehub.New()
	defer hub.Close()
	client, err := cubapi.NewClient(cubapi.ClientOptions{ServerURL: hub.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := installer.New(installer.Options{ConfigHub: client})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if _, err := quiet.Setup(ctx, installer.SetupOptions{WorkDir: work, Pull: example(t, "hello-app"), Namespace: "x", Render: true}); err != nil {
		t.Fatal(err)
	}
	for _, space := range []string{"one", "two"} {
		plan, err := quiet.PrepareUpload(ctx, installer.UploadOptions{WorkDir: work, Space: space})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := quiet.Upload(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}

	var errOut bytes.Buffer
	loud, err := installer.New(installer.Options{ConfigHub: client, ErrOut: &errOut})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loud.Setup(ctx, installer.SetupOptions{WorkDir: t.TempDir(), Pull: example(t, "hello-app"), Namespace: "y"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "warning: fetch the installer record for hello-app from ConfigHub: hello-app is installed in 2 Spaces (one, two)") {
		t.Errorf("ErrOut = %q", errOut.String())
	}
}

// TransformerCommand replaces re-executing the program for the transform step.
func TestTransformerCommand(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := []string{"/usr/bin/env", "INSTALLER_RUN_TRANSFORMER=1", exe}
	inst, err := installer.New(installer.Options{TransformerCommand: command})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	res, err := inst.Setup(context.Background(), installer.SetupOptions{
		WorkDir: work, Pull: example(t, "hello-app"), Namespace: "demo", Render: true,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if len(res.Render.Manifests) != 3 {
		t.Errorf("manifests = %v", res.Render.Manifests)
	}
	wrapper, err := os.ReadFile(filepath.Join(work, "out", "compose", "installer-transformer.sh"))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("#!/bin/sh\nexec \"/usr/bin/env\" \"INSTALLER_RUN_TRANSFORMER=1\" %q\n", exe)
	if string(wrapper) != want {
		t.Errorf("wrapper = %q, want %q", wrapper, want)
	}
	// The set-namespace transformer ran.
	if _, err := os.Stat(filepath.Join(work, "out", "manifests", "deployment-demo-hello-app.yaml")); err != nil {
		t.Error(err)
	}
}

// withInput copies hello-app with one more input, under version.
func withInput(t *testing.T, version, input string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pkg")
	if err := os.CopyFS(dir, os.DirFS(example(t, "hello-app"))); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "installer.yaml")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(data), "  version: 0.1.0", "  version: "+version, 1)
	s = strings.Replace(s, "  components:\n", "  inputs:\n"+input+"\n  components:\n", 1)
	if err := os.WriteFile(manifest, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// An upgrade the wizard rejects leaves the installed package in place, so
// the next attempt is still compared with the package the install was made
// from.
func TestFailedUpgradeKeepsPackage(t *testing.T) {
	ctx := context.Background()
	inst, err := installer.New(installer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	v1 := withInput(t, "0.1.0", "    - name: port\n      type: string\n      default: http\n")
	v2 := withInput(t, "0.2.0", "    - name: port\n      type: int\n      default: 80\n")
	if _, err := inst.Setup(ctx, installer.SetupOptions{WorkDir: work, Pull: v1, Namespace: "demo"}); err != nil {
		t.Fatal(err)
	}

	// The answer isn't an int, which the wizard finds after the schema diff.
	if _, err := inst.Setup(ctx, installer.SetupOptions{WorkDir: work, Pull: v2, Inputs: map[string]string{"port": "abc"}}); err == nil {
		t.Fatal("Setup accepted a non-int answer to an int input")
	}
	installed, err := os.ReadFile(filepath.Join(work, "package", "installer.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installed), "version: 0.1.0") {
		t.Errorf("the failed upgrade replaced the installed package:\n%s", installed)
	}
	if _, err := inst.Setup(ctx, installer.SetupOptions{WorkDir: work, Pull: v2}); !errors.Is(err, installer.ErrInputTypesChanged) {
		t.Errorf("retried upgrade: err = %v, want ErrInputTypesChanged", err)
	}
	if res, err := inst.Setup(ctx, installer.SetupOptions{WorkDir: work, Pull: v2, Inputs: map[string]string{"port": "8080"}}); err != nil {
		t.Errorf("upgrade answered with an int: %v", err)
	} else if res.Package.InstallerMetadata.Version != "0.2.0" {
		t.Errorf("installed version = %q, want 0.2.0", res.Package.InstallerMetadata.Version)
	}
}
