// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package render

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// keptInManifests are the files in out/manifests/ that render doesn't
// produce but keeps across renders. A Kptfile there makes the directory a
// kpt base package, and its identity and any consumer's upstream pointer
// have to survive an upgrade. See docs/kpt-guide.md.
var keptInManifests = []string{"Kptfile"}

// replaceDir makes dir hold exactly files, plus whichever of keep it already
// held, so that a resource a render no longer produces doesn't linger as a
// stale file. The files are written to a temporary directory beside dir,
// which then takes dir's place, so a write that fails leaves dir as it was.
// With no files and nothing kept, dir is removed.
func replaceDir(dir string, files []File, dirPerm, filePerm os.FileMode, keep []string) error {
	var kept []File
	for _, name := range keep {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		kept = append(kept, File{Filename: name, Body: body})
	}
	if len(files) == 0 && len(kept) == 0 {
		return os.RemoveAll(dir)
	}

	parent, base := filepath.Split(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+base+"-new-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, dirPerm); err != nil {
		return err
	}
	for _, f := range kept {
		if err := os.WriteFile(filepath.Join(tmp, f.Filename), f.Body, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", filepath.Join(dir, f.Filename), err)
		}
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(tmp, f.Filename), f.Body, filePerm); err != nil {
			return fmt.Errorf("write %s: %w", filepath.Join(dir, f.Filename), err)
		}
	}

	// Move the previous output aside rather than removing it first, so it can
	// be put back if the new one can't take its place.
	old := ""
	if _, err := os.Stat(dir); err == nil {
		old, err = os.MkdirTemp(parent, "."+base+"-old-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(old)
		old = filepath.Join(old, base)
		if err := os.Rename(dir, old); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		if old != "" {
			if restoreErr := os.Rename(old, dir); restoreErr != nil {
				return fmt.Errorf("replace %s: %w (and restoring the previous output: %v)", dir, err, restoreErr)
			}
		}
		return fmt.Errorf("replace %s: %w", dir, err)
	}
	return nil
}
