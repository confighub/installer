// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/confighub/installer/pkg/installer"
)

// Install sets up, renders and uploads in one call, and asks ConfirmEmpty
// before an upload that would empty Units.
func TestInstall(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	work := t.TempDir()
	opts := installer.InstallOptions{
		Setup: installer.SetupOptions{
			WorkDir:   work,
			Pull:      example(t, "hello-app"),
			Space:     "hello",
			Namespace: "demo",
			Select:    []string{"monitoring"},
		},
		Upload: installer.UploadOptions{Space: "hello"},
	}

	res, err := f.inst.Install(ctx, opts)
	if err != nil {
		t.Fatalf("Install: %v\n%s", err, f.err.String())
	}
	if res.Setup == nil || res.Setup.Render == nil || len(res.Setup.Render.Manifests) != 4 {
		t.Fatalf("Setup result = %+v, want a render of 4 manifests", res.Setup)
	}
	if res.Upload == nil || !res.Upload.Changed() || res.Upload.Failed() {
		t.Fatalf("Upload result = %+v, want a change and no failures", res.Upload)
	}
	if space := f.hub.Space("hello"); space == nil || space.Labels["Component"] != "hello-app" {
		t.Fatalf("Space hello = %+v", space)
	}
	units := len(f.hub.Units("hello"))

	// Dropping the monitoring component would empty its Unit. A refusal from
	// ConfirmEmpty stops the install before it writes.
	opts.Setup.Select = nil
	opts.Setup.Preset = "minimal"
	var asked []string
	refused := errors.New("refused")
	opts.ConfirmEmpty = func(units []string) error {
		asked = units
		return refused
	}
	f.hub.ClearRequests()
	res, err = f.inst.Install(ctx, opts)
	if !errors.Is(err, refused) {
		t.Fatalf("Install = %v, want the refusal", err)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "servicemonitor") {
		t.Errorf("ConfirmEmpty asked about %v, want the ServiceMonitor's Unit", asked)
	}
	if res.Setup == nil || res.Upload != nil {
		t.Errorf("result = %+v, want a setup and no upload", res)
	}
	for _, r := range f.hub.Requests() {
		if r.Method == "POST" && r.Path == "/upload" && r.Query["dry_run"] != "true" {
			t.Errorf("a refused install uploaded: %s %s %v", r.Method, r.Path, r.Query)
		}
	}
	if got := len(f.hub.Units("hello")); got != units {
		t.Errorf("Units = %d, want %d", got, units)
	}
}

// Without a ConfigHub client, Install fails before pulling anything.
func TestInstallNeedsConfigHub(t *testing.T) {
	inst, err := installer.New(installer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	res, err := inst.Install(context.Background(), installer.InstallOptions{
		Setup: installer.SetupOptions{WorkDir: work, Pull: example(t, "hello-app"), Namespace: "demo"},
	})
	if !errors.Is(err, installer.ErrNoConfigHub) {
		t.Fatalf("Install = %v, want ErrNoConfigHub", err)
	}
	if res.Setup != nil {
		t.Errorf("Install set up without ConfigHub: %+v", res.Setup)
	}
	if _, err := os.Stat(filepath.Join(work, "package")); !os.IsNotExist(err) {
		t.Errorf("Install pulled without ConfigHub: %v", err)
	}
}
