// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package wizard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/confighub/installer/pkg/api"
)

func TestLoadPriorStateNone(t *testing.T) {
	work := t.TempDir()
	state, src, err := LoadPriorState(context.Background(), work, "hello", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != nil {
		t.Errorf("state should be nil for empty work-dir, got %+v", state)
	}
	if src != SourceNone {
		t.Errorf("source = %q, want %q", src, SourceNone)
	}
}

func TestLoadPriorStateLocal(t *testing.T) {
	work := t.TempDir()
	recordDir := filepath.Join(work, "out", api.RecordDir)
	pkgDir := filepath.Join(work, "package")
	mustWrite(t, filepath.Join(recordDir, "selection.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Selection
metadata: {name: hello-selection}
spec:
  package: hello
  base: default
  components: [foo]
`)
	mustWrite(t, filepath.Join(recordDir, "inputs.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Inputs
metadata: {name: hello-inputs}
spec:
  package: hello
  namespace: demo
  values: {greeting: hi}
`)
	mustWrite(t, filepath.Join(pkgDir, "installer.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata: {name: hello}
installerMetadata: {version: 0.1.0}
spec:
  bases:
    - {name: default, path: bases/default, default: true}
`)

	state, src, err := LoadPriorState(context.Background(), work, "hello", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src != SourceLocal {
		t.Errorf("source = %q, want %q", src, SourceLocal)
	}
	if state == nil {
		t.Fatal("state should not be nil")
	}
	if state.Selection == nil || state.Selection.Spec.Base != "default" {
		t.Errorf("Selection mismatch: %+v", state.Selection)
	}
	if state.Inputs == nil || state.Inputs.Spec.Namespace != "demo" {
		t.Errorf("Inputs mismatch: %+v", state.Inputs)
	}
	if state.PriorPackage == nil || state.PriorPackage.Metadata.Name != "hello" {
		t.Errorf("PriorPackage mismatch: %+v", state.PriorPackage)
	}
}

// A work-dir with its own record uses it, without asking ConfigHub.
func TestLoadPriorStatePrefersLocal(t *testing.T) {
	work := t.TempDir()
	mustWrite(t, filepath.Join(work, "out", api.RecordDir, "selection.yaml"), `apiVersion: installer.confighub.com/v1alpha1
kind: Selection
metadata: {name: x}
spec: {package: hello, base: default}
`)
	fetch := func(context.Context, string) ([]byte, error) {
		t.Fatal("ConfigHub was asked although the work-dir has a record")
		return nil, nil
	}
	_, src, err := LoadPriorState(context.Background(), work, "hello", fetch, nil)
	if err != nil || src != SourceLocal {
		t.Fatalf("source = %q, err = %v", src, err)
	}
}

// A work-dir without a record, such as a fresh clone, is recovered from the
// installer record in ConfigHub.
func TestLoadPriorStateFromConfigHub(t *testing.T) {
	record := []byte(`configHub:
  configSchema: InstallerRecord
  configName: installer
package:
  name: hello
  installerMetadata: {version: 0.1.0}
  spec:
    bases:
      - {name: default, path: bases/default, default: true}
selection: {base: default, components: [foo]}
inputs: {namespace: demo, values: {greeting: hi}}
`)
	var asked string
	fetch := func(_ context.Context, name string) ([]byte, error) {
		asked = name
		return record, nil
	}
	state, src, err := LoadPriorState(context.Background(), t.TempDir(), "hello", fetch, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src != SourceConfigHub || asked != "hello" {
		t.Fatalf("source = %q, asked for %q", src, asked)
	}
	if state.Inputs.Spec.Namespace != "demo" || state.Inputs.Spec.Package != "hello" {
		t.Errorf("Inputs = %+v", state.Inputs.Spec)
	}
	if state.PriorPackage == nil || state.PriorPackage.InstallerMetadata.Version != "0.1.0" {
		t.Errorf("PriorPackage = %+v", state.PriorPackage)
	}
}

// A ConfigHub failure warns and starts fresh rather than failing setup.
func TestLoadPriorStateFetchFailureWarns(t *testing.T) {
	warned := 0
	fetch := func(context.Context, string) ([]byte, error) { return nil, errors.New("no session") }
	state, src, err := LoadPriorState(context.Background(), t.TempDir(), "hello", fetch, func(string) { warned++ })
	if err != nil || state != nil || src != SourceNone {
		t.Fatalf("state = %+v, source = %q, err = %v", state, src, err)
	}
	if warned != 1 {
		t.Errorf("warned %d times, want 1", warned)
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
