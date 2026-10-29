// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/confighub/installer/pkg/api"
)

// A pull records where the package came from, a local path made absolute, so
// that upload can carry it in the installer record.
func TestPullToWorkDirRecordsSource(t *testing.T) {
	src := testPackage(t)
	workDir := t.TempDir()
	// A relative path, as a user would type it.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PullToWorkDir(context.Background(), rel, workDir); err != nil {
		t.Fatalf("PullToWorkDir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(workDir, "out", api.RecordDir, SourceFile))
	if err != nil {
		t.Fatalf("read %s: %v", SourceFile, err)
	}
	source, err := api.ParseSource(data)
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}
	if source.Spec.Ref != src || source.Spec.Digest != "" {
		t.Errorf("want ref %s and no digest, got %+v", src, source.Spec)
	}
	if source.Metadata.Name != "ocitest-source" {
		t.Errorf("want metadata.name ocitest-source, got %q", source.Metadata.Name)
	}
}
