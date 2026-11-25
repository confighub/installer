// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/confighub/installer/pkg/installer"
)

// A program finds the record of one install, or lists them, and prints one as
// the installer's show command does.
func TestRecords(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	other := filepath.Join(t.TempDir(), "other-app")
	if err := os.CopyFS(other, os.DirFS(example(t, "hello-app"))); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(other, "installer.yaml")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(strings.Replace(string(data), "  name: hello-app", "  name: other-app", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, in := range []struct{ space, pkg string }{
		{"one", example(t, "hello-app")},
		{"two", example(t, "hello-app")},
		{"one", other},
	} {
		opts := installer.InstallOptions{Space: in.space, Setup: installer.SetupOptions{WorkDir: t.TempDir(), Pull: in.pkg, Namespace: "demo"}}
		if _, err := f.inst.Install(ctx, opts); err != nil {
			t.Fatalf("Install %s into %s: %v\n%s", in.pkg, in.space, err, f.err.String())
		}
	}

	for _, tc := range []struct {
		space, pkg string
		wantSpace  string
		wantPkg    string
		wantErr    error
	}{
		{space: "two", wantSpace: "two", wantPkg: "hello-app"},
		{space: "one", pkg: "other-app", wantSpace: "one", wantPkg: "other-app"},
		{pkg: "other-app", wantSpace: "one", wantPkg: "other-app"},
		{space: "one", wantErr: installer.ErrSeveralPackages},
		{pkg: "hello-app", wantErr: installer.ErrSeveralInstalls},
		{space: "nowhere", wantErr: installer.ErrNoInstallerRecord},
		{space: "two", pkg: "other-app", wantErr: installer.ErrNoInstallerRecord},
		{pkg: "nothing", wantErr: installer.ErrNoInstallerRecord},
	} {
		got, err := f.inst.FindRecord(ctx, tc.space, tc.pkg)
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("FindRecord(%q, %q) = %v, want %v", tc.space, tc.pkg, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("FindRecord(%q, %q): %v", tc.space, tc.pkg, err)
			continue
		}
		if got.Space != tc.wantSpace || got.Package() != tc.wantPkg || got.Version() == "" || len(got.Data) == 0 {
			t.Errorf("FindRecord(%q, %q) = Space %q, package %q, version %q; want Space %q, package %q",
				tc.space, tc.pkg, got.Space, got.Package(), got.Version(), tc.wantSpace, tc.wantPkg)
		}
	}
	if _, err := f.inst.FindRecord(ctx, "", ""); err == nil {
		t.Error("FindRecord with neither a Space nor a package succeeded")
	}

	for _, tc := range []struct {
		space, pkg string
		want       []string
	}{
		{want: []string{"one/hello-app", "one/other-app", "two/hello-app"}},
		{pkg: "hello-app", want: []string{"one/hello-app", "two/hello-app"}},
		{space: "one", want: []string{"one/hello-app", "one/other-app"}},
		{space: "nowhere"},
	} {
		recs, err := f.inst.ListRecords(ctx, tc.space, tc.pkg)
		if err != nil {
			t.Errorf("ListRecords(%q, %q): %v", tc.space, tc.pkg, err)
			continue
		}
		var got []string
		for _, r := range recs {
			got = append(got, r.Space+"/"+r.Package())
		}
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("ListRecords(%q, %q) = %v, want %v", tc.space, tc.pkg, got, tc.want)
		}
	}

	found, err := f.inst.FindRecord(ctx, "two", "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := installer.WriteRecord(&out, found); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Package:") || !strings.Contains(out.String(), "hello-app") || !strings.Contains(out.String(), "two") {
		t.Errorf("WriteRecord:\n%s", out.String())
	}

	f.hub.RefuseAuth(true)
	if _, err := f.inst.FindRecord(ctx, "two", ""); !errors.Is(err, installer.ErrNotAuthenticated) {
		t.Errorf("FindRecord with a refused session = %v, want ErrNotAuthenticated", err)
	}
	if _, err := f.inst.ListRecords(ctx, "", ""); !errors.Is(err, installer.ErrNotAuthenticated) {
		t.Errorf("ListRecords with a refused session = %v, want ErrNotAuthenticated", err)
	}
}

// Without a ConfigHub client there are no records to read.
func TestRecordsNeedConfigHub(t *testing.T) {
	inst, err := installer.New(installer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.FindRecord(context.Background(), "two", ""); !errors.Is(err, installer.ErrNoConfigHub) {
		t.Errorf("FindRecord = %v, want ErrNoConfigHub", err)
	}
	if _, err := inst.ListRecords(context.Background(), "", ""); !errors.Is(err, installer.ErrNoConfigHub) {
		t.Errorf("ListRecords = %v, want ErrNoConfigHub", err)
	}
}
