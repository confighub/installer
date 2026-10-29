// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/confighub/installer/pkg/api"
)

func runInit(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newInitCmd()
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd.Execute()
}

func TestInitSourceImportsBase(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"kustomization.yaml": "resources:\n- cm.yaml\n",
		"cm.yaml":            "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: settings\ndata:\n  mode: fast\n",
	} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkgDir := filepath.Join(tmp, "my-package")

	if err := runInit(t, pkgDir, "--source", src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "bases", "default", "cm.yaml")); err != nil {
		t.Errorf("source resource not imported: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(pkgDir, "installer.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := api.ParsePackage(body)
	if err != nil {
		t.Fatal(err)
	}
	if b := pkg.Spec.Bases[0]; b.Path != "bases/default" || !strings.Contains(b.Description, "Imported") {
		t.Errorf("base = %+v, want the imported bases/default", b)
	}
}

func TestInitSourceRefusesNonEmptyBaseBeforeWriting(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "kustomization.yaml"), []byte("resources: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(tmp, "my-package")
	base := filepath.Join(pkgDir, "bases", "default")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "mine.yaml"), []byte("x: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runInit(t, pkgDir, "--source", src, "--force"); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err = %v, want a non-empty refusal", err)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "installer.yaml")); !os.IsNotExist(err) {
		t.Errorf("installer.yaml written despite the failed import")
	}
}

func TestInitSourceScopeRequiresSource(t *testing.T) {
	if err := runInit(t, filepath.Join(t.TempDir(), "p"), "--source-scope", "."); err == nil || !strings.Contains(err.Error(), "requires --source") {
		t.Fatalf("err = %v, want --source-scope to require --source", err)
	}
}
