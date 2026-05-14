package wizard

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/confighub/installer/internal/upload"
	"github.com/confighub/installer/pkg/api"
)

// PriorState bundles the documents we recover from a prior install. Any
// field may be nil if it was never written (e.g., Facts when the
// package has no Collector, or PriorPackage when the loader could not
// fetch ConfigHub state and the work-dir's local package has been
// replaced).
type PriorState struct {
	Selection *api.Selection
	Inputs    *api.Inputs
	Facts     *api.Facts
	// PriorPackage is the installer.yaml that produced this state —
	// recovered either from the embedded installer-record (ConfigHub
	// source) or from the work-dir's package/installer.yaml (local
	// source). Used by upgrade's schema-diff to compare new vs old.
	PriorPackage *api.Package
	// Upload is the recorded upload destination, if any.
	Upload *api.Upload
}

// PriorSource names where the prior state came from.
type PriorSource string

const (
	// SourceNone indicates the work-dir had no usable prior state.
	SourceNone PriorSource = "none"
	// SourceLocal indicates state came from out/spec/*.yaml.
	SourceLocal PriorSource = "local"
	// SourceConfigHub indicates state came from the installer-record
	// Unit on ConfigHub (located via out/spec/upload.yaml).
	SourceConfigHub PriorSource = "confighub"
)

// LoadPriorState looks for prior install state in workDir, preferring
// ConfigHub if upload.yaml is present. Returns SourceNone with a nil
// state if no prior state exists.
//
// On ConfigHub fetch failure, falls back to local files and returns a
// best-effort warning via the warn callback (set by the caller — the
// CLI passes a function that prints to stderr; tests pass nil to drop
// the warning silently).
func LoadPriorState(ctx context.Context, workDir string, warn func(string)) (*PriorState, PriorSource, error) {
	specDir := filepath.Join(workDir, "out", "spec")
	uploadPath := filepath.Join(specDir, upload.UploadDocFilename)

	// Step 1: try ConfigHub if upload.yaml is present.
	if data, err := os.ReadFile(uploadPath); err == nil {
		u, perr := api.ParseUpload(data)
		if perr != nil {
			if warn != nil {
				warn(fmt.Sprintf("read %s: %v — falling back to local spec", uploadPath, perr))
			}
		} else {
			parentSlug := parentSpaceSlug(u)
			if parentSlug != "" {
				state, ferr := fetchFromConfigHub(ctx, parentSlug, u)
				if ferr == nil {
					return state, SourceConfigHub, nil
				}
				if warn != nil {
					warn(fmt.Sprintf("fetch installer-record from Space %s: %v — falling back to local spec", parentSlug, ferr))
				}
			}
		}
	}

	// Step 2: try local spec files.
	state, err := loadLocalSpec(workDir)
	if err != nil {
		return nil, SourceNone, err
	}
	if state == nil {
		return nil, SourceNone, nil
	}
	return state, SourceLocal, nil
}

func parentSpaceSlug(u *api.Upload) string {
	for _, s := range u.Spec.Spaces {
		if s.IsParent {
			return s.Slug
		}
	}
	return ""
}

func fetchFromConfigHub(ctx context.Context, parentSlug string, u *api.Upload) (*PriorState, error) {
	cmd := exec.CommandContext(ctx, "cub", "unit", "data",
		"--space", parentSlug, upload.InstallerRecordSlug)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cub unit data installer-record: %w\n%s", err, stderr.String())
	}
	rec, err := upload.SplitInstallerRecord(stdout.Bytes())
	if err != nil {
		return nil, err
	}
	return &PriorState{
		Selection:    rec.Selection,
		Inputs:       rec.Inputs,
		Facts:        rec.Facts,
		PriorPackage: rec.Package,
		Upload:       firstNonNil(rec.Upload, u),
	}, nil
}

func firstNonNil(a, b *api.Upload) *api.Upload {
	if a != nil {
		return a
	}
	return b
}

// loadLocalSpec reads selection.yaml / inputs.yaml / facts.yaml /
// upload.yaml from the work-dir's spec dir. Returns nil with no error
// if no spec files are present (a fresh work-dir is not an error).
//
// PriorPackage is read from <work-dir>/package/installer.yaml so
// upgrade's schema-diff has something to compare against. If the
// package directory is missing, PriorPackage is left nil.
func loadLocalSpec(workDir string) (*PriorState, error) {
	specDir := filepath.Join(workDir, "out", "spec")
	state := &PriorState{}
	any := false

	if data, err := os.ReadFile(filepath.Join(specDir, "selection.yaml")); err == nil {
		s, perr := api.ParseSelection(data)
		if perr != nil {
			return nil, fmt.Errorf("parse selection.yaml: %w", perr)
		}
		state.Selection = s
		any = true
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	if data, err := os.ReadFile(filepath.Join(specDir, "inputs.yaml")); err == nil {
		i, perr := api.ParseInputs(data)
		if perr != nil {
			return nil, fmt.Errorf("parse inputs.yaml: %w", perr)
		}
		state.Inputs = i
		any = true
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	if data, err := os.ReadFile(filepath.Join(specDir, "facts.yaml")); err == nil {
		f, perr := api.ParseFacts(data)
		if perr != nil {
			return nil, fmt.Errorf("parse facts.yaml: %w", perr)
		}
		state.Facts = f
		any = true
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	if data, err := os.ReadFile(filepath.Join(specDir, upload.UploadDocFilename)); err == nil {
		u, perr := api.ParseUpload(data)
		if perr != nil {
			return nil, fmt.Errorf("parse upload.yaml: %w", perr)
		}
		state.Upload = u
		any = true
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	if data, err := os.ReadFile(filepath.Join(workDir, "package", "installer.yaml")); err == nil {
		p, perr := api.ParsePackage(data)
		if perr == nil {
			state.PriorPackage = p
		}
	}

	if !any {
		return nil, nil
	}
	return state, nil
}
