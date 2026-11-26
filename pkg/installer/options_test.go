// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/confighub/sdk/core/cubapi"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/confighub/installer/internal/fakehub"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/testregistry"
	"github.com/confighub/installer/pkg/api"
	"github.com/confighub/installer/pkg/installer"
)

// staticStore is a credential store holding one registry's credentials.
type staticStore struct {
	host string
	cred auth.Credential
}

func (s staticStore) Get(_ context.Context, serverAddress string) (auth.Credential, error) {
	if serverAddress == s.host {
		return s.cred, nil
	}
	return auth.EmptyCredential, nil
}

func (staticStore) Put(context.Context, string, auth.Credential) error { return nil }
func (staticStore) Delete(context.Context, string) error               { return nil }

// pushHello pushes hello-app to reg and returns its reference.
func pushHello(t *testing.T, reg *testregistry.Registry, creds staticStore) string {
	t.Helper()
	ref := "oci://" + reg.Host + "/pkgs/hello-app:v0.1.0"
	if _, err := (ipkg.Registry{Credentials: creds}).Push(context.Background(), example(t, "hello-app"), ref); err != nil {
		t.Fatalf("push: %v", err)
	}
	return ref
}

// The registry credentials are the ones Options gives.
func TestRegistryCredentials(t *testing.T) {
	ctx := context.Background()
	reg := testregistry.New(testregistry.BasicAuth("robot", "s3cret"))
	defer reg.Close()
	good := staticStore{host: reg.Host, cred: auth.Credential{Username: "robot", Password: "s3cret"}}
	ref := pushHello(t, reg, good)

	inst, err := installer.New(installer.Options{Registry: installer.RegistryOptions{Credentials: good}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Setup(ctx, installer.SetupOptions{WorkDir: t.TempDir(), Pull: ref, Namespace: "demo"}); err != nil {
		t.Errorf("Setup with the registry's credentials: %v", err)
	}

	wrong := staticStore{host: reg.Host, cred: auth.Credential{Username: "robot", Password: "wrong"}}
	inst, err = installer.New(installer.Options{Registry: installer.RegistryOptions{Credentials: wrong}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Setup(ctx, installer.SetupOptions{WorkDir: t.TempDir(), Pull: ref, Namespace: "demo"}); err == nil {
		t.Error("Setup with the wrong credentials pulled the package")
	}
}

// A bare package name pulls the newest release from the package repository
// Options names, and says which reference that was.
func TestPackageRepository(t *testing.T) {
	reg := testregistry.New()
	defer reg.Close()
	for _, tag := range []string{"v0.1.0", "v0.2.0"} {
		if _, err := (ipkg.Registry{}).Push(context.Background(), example(t, "hello-app"), "oci://"+reg.Host+"/packages/hello-app:"+tag); err != nil {
			t.Fatalf("push: %v", err)
		}
	}
	var out bytes.Buffer
	inst, err := installer.New(installer.Options{Out: &out, Registry: installer.RegistryOptions{PackageRepository: reg.Host + "/packages"}})
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	if _, err := inst.Setup(context.Background(), installer.SetupOptions{WorkDir: workDir, Pull: "hello-app", Namespace: "demo"}); err != nil {
		t.Fatalf("Setup of a bare name: %v", err)
	}
	want := "oci://" + reg.Host + "/packages/hello-app:v0.2.0"
	if !strings.Contains(out.String(), "Pulling "+want+" for package hello-app.") {
		t.Errorf("output does not name the release pulled:\n%s", out.String())
	}
	data, err := os.ReadFile(filepath.Join(workDir, "out", api.RecordDir, ipkg.SourceFile))
	if err != nil {
		t.Fatal(err)
	}
	if source, err := api.ParseSource(data); err != nil || source.Spec.Ref != want {
		t.Errorf("recorded source = %+v, %v; want %s", source, err, want)
	}
}

// fakeCosign writes a cosign stand-in that records its arguments in log and
// exits with status.
func fakeCosign(t *testing.T, log string, status int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cosign")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\nexit " + string(rune('0'+status)) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The signing policy and cosign binary are the ones Options gives, and are
// enforced on the package pulled.
func TestSigningPolicy(t *testing.T) {
	ctx := context.Background()
	reg := testregistry.New()
	defer reg.Close()
	ref := pushHello(t, reg, staticStore{})
	policy := &api.SigningPolicy{Spec: api.SigningPolicySpec{
		Enforce:     true,
		TrustedKeys: []api.TrustedKey{{PublicKey: "/keys/release.pub"}},
	}}

	log := filepath.Join(t.TempDir(), "cosign.log")
	inst, err := installer.New(installer.Options{SigningPolicy: policy, CosignBinary: fakeCosign(t, log, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Setup(ctx, installer.SetupOptions{WorkDir: t.TempDir(), Pull: ref, Namespace: "demo"}); err != nil {
		t.Fatalf("Setup with a verified package: %v", err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if want := "verify --key /keys/release.pub " + reg.Host + "/pkgs/hello-app@sha256:"; !strings.HasPrefix(string(calls), want) {
		t.Errorf("cosign called with %q, want a call starting %q", calls, want)
	}

	inst, err = installer.New(installer.Options{SigningPolicy: policy, CosignBinary: fakeCosign(t, log, 1)})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	_, err = inst.Setup(ctx, installer.SetupOptions{WorkDir: work, Pull: ref, Namespace: "demo"})
	if err == nil || !strings.Contains(err.Error(), "no trust entry verified") {
		t.Fatalf("Setup with a package that fails verification: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "package")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a package that failed verification is in the work-dir: %v", err)
	}
}

// A package collector's stderr goes to ErrOut.
func TestCollectorStderr(t *testing.T) {
	pkg := filepath.Join(t.TempDir(), "pkg")
	if err := os.CopyFS(pkg, os.DirFS(example(t, "hello-app"))); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(pkg, "installer.yaml")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	withCollector := strings.Replace(string(data), "  components:\n", "  collector:\n    command: ./collector.sh\n\n  components:\n", 1)
	if err := os.WriteFile(manifest, []byte(withCollector), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho 'collecting facts' >&2\necho 'zone: a'\n"
	if err := os.WriteFile(filepath.Join(pkg, "collector.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	inst, err := installer.New(installer.Options{ErrOut: &errOut})
	if err != nil {
		t.Fatal(err)
	}
	res, err := inst.Setup(context.Background(), installer.SetupOptions{WorkDir: t.TempDir(), Pull: pkg, Namespace: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Facts == nil || res.Facts.Spec.Values["zone"] != "a" {
		t.Errorf("facts = %+v", res.Facts)
	}
	if !strings.Contains(errOut.String(), "collecting facts") {
		t.Errorf("ErrOut = %q, want the collector's stderr", errOut.String())
	}
}

// Uploads carry Options.ClientVersion, or the installer's own version.
func TestClientVersion(t *testing.T) {
	ctx := context.Background()
	hub := fakehub.New()
	defer hub.Close()
	client, err := cubapi.NewClient(cubapi.ClientOptions{ServerURL: hub.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	quiet, err := installer.New(installer.Options{ConfigHub: client})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := quiet.Setup(ctx, installer.SetupOptions{WorkDir: work, Pull: example(t, "hello-app"), Namespace: "demo", Render: true}); err != nil {
		t.Fatal(err)
	}
	for want, opts := range map[string]installer.Options{
		"1.2.3": {ConfigHub: client, ClientVersion: "1.2.3"},
		// A test binary is built from the installer module itself, which has
		// no version of its own.
		"dev": {ConfigHub: client},
	} {
		inst, err := installer.New(opts)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := inst.PrepareUpload(ctx, installer.UploadOptions{WorkDir: work, Space: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		if got := plan.Packages[0].Request.Source.ClientVersion; got != want {
			t.Errorf("ClientVersion = %q, want %q", got, want)
		}
	}
}

// Without a ConfigHub client, setup re-enters only from the work-dir, and
// what needs ConfigHub says so.
func TestNoConfigHub(t *testing.T) {
	ctx := context.Background()
	var warnings []error
	inst, err := installer.New(installer.Options{Warn: func(err error) { warnings = append(warnings, err) }})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	res, err := inst.Setup(ctx, installer.SetupOptions{WorkDir: work, Pull: example(t, "hello-app"), Namespace: "demo", Render: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.PriorSource != installer.PriorNone || len(warnings) != 0 {
		t.Errorf("PriorSource = %q, warnings = %v; want none, none", res.PriorSource, warnings)
	}
	if _, err := inst.Setup(ctx, installer.SetupOptions{WorkDir: t.TempDir(), Space: "hello"}); !errors.Is(err, installer.ErrNoConfigHub) {
		t.Errorf("Setup with a Space: err = %v, want ErrNoConfigHub", err)
	}
	if _, err := inst.PrepareUpload(ctx, installer.UploadOptions{WorkDir: work, Space: "hello"}); !errors.Is(err, installer.ErrNoConfigHub) {
		t.Errorf("PrepareUpload: err = %v, want ErrNoConfigHub", err)
	}
}

// An upload whose ConfigHub session is refused stops before anything else.
func TestNotAuthenticated(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	work := t.TempDir()
	if _, err := f.inst.Setup(ctx, installer.SetupOptions{WorkDir: work, Pull: example(t, "hello-app"), Namespace: "demo", Render: true}); err != nil {
		t.Fatal(err)
	}
	f.hub.ClearRequests()
	f.hub.RefuseAuth(true)
	if _, err := f.inst.PrepareUpload(ctx, installer.UploadOptions{WorkDir: work, Space: "hello"}); !errors.Is(err, installer.ErrNotAuthenticated) {
		t.Errorf("err = %v, want ErrNotAuthenticated", err)
	}
	if reqs := f.hub.Requests(); len(reqs) != 1 || reqs[0].Path != "/me" {
		t.Errorf("requests = %+v, want only GET /me", reqs)
	}
}

// An Installer reads none of the environment variables, home-directory files
// or cub configuration the installer CLI does: each of them here would change
// what Setup does if it were read.
func TestIgnoresEnvironmentAndHome(t *testing.T) {
	ctx := context.Background()
	reg := testregistry.New()
	defer reg.Close()
	ref := pushHello(t, reg, staticStore{})

	// A ConfigHub holding an earlier install, which the cub environment names.
	hub := fakehub.New()
	defer hub.Close()
	client, err := cubapi.NewClient(cubapi.ClientOptions{ServerURL: hub.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := installer.New(installer.Options{ConfigHub: client})
	if err != nil {
		t.Fatal(err)
	}
	seedWork := t.TempDir()
	if _, err := seed.Setup(ctx, installer.SetupOptions{WorkDir: seedWork, Pull: example(t, "hello-app"), Namespace: "seeded", Render: true}); err != nil {
		t.Fatal(err)
	}
	plan, err := seed.PrepareUpload(ctx, installer.UploadOptions{WorkDir: seedWork, Space: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Upload(ctx, plan); err != nil {
		t.Fatal(err)
	}
	hub.ClearRequests()

	// An enforcing signing policy whose cosign fails, where the CLI looks.
	home := t.TempDir()
	policy := "apiVersion: installer.confighub.com/v1alpha1\nkind: SigningPolicy\nspec:\n  enforce: true\n  trustedKeys:\n    - publicKey: /k.pub\n"
	policyPath := filepath.Join(home, ".config", "installer", "policy.yaml")
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("INSTALLER_SIGNING_POLICY", policyPath)
	t.Setenv("INSTALLER_COSIGN_BIN", fakeCosign(t, filepath.Join(home, "cosign.log"), 1))
	t.Setenv("CUB_SERVER", hub.URL)
	t.Setenv("CUB_TOKEN", "t")

	inst, err := installer.New(installer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := inst.Setup(ctx, installer.SetupOptions{WorkDir: t.TempDir(), Pull: ref, Namespace: "demo", Render: true})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if res.PriorSource != installer.PriorNone || res.Inputs.Spec.Namespace != "demo" {
		t.Errorf("PriorSource = %q, namespace = %q; the install was re-entered from ConfigHub", res.PriorSource, res.Inputs.Spec.Namespace)
	}
	if reqs := hub.Requests(); len(reqs) != 0 {
		t.Errorf("Setup reached the ConfigHub the environment names: %+v", reqs)
	}
	if _, err := os.Stat(filepath.Join(home, "cosign.log")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Setup ran the cosign the environment names: %v", err)
	}
}

// processOutput runs fn and returns what reached the process's stderr: through
// slog's default logger, which writes through the log package's, or from a
// child process kustomize runs, which inherits os.Stderr.
func processOutput(t *testing.T, fn func()) string {
	t.Helper()
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	piped := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		piped <- b
	}()
	fn()
	os.Stderr = stderr
	w.Close()
	return logged.String() + string(<-piped)
}

// The functions a render runs, in this process and in the transform step's,
// log to ErrOut, and nothing reaches the process's stderr.
func TestFunctionLogGoesToErrOut(t *testing.T) {
	var errOut bytes.Buffer
	inst, err := installer.New(installer.Options{ErrOut: &errOut})
	if err != nil {
		t.Fatal(err)
	}
	leaked := processOutput(t, func() {
		_, err = inst.Setup(context.Background(), installer.SetupOptions{
			WorkDir: t.TempDir(), Pull: example(t, "hello-app"), Namespace: "demo", Render: true,
			// Overriding an image edits the kustomization with set-yq, in
			// this process.
			SetImage: map[string]string{"nginxdemos/hello": "nginxdemos/hello:0.4"},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if leaked != "" {
		t.Errorf("wrote to the process's stderr:\n%s", leaked)
	}
	for _, want := range []string{"INFO invoke set-yq: succeeded", "INFO invoke set-namespace: succeeded"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("ErrOut has no %q:\n%s", want, errOut.String())
		}
	}
}

// A transform step that fails says why in the error, not on the process's
// stderr.
func TestFailedTransformStepIsInTheError(t *testing.T) {
	pkg := filepath.Join(t.TempDir(), "pkg")
	if err := os.CopyFS(pkg, os.DirFS(example(t, "hello-app"))); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(pkg, "installer.yaml")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(strings.Replace(string(data), "- name: set-namespace", "- name: no-such-function", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	inst, err := installer.New(installer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	leaked := processOutput(t, func() {
		_, err = inst.Setup(context.Background(), installer.SetupOptions{
			WorkDir: t.TempDir(), Pull: pkg, Namespace: "demo", Render: true,
		})
	})
	if err == nil || !strings.Contains(err.Error(), `"no-such-function"`) {
		t.Errorf("Setup error = %v, want one naming the function", err)
	}
	if leaked != "" {
		t.Errorf("wrote to the process's stderr:\n%s", leaked)
	}
}
